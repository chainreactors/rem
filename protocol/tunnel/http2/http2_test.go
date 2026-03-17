package http2_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/tunnel"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	utils.Log = logs.NewLogger(logs.DebugLevel)
}

func TestHTTP2RoundTrip(t *testing.T) {
	clientConn, serverConn := setupHTTP2TunnelPair(t, "http2", nil)

	go func() {
		defer serverConn.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if string(buf) != "hello" {
			t.Errorf("expected hello, got %q", string(buf))
			return
		}
		if _, err := serverConn.Write([]byte("world")); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	if _, err := clientConn.Write([]byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}

	reply := make([]byte, 5)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(reply) != "world" {
		t.Fatalf("expected world, got %q", string(reply))
	}
}

func TestHTTP2SRoundTrip(t *testing.T) {
	clientConn, serverConn := setupHTTP2TunnelPair(t, "http2s", nil)

	go func() {
		defer serverConn.Close()
		if _, err := serverConn.Write([]byte("secure")); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	reply := make([]byte, 6)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(reply) != "secure" {
		t.Fatalf("expected secure, got %q", string(reply))
	}
}

func TestHTTP2PartialRead(t *testing.T) {
	clientConn, serverConn := setupHTTP2TunnelPair(t, "http2", nil)
	payload := []byte("partial-read-check")

	go func() {
		defer serverConn.Close()
		if _, err := serverConn.Write(payload); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	first := make([]byte, 7)
	n, err := clientConn.Read(first)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	rest, err := io.ReadAll(clientConn)
	if err != nil {
		t.Fatalf("read remainder: %v", err)
	}

	got := append(first[:n], rest...)
	if string(got) != string(payload) {
		t.Fatalf("expected %q, got %q", string(payload), string(got))
	}
}

func TestHTTP2LargePayload(t *testing.T) {
	clientConn, serverConn := setupHTTP2TunnelPair(t, "http2", nil)

	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	go func() {
		defer serverConn.Close()
		if _, err := serverConn.Write(payload); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(clientConn, got); err != nil {
		t.Fatalf("client read: %v", err)
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("mismatch at byte %d: want %d got %d", i, payload[i], got[i])
		}
	}
}

func TestHTTP2WithForwardProxy(t *testing.T) {
	proxyAddr := startSOCKS5Proxy(t)
	clientConn, serverConn := setupHTTP2TunnelPair(
		t,
		"http2",
		[]tunnel.TunnelOption{tunnel.WithProxyClient([]string{"socks5://" + proxyAddr})},
	)

	go func() {
		defer serverConn.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if _, err := serverConn.Write([]byte("pong")); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	if _, err := clientConn.Write([]byte("ping")); err != nil {
		t.Fatalf("client write: %v", err)
	}

	reply := make([]byte, 4)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(reply) != "pong" {
		t.Fatalf("expected pong, got %q", string(reply))
	}
}

func TestHTTP2ConcurrentSessions(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("http2://127.0.0.1:%d/rem?wrapper=raw", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), "http2", true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	lsn, err := serverTun.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	const numConns = 8
	const msgSize = 2048

	go func() {
		for {
			conn, err := lsn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, msgSize)
				for {
					if _, err := io.ReadFull(c, buf); err != nil {
						return
					}
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	clients := make([]net.Conn, 0, numConns)
	for i := 0; i < numConns; i++ {
		clientTun, err := tunnel.NewTunnel(context.Background(), "http2", false)
		if err != nil {
			t.Fatalf("client %d new tunnel: %v", i, err)
		}
		conn, err := clientTun.Dial(addr)
		if err != nil {
			t.Fatalf("client %d dial: %v", i, err)
		}
		clients = append(clients, conn)
		t.Cleanup(func() { _ = conn.Close() })
	}

	var wg sync.WaitGroup
	errs := make(chan error, numConns)
	for i, conn := range clients {
		wg.Add(1)
		go func(id int, c net.Conn) {
			defer wg.Done()
			payload := make([]byte, msgSize)
			for j := range payload {
				payload[j] = byte((id + j) % 256)
			}
			if _, err := c.Write(payload); err != nil {
				errs <- fmt.Errorf("client %d write: %w", id, err)
				return
			}
			reply := make([]byte, msgSize)
			if _, err := io.ReadFull(c, reply); err != nil {
				errs <- fmt.Errorf("client %d read: %w", id, err)
				return
			}
			for j := range payload {
				if reply[j] != payload[j] {
					errs <- fmt.Errorf("client %d mismatch at %d", id, j)
					return
				}
			}
		}(i, conn)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
}

func TestHTTP2RejectsHTTP11(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("http2://127.0.0.1:%d/rem?wrapper=raw", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), "http2", true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	_, err = serverTun.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/rem", port), "application/octet-stream", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("http/1.1 post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusHTTPVersionNotSupported {
		t.Fatalf("expected %d, got %d", http.StatusHTTPVersionNotSupported, resp.StatusCode)
	}
}

func BenchmarkHTTP2Throughput(b *testing.B) {
	schemes := []string{"http2", "http2s"}
	sizes := []int{1024, 8 * 1024, 64 * 1024}

	for _, scheme := range schemes {
		b.Run(scheme, func(b *testing.B) {
			for _, size := range sizes {
				b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
					clientConn, serverConn := setupHTTP2BenchPair(b, scheme)

					payload := make([]byte, size)
					for i := range payload {
						payload[i] = byte(i % 251)
					}
					recvBuf := make([]byte, size)

					done := make(chan struct{})
					go func() {
						defer close(done)
						for {
							if _, err := io.ReadFull(serverConn, recvBuf); err != nil {
								return
							}
						}
					}()

					b.ReportAllocs()
					b.SetBytes(int64(size))
					b.ResetTimer()

					for i := 0; i < b.N; i++ {
						if _, err := clientConn.Write(payload); err != nil {
							b.Fatalf("write: %v", err)
						}
					}

					b.StopTimer()
					_ = serverConn.Close()
					_ = clientConn.Close()
					<-done
				})
			}
		})
	}
}

func BenchmarkHTTP2RoundTrip(b *testing.B) {
	for _, scheme := range []string{"http2", "http2s"} {
		b.Run(scheme, func(b *testing.B) {
			clientConn, serverConn := setupHTTP2BenchPair(b, scheme)

			payload := []byte("ping")
			reply := []byte("pong")

			go func() {
				buf := make([]byte, len(payload))
				for {
					if _, err := io.ReadFull(serverConn, buf); err != nil {
						return
					}
					if _, err := serverConn.Write(reply); err != nil {
						return
					}
				}
			}()

			readBuf := make([]byte, len(reply))
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				if _, err := clientConn.Write(payload); err != nil {
					b.Fatalf("write: %v", err)
				}
				if _, err := io.ReadFull(clientConn, readBuf); err != nil {
					b.Fatalf("read: %v", err)
				}
			}

			b.StopTimer()
			_ = serverConn.Close()
			_ = clientConn.Close()
		})
	}
}

func setupHTTP2TunnelPair(t *testing.T, scheme string, clientOpts []tunnel.TunnelOption) (net.Conn, net.Conn) {
	t.Helper()

	port := freeTCPPort(t)
	addr := fmt.Sprintf("%s://127.0.0.1:%d/rem?wrapper=raw", scheme, port)

	serverTun, err := tunnel.NewTunnel(context.Background(), scheme, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	lsn, err := serverTun.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := lsn.Accept()
		acceptCh <- acceptResult{conn: conn, err: err}
	}()

	clientTun, err := tunnel.NewTunnel(context.Background(), scheme, false, clientOpts...)
	if err != nil {
		t.Fatalf("new client tunnel: %v", err)
	}
	clientConn, err := clientTun.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	select {
	case accepted := <-acceptCh:
		if accepted.err != nil {
			t.Fatalf("accept: %v", accepted.err)
		}
		t.Cleanup(func() { _ = accepted.conn.Close() })
		return clientConn, accepted.conn
	case <-time.After(10 * time.Second):
		t.Fatal("accept timeout")
		return nil, nil
	}
}

func setupHTTP2BenchPair(b *testing.B, scheme string) (net.Conn, net.Conn) {
	b.Helper()

	port := freeBenchTCPPort(b)
	addr := fmt.Sprintf("%s://127.0.0.1:%d/rem?wrapper=raw", scheme, port)

	serverTun, err := tunnel.NewTunnel(context.Background(), scheme, true)
	if err != nil {
		b.Fatalf("new server tunnel: %v", err)
	}
	lsn, err := serverTun.Listen(addr)
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	b.Cleanup(func() { _ = serverTun.Close() })

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := lsn.Accept()
		acceptCh <- acceptResult{conn: conn, err: err}
	}()

	clientTun, err := tunnel.NewTunnel(context.Background(), scheme, false)
	if err != nil {
		b.Fatalf("new client tunnel: %v", err)
	}
	clientConn, err := clientTun.Dial(addr)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	b.Cleanup(func() { _ = clientConn.Close() })

	select {
	case accepted := <-acceptCh:
		if accepted.err != nil {
			b.Fatalf("accept: %v", accepted.err)
		}
		b.Cleanup(func() { _ = accepted.conn.Close() })
		return clientConn, accepted.conn
	case <-time.After(10 * time.Second):
		b.Fatal("accept timeout")
		return nil, nil
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func freeBenchTCPPort(b *testing.B) int {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func startSOCKS5Proxy(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	server, err := socks5.New(&socks5.Config{})
	if err != nil {
		t.Fatalf("new socks5 proxy: %v", err)
	}

	go func() {
		_ = server.Serve(ln)
	}()

	return ln.Addr().String()
}
