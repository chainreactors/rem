package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	utils.Log = logs.NewLogger(logs.WarnLevel)
}

func TestMemoryBridge_BasicReadWrite(t *testing.T) {
	bridge := NewMemoryBridge()
	ch := bridge.CreateChannel("test-rw")

	errCh := make(chan error, 2)

	// Server side: send conn via channel, write "pong", read "ping"
	go func() {
		client, server := net.Pipe()
		ch <- server

		buf := make([]byte, 4)
		if _, err := io.ReadFull(client, buf); err != nil {
			errCh <- err
			return
		}
		if string(buf) != "ping" {
			errCh <- &net.OpError{Op: "read", Err: io.ErrUnexpectedEOF}
			return
		}
		if _, err := client.Write([]byte("pong")); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	// Client side: receive conn from channel, write "ping", read "pong"
	conn := <-ch
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("expected pong, got %q", buf)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("server goroutine error: %v", err)
	}
}

func TestMemoryBridge_LargeData(t *testing.T) {
	bridge := NewMemoryBridge()
	ch := bridge.CreateChannel("test-large")

	const dataSize = 1 << 20 // 1MB
	data := make([]byte, dataSize)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	expectedHash := sha256.Sum256(data)

	errCh := make(chan error, 1)

	go func() {
		client, server := net.Pipe()
		ch <- server
		// Write all data through client side of pipe
		_, err := client.Write(data)
		client.Close()
		errCh <- err
	}()

	conn := <-ch
	received, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	conn.Close()

	if len(received) != dataSize {
		t.Fatalf("expected %d bytes, got %d", dataSize, len(received))
	}
	actualHash := sha256.Sum256(received)
	if actualHash != expectedHash {
		t.Fatal("SHA256 mismatch")
	}

	if err := <-errCh; err != nil {
		t.Fatalf("writer error: %v", err)
	}
}

func TestMemoryDialer_SOCKS5(t *testing.T) {
	// 1. Start a TCP echo server
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

	// 2. Create memory bridge channel
	pipeID := "test-socks5"
	bridge := globalBridge
	connChan := bridge.CreateChannel(pipeID)

	// 3. Start SOCKS5 server on the listener side of the memory pipe
	conf := &socks5.Config{
		Logger: log.New(io.Discard, "", 0),
	}
	socksServer, err := socks5.New(conf)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for {
			conn, ok := <-connChan
			if !ok {
				return
			}
			go socksServer.ServeConn(conn)
		}
	}()

	// 4. Dial through memory pipe + SOCKS5 to echo server
	client, server := net.Pipe()
	connChan <- server

	if err := socks5.ClientConnect(client, echoAddr, "", ""); err != nil {
		t.Fatalf("SOCKS5 handshake: %v", err)
	}

	// 5. Send data and verify echo
	msg := []byte("hello through memory+socks5")
	if _, err := client.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	client.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("expected %q, got %q", msg, buf)
	}
	client.Close()
}

func TestMemoryBridge_ConcurrentPipes(t *testing.T) {
	bridge := NewMemoryBridge()
	const numPipes = 10

	var wg sync.WaitGroup
	errors := make(chan error, numPipes*2)

	for i := 0; i < numPipes; i++ {
		id := string(rune('A'+i)) + "-concurrent"
		payload := make([]byte, 1024)
		if _, err := rand.Read(payload); err != nil {
			t.Fatal(err)
		}
		expectedHash := sha256.Sum256(payload)

		ch := bridge.CreateChannel(id)
		wg.Add(2)

		// Writer: create pipe, send server end, write data through client end
		go func() {
			defer wg.Done()
			client, server := net.Pipe()
			ch <- server
			if _, err := client.Write(payload); err != nil {
				errors <- err
			}
			client.Close()
		}()

		// Reader: receive conn, read data, verify hash
		go func() {
			defer wg.Done()
			conn := <-ch
			defer conn.Close()
			received, err := io.ReadAll(conn)
			if err != nil {
				errors <- err
				return
			}
			actualHash := sha256.Sum256(received)
			if actualHash != expectedHash {
				errors <- &net.OpError{Op: "verify", Err: io.ErrUnexpectedEOF}
			}
		}()
	}

	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("concurrent pipe error: %v", err)
	}
}

// TestMemoryBridge_ProxyClientLargeData verifies large data transfer through
// the proxyclient → memory → SOCKS5 → net.Dial path.
func TestMemoryBridge_ProxyClientLargeData(t *testing.T) {
	const dataSize = 256 << 10 // 256KB

	// 1. Start TCP server that sends back a known payload
	payload := make([]byte, dataSize)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	expectedHash := sha256.Sum256(payload)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srvAddr := ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Read request marker, then send payload
				marker := make([]byte, 3)
				io.ReadFull(c, marker)
				c.Write(payload)
			}(conn)
		}
	}()

	// 2. Set up memory bridge + SOCKS5
	pipeID := "test-proxyclient-large"
	connChan := globalBridge.CreateChannel(pipeID)

	conf := &socks5.Config{
		Logger:   log.New(io.Discard, "", 0),
		Resolver: socks5.PassthroughResolver{},
	}
	socksServer, err := socks5.New(conf)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for {
			conn, ok := <-connChan
			if !ok {
				return
			}
			go socksServer.ServeConn(conn)
		}
	}()

	// 3. Dial through proxyclient
	memURL := &url.URL{Scheme: "memory", Host: pipeID}
	memClient, err := proxyclient.NewClient(memURL)
	if err != nil {
		t.Fatalf("proxyclient.NewClient: %v", err)
	}

	conn, err := memClient(context.Background(), "tcp", srvAddr)
	if err != nil {
		t.Fatalf("memory dial: %v", err)
	}
	defer conn.Close()

	// 4. Request and receive large payload
	conn.Write([]byte("GET"))
	received, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(received) != dataSize {
		t.Fatalf("expected %d bytes, got %d", dataSize, len(received))
	}
	actualHash := sha256.Sum256(received)
	if actualHash != expectedHash {
		t.Fatal("SHA256 mismatch on large transfer through memory bridge")
	}
}

func benchmarkMemoryPipeRaw(b *testing.B, size int) {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		b.Fatal(err)
	}
	recv := make([]byte, size)
	const chunkSize = 16 << 10

	client, server := net.Pipe()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 64<<10)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				if _, werr := server.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for off := 0; off < size; {
			end := off + chunkSize
			if end > size {
				end = size
			}
			if _, err := client.Write(payload[off:end]); err != nil {
				b.Fatal(err)
			}
			if _, err := io.ReadFull(client, recv[off:end]); err != nil {
				b.Fatal(err)
			}
			off = end
		}
	}
	b.StopTimer()
	_ = client.Close()
	_ = server.Close()
	wg.Wait()
}

func benchmarkMemoryPipeProxyClient(b *testing.B, size int) {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		b.Fatal(err)
	}
	recv := make([]byte, size)
	const chunkSize = 16 << 10

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	srvAddr := ln.Addr().String()

	var echoWG sync.WaitGroup
	echoWG.Add(1)
	go func() {
		defer echoWG.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	pipeID := fmt.Sprintf("bench-proxyclient-%d", time.Now().UnixNano())
	connChan := globalBridge.CreateChannel(pipeID)

	conf := &socks5.Config{
		Logger:   log.New(io.Discard, "", 0),
		Resolver: socks5.PassthroughResolver{},
	}
	socksServer, err := socks5.New(conf)
	if err != nil {
		b.Fatal(err)
	}

	var proxyWG sync.WaitGroup
	proxyWG.Add(1)
	go func() {
		defer proxyWG.Done()
		for {
			conn, ok := <-connChan
			if !ok {
				return
			}
			go socksServer.ServeConn(conn)
		}
	}()

	memURL := &url.URL{Scheme: "memory", Host: pipeID}
	memClient, err := proxyclient.NewClient(memURL)
	if err != nil {
		b.Fatalf("proxyclient.NewClient: %v", err)
	}

	conn, err := memClient(context.Background(), "tcp", srvAddr)
	if err != nil {
		b.Fatalf("memory dial: %v", err)
	}

	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for off := 0; off < size; {
			end := off + chunkSize
			if end > size {
				end = size
			}
			if _, err := conn.Write(payload[off:end]); err != nil {
				b.Fatal(err)
			}
			if _, err := io.ReadFull(conn, recv[off:end]); err != nil {
				b.Fatal(err)
			}
			off = end
		}
	}
	b.StopTimer()

	_ = conn.Close()
	close(connChan)
	_ = ln.Close()
	proxyWG.Wait()
	echoWG.Wait()
}

func benchmarkMemoryPipeProxyClientNoTCP(b *testing.B, size int) {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		b.Fatal(err)
	}
	recv := make([]byte, size)
	const chunkSize = 16 << 10

	pipeID := fmt.Sprintf("bench-proxyclient-notcp-%d", time.Now().UnixNano())
	connChan := globalBridge.CreateChannel(pipeID)

	conf := &socks5.Config{
		Logger:   log.New(io.Discard, "", 0),
		Resolver: socks5.PassthroughResolver{},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				buf := make([]byte, 64<<10)
				for {
					n, err := server.Read(buf)
					if n > 0 {
						if _, werr := server.Write(buf[:n]); werr != nil {
							_ = server.Close()
							return
						}
					}
					if err != nil {
						_ = server.Close()
						return
					}
				}
			}()
			return client, nil
		},
	}
	socksServer, err := socks5.New(conf)
	if err != nil {
		b.Fatal(err)
	}

	var proxyWG sync.WaitGroup
	proxyWG.Add(1)
	go func() {
		defer proxyWG.Done()
		for {
			conn, ok := <-connChan
			if !ok {
				return
			}
			go socksServer.ServeConn(conn)
		}
	}()

	memURL := &url.URL{Scheme: "memory", Host: pipeID}
	memClient, err := proxyclient.NewClient(memURL)
	if err != nil {
		b.Fatalf("proxyclient.NewClient: %v", err)
	}

	conn, err := memClient(context.Background(), "tcp", "example.com:80")
	if err != nil {
		b.Fatalf("memory dial: %v", err)
	}

	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for off := 0; off < size; {
			end := off + chunkSize
			if end > size {
				end = size
			}
			if _, err := conn.Write(payload[off:end]); err != nil {
				b.Fatal(err)
			}
			if _, err := io.ReadFull(conn, recv[off:end]); err != nil {
				b.Fatal(err)
			}
			off = end
		}
	}
	b.StopTimer()

	_ = conn.Close()
	close(connChan)
	proxyWG.Wait()
}

func BenchmarkMemoryPipeRaw_1MiB(b *testing.B) {
	benchmarkMemoryPipeRaw(b, 1<<20)
}

func BenchmarkMemoryPipeProxyClient_1MiB(b *testing.B) {
	benchmarkMemoryPipeProxyClient(b, 1<<20)
}

func BenchmarkMemoryPipeProxyClientNoTCP_1MiB(b *testing.B) {
	benchmarkMemoryPipeProxyClientNoTCP(b, 1<<20)
}

func BenchmarkMemoryPipeRaw_32KiB(b *testing.B) {
	benchmarkMemoryPipeRaw(b, 32<<10)
}

func BenchmarkMemoryPipeProxyClient_32KiB(b *testing.B) {
	benchmarkMemoryPipeProxyClient(b, 32<<10)
}

func BenchmarkMemoryPipeProxyClientNoTCP_32KiB(b *testing.B) {
	benchmarkMemoryPipeProxyClientNoTCP(b, 32<<10)
}
