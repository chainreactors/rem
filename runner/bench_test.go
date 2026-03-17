//go:build !tinygo

package runner

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// ---------------------------------------------------------------------------
// Bench infrastructure: generic SOCKS5 proxy setup for TCP and Duplex modes
// ---------------------------------------------------------------------------

type benchMode string

const (
	modeTCP    benchMode = "tcp"
	modeDuplex benchMode = "duplex"
	modeUDP    benchMode = "udp"
)

func benchExternalBin() string {
	return os.Getenv("REM_BENCH_BIN")
}

func startBenchHelperProcess(t *testing.T, helperCmd string) *exec.Cmd {
	t.Helper()
	proc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	proc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=%s", helperCmd),
	)
	if err := proc.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	t.Cleanup(func() {
		proc.Process.Kill()
		proc.Wait()
	})
	return proc
}

// setupBenchProxy launches server+client subprocesses and returns the SOCKS5 addr.
// Supports both normal TCP and duplex (TCP-up + UDP-down) modes.
func setupBenchProxy(t *testing.T, mode benchMode) string {
	t.Helper()
	switch mode {
	case modeTCP:
		return setupBenchTCP(t)
	case modeDuplex:
		return setupBenchDuplex(t)
	case modeUDP:
		return setupBenchUDP(t)
	default:
		t.Fatalf("unknown mode: %s", mode)
		return ""
	}
}

func waitForTCPReady(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func setupBenchTCP(t *testing.T) string {
	t.Helper()
	bin := benchExternalBin()

	if bin != "" {
		const maxAttempts = 4
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			serverPort := freePort(t)
			socksPort := freePort(t)
			alias := fmt.Sprintf("bench%d_a%d", atomic.AddUint32(&testCounter, 1), attempt)

			srv := exec.Command(bin,
				"-s", fmt.Sprintf("tcp://127.0.0.1:%d", serverPort),
				"--no-sub",
			)
			srv.Env = os.Environ()
			srv.Stdout = os.Stdout
			srv.Stderr = os.Stderr
			if err := srv.Start(); err != nil {
				t.Fatalf("start rem server: %v", err)
			}
			// TinyGo TCP server may not handle raw probe connections robustly.
			// Avoid pre-dial probing and allow server setup by delay.
			time.Sleep(2 * time.Second)

			cli := exec.Command(bin,
				"-c", fmt.Sprintf("tcp://127.0.0.1:%d/?retry=30&retry-interval=1", serverPort),
				"-m", "proxy",
				"-l", fmt.Sprintf("socks5://127.0.0.1:%d", socksPort),
				"-a", alias,
			)
			cli.Env = os.Environ()
			cli.Stdout = os.Stdout
			cli.Stderr = os.Stderr
			if err := cli.Start(); err != nil {
				srv.Process.Kill()
				srv.Wait()
				t.Fatalf("start rem client: %v", err)
			}

			socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
			if waitForTCPReady(socksAddr, 35*time.Second) {
				t.Cleanup(func() { cli.Process.Kill(); cli.Wait() })
				t.Cleanup(func() { srv.Process.Kill(); srv.Wait() })
				time.Sleep(500 * time.Millisecond)
				return socksAddr
			}

			cli.Process.Kill()
			cli.Wait()
			srv.Process.Kill()
			srv.Wait()
		}
		t.Fatalf("tinygo bench tcp setup failed after retries")
	}

	serverPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("bench%d", atomic.AddUint32(&testCounter, 1))

	if bin == "" {
		startBenchHelperProcess(t,
			fmt.Sprintf("-s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort))
	}

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", serverPort), 10*time.Second)

	if bin == "" {
		startBenchHelperProcess(t,
			fmt.Sprintf("-c tcp://127.0.0.1:%d/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s",
				serverPort, socksPort, alias))
	}

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	return socksAddr
}

func setupBenchDuplex(t *testing.T) string {
	t.Helper()
	if benchExternalBin() != "" {
		t.Skip("duplex benchmark in REM_BENCH_BIN mode is not supported")
	}

	tcpPort := freePort(t)
	udpPort := freeUDPPort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("benchdx%d", atomic.AddUint32(&testCounter, 1))

	srvProc := exec.Command(os.Args[0], "-test.run=^TestHelperDuplexServer$", "-test.v")
	srvProc.Env = append(os.Environ(), "REM_DUPLEX_SERVER=1",
		fmt.Sprintf("REM_TCP_PORT=%d", tcpPort),
		fmt.Sprintf("REM_UDP_PORT=%d", udpPort))
	if err := srvProc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srvProc.Process.Kill(); srvProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", tcpPort), 10*time.Second)
	time.Sleep(1 * time.Second)

	cliProc := exec.Command(os.Args[0], "-test.run=^TestHelperDuplexClient$", "-test.v")
	cliProc.Env = append(os.Environ(), "REM_DUPLEX_CLIENT=1",
		fmt.Sprintf("REM_TCP_ADDR=127.0.0.1:%d", tcpPort),
		fmt.Sprintf("REM_UDP_ADDR=127.0.0.1:%d", udpPort),
		fmt.Sprintf("REM_SOCKS_PORT=%d", socksPort),
		fmt.Sprintf("REM_ALIAS=%s", alias))
	if err := cliProc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cliProc.Process.Kill(); cliProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	return socksAddr
}

func setupBenchUDP(t *testing.T) string {
	t.Helper()
	bin := benchExternalBin()

	if bin != "" {
		t.Skip("udp benchmark in REM_BENCH_BIN mode requires tinygo udp registration; current minimal tinygo build only includes tcp/memory")
	}

	serverPort := freeUDPPort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("benchudp%d", atomic.AddUint32(&testCounter, 1))

	if bin == "" {
		startBenchHelperProcess(t,
			fmt.Sprintf("-s udp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort))
	}

	// UDP has no TCP listener to probe; just wait a fixed duration
	time.Sleep(2 * time.Second)

	if bin == "" {
		startBenchHelperProcess(t,
			fmt.Sprintf("-c udp://127.0.0.1:%d/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s",
				serverPort, socksPort, alias))
	}

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	return socksAddr
}

// newBenchSOCKS5Client creates an http.Client that routes through SOCKS5.
func newBenchSOCKS5Client(t *testing.T, socksAddr string) *http.Client {
	t.Helper()
	dialer, err := proxy.SOCKS5("tcp", socksAddr, socksAuth(), proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Dial:                dialer.Dial,
			MaxIdleConnsPerHost: 256,
			MaxIdleConns:        256,
			DisableKeepAlives:   false,
		},
		Timeout: 60 * time.Second,
	}
}

// newBenchSOCKS5Dialer creates a raw SOCKS5 dialer for TCP-level benchmarks.
func newBenchSOCKS5Dialer(t *testing.T, socksAddr string) proxy.Dialer {
	t.Helper()
	dialer, err := proxy.SOCKS5("tcp", socksAddr, socksAuth(), proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	return dialer
}

// ---------------------------------------------------------------------------
// Results reporting
// ---------------------------------------------------------------------------

type benchResult struct {
	Scenario   string
	Mode       benchMode
	Duration   time.Duration
	TotalBytes int64
	TotalReqs  int64
	Errors     int64
	Latencies  []time.Duration // per-request latencies (for concurrency tests)
}

func (r *benchResult) report(t *testing.T) {
	t.Helper()
	t.Logf("=== %s [%s] ===", r.Scenario, r.Mode)
	t.Logf("  Duration:    %v", r.Duration)
	t.Logf("  Requests:    %d (errors: %d)", r.TotalReqs, r.Errors)

	if r.TotalBytes > 0 {
		mbps := float64(r.TotalBytes) / r.Duration.Seconds() / 1024 / 1024
		t.Logf("  Throughput:  %.2f MB/s (%d bytes)", mbps, r.TotalBytes)
	}
	if r.TotalReqs > 0 {
		rps := float64(r.TotalReqs) / r.Duration.Seconds()
		t.Logf("  Req/s:       %.1f", rps)
	}
	if len(r.Latencies) > 0 {
		sort.Slice(r.Latencies, func(i, j int) bool { return r.Latencies[i] < r.Latencies[j] })
		n := len(r.Latencies)
		t.Logf("  Latency p50: %v", r.Latencies[n/2])
		t.Logf("  Latency p95: %v", r.Latencies[n*95/100])
		t.Logf("  Latency p99: %v", r.Latencies[n*99/100])
	}
}

// ---------------------------------------------------------------------------
// Scenario 1: High Concurrency — many small HTTP requests
// ---------------------------------------------------------------------------

func benchHighConcurrency(t *testing.T, mode benchMode, concurrency, totalReqs int) *benchResult {
	t.Helper()
	// Target: tiny 64-byte response
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	socksAddr := setupBenchProxy(t, mode)
	client := newBenchSOCKS5Client(t, socksAddr)

	// Warm up
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("warmup: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	var (
		wg        sync.WaitGroup
		errCount  int64
		latencies = make([]time.Duration, totalReqs)
		sem       = make(chan struct{}, concurrency)
	)

	start := time.Now()
	for i := 0; i < totalReqs; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			reqStart := time.Now()
			resp, err := client.Get(ts.URL)
			if err != nil {
				atomic.AddInt64(&errCount, 1)
				latencies[idx] = time.Since(reqStart)
				return
			}
			io.ReadAll(resp.Body)
			resp.Body.Close()
			latencies[idx] = time.Since(reqStart)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Filter zero latencies (shouldn't happen but be safe)
	valid := latencies[:0]
	for _, l := range latencies {
		if l > 0 {
			valid = append(valid, l)
		}
	}

	return &benchResult{
		Scenario:  fmt.Sprintf("HighConcurrency(c=%d,n=%d)", concurrency, totalReqs),
		Mode:      mode,
		Duration:  elapsed,
		TotalReqs: int64(totalReqs),
		Errors:    errCount,
		Latencies: valid,
	}
}

// ---------------------------------------------------------------------------
// Scenario 2: High Bandwidth — single large download via raw TCP through SOCKS5
// ---------------------------------------------------------------------------

func benchHighBandwidth(t *testing.T, mode benchMode, sizeMB int) *benchResult {
	t.Helper()
	dataSize := sizeMB * 1024 * 1024

	// TCP echo-style server that sends `dataSize` random bytes on connect
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	targetAddr := ln.Addr().String()

	// Pre-generate random data
	data := make([]byte, dataSize)
	rand.Read(data)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, bytes.NewReader(data))
			}(conn)
		}
	}()

	socksAddr := setupBenchProxy(t, mode)
	dialer := newBenchSOCKS5Dialer(t, socksAddr)

	start := time.Now()
	conn, err := dialer.Dial("tcp", targetAddr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	deadline := 120 * time.Second
	if benchExternalBin() != "" {
		deadline = 180 * time.Second
	}
	if tc, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = tc.SetDeadline(time.Now().Add(deadline))
	}
	n, err := io.CopyN(io.Discard, conn, int64(dataSize))
	conn.Close()
	elapsed := time.Since(start)

	var errCount int64
	if err != nil || n != int64(dataSize) {
		errCount = 1
	}

	return &benchResult{
		Scenario:   fmt.Sprintf("HighBandwidth(%dMB)", sizeMB),
		Mode:       mode,
		Duration:   elapsed,
		TotalBytes: n,
		TotalReqs:  1,
		Errors:     errCount,
	}
}

// ---------------------------------------------------------------------------
// Scenario 3: High Concurrency + High Bandwidth — parallel large downloads
// ---------------------------------------------------------------------------

func benchConcurrentBandwidth(t *testing.T, mode benchMode, concurrency int, sizeMB int) *benchResult {
	t.Helper()
	dataSize := sizeMB * 1024 * 1024

	// Data server
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
				_, _ = io.Copy(c, bytes.NewReader(data))
			}(conn)
		}
	}()

	socksAddr := setupBenchProxy(t, mode)
	dialer := newBenchSOCKS5Dialer(t, socksAddr)

	var (
		wg         sync.WaitGroup
		totalBytes int64
		errCount   int64
		latencies  = make([]time.Duration, concurrency)
	)

	start := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reqStart := time.Now()
			conn, err := dialer.Dial("tcp", targetAddr)
			if err != nil {
				atomic.AddInt64(&errCount, 1)
				return
			}
			deadline := 60 * time.Second
			if benchExternalBin() != "" {
				deadline = 120 * time.Second
			}
			if tc, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
				tc.SetDeadline(time.Now().Add(deadline))
			}
			n, err := io.CopyN(io.Discard, conn, int64(dataSize))
			conn.Close()
			atomic.AddInt64(&totalBytes, n)
			if err != nil || n != int64(dataSize) {
				atomic.AddInt64(&errCount, 1)
			}
			latencies[idx] = time.Since(reqStart)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	valid := latencies[:0]
	for _, l := range latencies {
		if l > 0 {
			valid = append(valid, l)
		}
	}

	return &benchResult{
		Scenario:   fmt.Sprintf("ConcurrentBandwidth(c=%d,%dMB)", concurrency, sizeMB),
		Mode:       mode,
		Duration:   elapsed,
		TotalBytes: totalBytes,
		TotalReqs:  int64(concurrency),
		Errors:     errCount,
		Latencies:  valid,
	}
}

// ---------------------------------------------------------------------------
// Test entry points
// ---------------------------------------------------------------------------

// TestBench_TCP runs all three scenarios over normal TCP tunnel.
func TestBench_TCP(t *testing.T) {
	if os.Getenv("REM_BENCH") == "" {
		t.Skip("set REM_BENCH=1 to run performance benchmarks")
	}

	t.Run("HighConcurrency", func(t *testing.T) {
		r := benchHighConcurrency(t, modeTCP, 50, 500)
		r.report(t)
	})
	t.Run("HighBandwidth", func(t *testing.T) {
		r := benchHighBandwidth(t, modeTCP, 100)
		r.report(t)
	})
	t.Run("ConcurrentBandwidth", func(t *testing.T) {
		r := benchConcurrentBandwidth(t, modeTCP, 10, 100)
		r.report(t)
	})
}

// TestBench_Duplex runs all three scenarios over duplex (TCP-up + UDP-down) tunnel.
func TestBench_Duplex(t *testing.T) {
	if os.Getenv("REM_BENCH") == "" {
		t.Skip("set REM_BENCH=1 to run performance benchmarks")
	}

	t.Run("HighConcurrency", func(t *testing.T) {
		r := benchHighConcurrency(t, modeDuplex, 50, 200)
		r.report(t)
	})
	t.Run("HighBandwidth", func(t *testing.T) {
		r := benchHighBandwidth(t, modeDuplex, 32)
		r.report(t)
	})
	t.Run("ConcurrentBandwidth", func(t *testing.T) {
		r := benchConcurrentBandwidth(t, modeDuplex, 3, 2)
		r.report(t)
	})
}

// TestBench_UDP runs all three scenarios over UDP tunnel.
func TestBench_UDP(t *testing.T) {
	if os.Getenv("REM_BENCH") == "" {
		t.Skip("set REM_BENCH=1 to run performance benchmarks")
	}
	if benchExternalBin() != "" {
		t.Skip("TestBench_UDP is skipped in REM_BENCH_BIN mode: minimal tinygo benchmark target is tcp/memory")
	}

	t.Run("HighConcurrency", func(t *testing.T) {
		r := benchHighConcurrency(t, modeUDP, 50, 200)
		r.report(t)
	})
	t.Run("HighBandwidth", func(t *testing.T) {
		r := benchHighBandwidth(t, modeUDP, 32)
		r.report(t)
	})
	t.Run("ConcurrentBandwidth", func(t *testing.T) {
		r := benchConcurrentBandwidth(t, modeUDP, 3, 2)
		r.report(t)
	})
}

// TestBench_Compare runs all modes side-by-side for easy comparison.
func TestBench_Compare(t *testing.T) {
	if os.Getenv("REM_BENCH") == "" {
		t.Skip("set REM_BENCH=1 to run performance benchmarks")
	}

	modes := []benchMode{modeTCP, modeUDP}
	if benchExternalBin() != "" {
		modes = []benchMode{modeTCP}
	}
	var results []*benchResult

	for _, m := range modes {
		t.Run(fmt.Sprintf("%s/HighConcurrency", m), func(t *testing.T) {
			r := benchHighConcurrency(t, m, 50, 200)
			r.report(t)
			results = append(results, r)
		})
	}
	for _, m := range modes {
		t.Run(fmt.Sprintf("%s/HighBandwidth", m), func(t *testing.T) {
			r := benchHighBandwidth(t, m, 32)
			r.report(t)
			results = append(results, r)
		})
	}
	for _, m := range modes {
		t.Run(fmt.Sprintf("%s/ConcurrentBandwidth", m), func(t *testing.T) {
			r := benchConcurrentBandwidth(t, m, 3, 2)
			r.report(t)
			results = append(results, r)
		})
	}

	// Summary
	t.Log("\n========== COMPARISON SUMMARY ==========")
	for _, r := range results {
		secs := r.Duration.Seconds()
		if secs < 0.001 {
			secs = 0.001
		}
		mbps := float64(r.TotalBytes) / secs / 1024 / 1024
		rps := float64(r.TotalReqs) / secs
		t.Logf("  %-45s | %8s | %6.1f req/s | %7.2f MB/s | errs=%d",
			r.Scenario, r.Mode, rps, mbps, r.Errors)
	}
}
