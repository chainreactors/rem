//go:build !tinygo

package runner

import (
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// ---------------------------------------------------------------------------
// Stress / boundary scenario tests
// Require REM_BENCH=1 to run (same gate as performance benchmarks)
// ---------------------------------------------------------------------------

func skipUnlessBench(t *testing.T) {
	t.Helper()
	if os.Getenv("REM_BENCH") == "" {
		t.Skip("set REM_BENCH=1 to run stress tests")
	}
}

// --- Scenario 1: Burst connections (rapid open/close) ---
// Creates many short-lived connections in quick succession to stress
// bridge creation/teardown and yamux stream lifecycle.

func TestStress_BurstConnections(t *testing.T) {
	skipUnlessBench(t)

	const (
		totalConns  = 200
		concurrency = 30
	)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	targetAddr := ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	socksAddr := setupBenchProxy(t, modeTCP)
	dialer := newBenchSOCKS5Dialer(t, socksAddr)

	var (
		wg       sync.WaitGroup
		errCount int64
		sem      = make(chan struct{}, concurrency)
	)

	start := time.Now()
	for i := 0; i < totalConns; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := dialer.Dial("tcp", targetAddr)
			if err != nil {
				atomic.AddInt64(&errCount, 1)
				return
			}
			msg := []byte("ping")
			conn.Write(msg)
			if tc, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
				tc.SetDeadline(time.Now().Add(10 * time.Second))
			}
			buf := make([]byte, len(msg))
			io.ReadFull(conn, buf)
			conn.Close()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	t.Logf("BurstConnections: %d conns in %v (%.0f conn/s), errors: %d",
		totalConns, elapsed, float64(totalConns)/elapsed.Seconds(), errCount)

	if errCount > int64(totalConns/10) {
		t.Fatalf("too many errors: %d/%d", errCount, totalConns)
	}
}

// --- Scenario 2: Spike load (concurrent large + small requests) ---
// Simulates a realistic mixed workload: some connections transfer large
// data while many small connections compete for yamux streams.

func TestStress_SpikeLoad(t *testing.T) {
	skipUnlessBench(t)

	const (
		heavyConns = 5
		heavySize  = 10 * 1024 * 1024 // 10MB each
		lightConns = 50
	)

	// Data server for heavy connections
	heavyData := make([]byte, heavySize)
	rand.Read(heavyData)

	dataLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dataLn.Close()
	dataAddr := dataLn.Addr().String()

	go func() {
		for {
			conn, err := dataLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write(heavyData)
			}(conn)
		}
	}()

	// Echo server for light connections
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	echoAddr := echoLn.Addr().String()

	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	socksAddr := setupBenchProxy(t, modeTCP)
	dialer := newBenchSOCKS5Dialer(t, socksAddr)

	var (
		wg         sync.WaitGroup
		heavyBytes int64
		heavyErrs  int64
		lightErrs  int64
	)

	start := time.Now()

	// Launch heavy transfers
	for i := 0; i < heavyConns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := dialer.Dial("tcp", dataAddr)
			if err != nil {
				atomic.AddInt64(&heavyErrs, 1)
				return
			}
			n, _ := io.Copy(io.Discard, conn)
			conn.Close()
			atomic.AddInt64(&heavyBytes, n)
		}()
	}

	// Launch light echo connections with concurrency limit
	sem := make(chan struct{}, 20)
	for i := 0; i < lightConns; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := dialer.Dial("tcp", echoAddr)
			if err != nil {
				atomic.AddInt64(&lightErrs, 1)
				return
			}
			if tc, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
				tc.SetDeadline(time.Now().Add(30 * time.Second))
			}
			msg := []byte("hello")
			conn.Write(msg)
			buf := make([]byte, len(msg))
			_, err = io.ReadFull(conn, buf)
			conn.Close()
			if err != nil {
				atomic.AddInt64(&lightErrs, 1)
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	mbps := float64(heavyBytes) / elapsed.Seconds() / 1024 / 1024
	t.Logf("SpikeLoad: heavy=%d×10MB (%.1f MB/s), light=%d echo, duration=%v",
		heavyConns, mbps, lightConns, elapsed)
	t.Logf("  heavy_errors=%d, light_errors=%d", heavyErrs, lightErrs)

	if heavyErrs > 0 {
		t.Fatalf("heavy transfer errors: %d", heavyErrs)
	}
	if lightErrs > int64(lightConns/5) {
		t.Fatalf("too many light errors: %d/%d", lightErrs, lightConns)
	}
}

// --- Scenario 3: Rapid open/close (bridge lifecycle stress) ---
// Opens and immediately closes connections to test bridge cleanup
// under rapid churn. This catches goroutine leaks and race conditions
// in BridgeOpen/BridgeClose handling.

func TestStress_RapidOpenClose(t *testing.T) {
	skipUnlessBench(t)

	const totalConns = 500

	// Server that accepts and immediately closes
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	targetAddr := ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	socksAddr := setupBenchProxy(t, modeTCP)
	dialer := newBenchSOCKS5Dialer(t, socksAddr)

	var (
		wg       sync.WaitGroup
		errCount int64
		sem      = make(chan struct{}, 50) // limit concurrency
	)

	start := time.Now()
	for i := 0; i < totalConns; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := dialer.Dial("tcp", targetAddr)
			if err != nil {
				atomic.AddInt64(&errCount, 1)
				return
			}
			conn.Close()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	t.Logf("RapidOpenClose: %d conns in %v (%.0f/s), errors: %d",
		totalConns, elapsed, float64(totalConns)/elapsed.Seconds(), errCount)

	if errCount > int64(totalConns/5) {
		t.Fatalf("too many errors: %d/%d", errCount, totalConns)
	}
}

// --- Scenario 4: Large single stream (yamux flow control stress) ---
// Transfers a very large payload through a single yamux stream to test
// flow control window updates under sustained pressure.

func TestStress_LargeSingleStream(t *testing.T) {
	skipUnlessBench(t)

	const sizeMB = 256

	dataSize := sizeMB * 1024 * 1024
	data := make([]byte, dataSize)
	rand.Read(data)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	targetAddr := ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write(data)
			}(conn)
		}
	}()

	socksAddr := setupBenchProxy(t, modeTCP)
	dialer := newBenchSOCKS5Dialer(t, socksAddr)

	start := time.Now()
	conn, err := dialer.Dial("tcp", targetAddr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	n, err := io.Copy(io.Discard, conn)
	conn.Close()
	elapsed := time.Since(start)

	mbps := float64(n) / elapsed.Seconds() / 1024 / 1024
	t.Logf("LargeSingleStream: %dMB in %v (%.1f MB/s)", sizeMB, elapsed, mbps)

	if err != nil || n != int64(dataSize) {
		t.Fatalf("transfer failed: got %d/%d bytes, err=%v", n, dataSize, err)
	}
}

// --- Scenario 5: UDP burst connections ---
// Same as BurstConnections but over UDP/KCP tunnel.

func TestStress_UDP_BurstConnections(t *testing.T) {
	skipUnlessBench(t)

	const totalConns = 50

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	targetAddr := ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	socksAddr := setupBenchProxy(t, modeUDP)
	dialer, err2 := proxy.SOCKS5("tcp", socksAddr, socksAuth(), proxy.Direct)
	if err2 != nil {
		t.Fatal(err2)
	}

	var (
		wg       sync.WaitGroup
		errCount int64
		sem      = make(chan struct{}, 10)
	)

	start := time.Now()
	for i := 0; i < totalConns; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := dialer.Dial("tcp", targetAddr)
			if err != nil {
				atomic.AddInt64(&errCount, 1)
				return
			}
			msg := []byte("ping")
			conn.Write(msg)
			if tc, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
				tc.SetDeadline(time.Now().Add(10 * time.Second))
			}
			buf := make([]byte, len(msg))
			io.ReadFull(conn, buf)
			conn.Close()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	t.Logf("UDP BurstConnections: %d conns in %v (%.0f/s), errors: %d",
		totalConns, elapsed, float64(totalConns)/elapsed.Seconds(), errCount)

	if errCount > int64(totalConns/5) {
		t.Fatalf("too many errors: %d/%d", errCount, totalConns)
	}
}

// suppress unused import warning
var _ = fmt.Sprintf
