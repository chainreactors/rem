//go:build !tinygo

package runner

// integration_serve_test.go - E2E tests for application-layer serve modes.
//
// Tests cover the serve/application layer running over a standard TCP tunnel:
//   - HTTP proxy (http) : TestE2E_Serve_HTTPProxy_*
//   - Port-forward (forward): TestE2E_Serve_PortForward_*
//
// The tunnel transport is always raw TCP so these tests isolate the serve
// layer from transport-layer concerns.  Transport-layer isolation tests live
// in integration_transport_test.go.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"os"
)

// ─────────────────────────────────────────────────────────────────────────────
// HTTP proxy helpers
// ─────────────────────────────────────────────────────────────────────────────

// setupHTTPProxyE2E launches a REM server + proxy-mode client (HTTP proxy serve)
// and returns the HTTP proxy address (e.g. "127.0.0.1:8080").
func setupHTTPProxyE2E(t *testing.T) string {
	t.Helper()
	serverPort := freePort(t)
	httpProxyPort := freePort(t)
	alias := fmt.Sprintf("httpe2e%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start HTTP-serve server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", serverPort), 10*time.Second)

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://127.0.0.1:%d/?wrapper=raw -m proxy -l http://127.0.0.1:%d -a %s --debug",
			serverPort, httpProxyPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start HTTP-serve client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	httpProxyAddr := fmt.Sprintf("127.0.0.1:%d", httpProxyPort)
	waitForTCP(t, httpProxyAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	return httpProxyAddr
}

// newHTTPProxyClient creates an *http.Client that routes traffic through the
// REM HTTP proxy at proxyAddr.
func newHTTPProxyClient(t *testing.T, proxyAddr string, timeout time.Duration) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse("http://remno1:0onmer@" + proxyAddr)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   timeout,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Port-forward helpers
// ─────────────────────────────────────────────────────────────────────────────

// startEchoServer starts a TCP echo server and returns its address and a
// cleanup function.  Suitable for direct-TCP connectivity verification.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
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
	return ln.Addr().String()
}

func startLocalhostEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
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
	return ln.Addr().String()
}

func asLocalhostAddr(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	return net.JoinHostPort("localhost", port)
}

// setupPortForwardE2E launches a REM server + proxy-mode client configured
// for port-forwarding to echoAddr.  Returns the local forward port address
// that clients can connect to directly over TCP.
func setupPortForwardE2E(t *testing.T, echoAddr string) string {
	t.Helper()
	serverPort := freePort(t)
	pfPort := freePort(t)
	alias := fmt.Sprintf("pfe2e%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start port-forward server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", serverPort), 10*time.Second)

	// -l forward://0.0.0.0:pfPort  — local port the client listens on
	// -r echoAddr                  — "dest" for the ForwardPlugin on both sides
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://127.0.0.1:%d/?wrapper=raw -m proxy -l forward://0.0.0.0:%d -r %s -a %s --debug",
			serverPort, pfPort, echoAddr, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start port-forward client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	pfAddr := fmt.Sprintf("127.0.0.1:%d", pfPort)
	waitForTCP(t, pfAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	return pfAddr
}

// echoRoundtrip dials addr over TCP, sends msg, reads the echoed reply, and
// fails the test if the reply does not match.
func echoRoundtrip(t *testing.T, addr string, msg []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: expected %q, got %q", msg, buf)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP proxy tests
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_Serve_HTTPProxy_GET verifies that HTTPS tunnelling via HTTP CONNECT
// works end-to-end through the REM tunnel. The REM HTTP proxy Relay only
// supports CONNECT (not plain HTTP GET), so all tests use HTTPS.
func TestE2E_Serve_HTTPProxy_GET(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello http-proxy"))
	}))
	defer ts.Close()

	httpProxyAddr := setupHTTPProxyE2E(t)
	proxyURL, _ := url.Parse("http://remno1:0onmer@" + httpProxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: ts.Client().Transport.(*http.Transport).TLSClientConfig,
		},
		Timeout: 15 * time.Second,
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("HTTPS through HTTP proxy: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello http-proxy" {
		t.Fatalf("expected %q, got %q", "hello http-proxy", string(body))
	}
}

// TestE2E_Serve_HTTPProxy_CONNECT verifies that HTTPS tunnelling via
// HTTP CONNECT works end-to-end through the REM tunnel.
func TestE2E_Serve_HTTPProxy_CONNECT(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello https-connect"))
	}))
	defer ts.Close()

	httpProxyAddr := setupHTTPProxyE2E(t)
	proxyURL, _ := url.Parse("http://remno1:0onmer@" + httpProxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: ts.Client().Transport.(*http.Transport).TLSClientConfig,
		},
		Timeout: 15 * time.Second,
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("HTTPS CONNECT through HTTP proxy: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello https-connect" {
		t.Fatalf("expected %q, got %q", "hello https-connect", string(body))
	}
}

// TestE2E_Serve_HTTPProxy_ConcurrentRequests fires 8 concurrent HTTPS requests
// through the HTTP proxy to verify stream isolation via CONNECT.
func TestE2E_Serve_HTTPProxy_ConcurrentRequests(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("http-proxy-concurrent"))
	}))
	defer ts.Close()

	httpProxyAddr := setupHTTPProxyE2E(t)

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			proxyURL, _ := url.Parse("http://remno1:0onmer@" + httpProxyAddr)
			client := &http.Client{
				Transport: &http.Transport{
					Proxy:           http.ProxyURL(proxyURL),
					TLSClientConfig: ts.Client().Transport.(*http.Transport).TLSClientConfig,
				},
				Timeout: 15 * time.Second,
			}
			resp, err := client.Get(ts.URL)
			if err != nil {
				errs <- fmt.Errorf("req %d: %w", idx, err)
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- fmt.Errorf("req %d read: %w", idx, err)
				return
			}
			if string(body) != "http-proxy-concurrent" {
				errs <- fmt.Errorf("req %d: expected %q, got %q", idx, "http-proxy-concurrent", string(body))
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Port-forward tests
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_Serve_PortForward_Echo verifies that a direct TCP connection to the
// local port-forward listener is relayed through the tunnel and forwarded to
// the echo server.
func TestE2E_Serve_PortForward_Echo(t *testing.T) {
	echoAddr := startEchoServer(t)
	pfAddr := setupPortForwardE2E(t, echoAddr)

	echoRoundtrip(t, pfAddr, []byte("hello port-forward"))
}

// TestE2E_Serve_PortForward_LargePayload sends a 128 KB payload through the
// port-forward tunnel and verifies it is echoed back correctly.
func TestE2E_Serve_PortForward_LargePayload(t *testing.T) {
	echoAddr := startEchoServer(t)
	pfAddr := setupPortForwardE2E(t, echoAddr)

	payload := make([]byte, 128*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	echoRoundtrip(t, pfAddr, payload)
}

// TestE2E_Serve_PortForward_ConcurrentConns opens 5 simultaneous TCP
// connections through the port-forward tunnel to verify that each stream
// is correctly demultiplexed by Yamux.
func TestE2E_Serve_PortForward_ConcurrentConns(t *testing.T) {
	echoAddr := startEchoServer(t)
	pfAddr := setupPortForwardE2E(t, echoAddr)

	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := []byte(fmt.Sprintf("conn-%d-hello", idx))
			conn, err := net.DialTimeout("tcp", pfAddr, 5*time.Second)
			if err != nil {
				errs <- fmt.Errorf("conn %d dial: %w", idx, err)
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := conn.Write(msg); err != nil {
				errs <- fmt.Errorf("conn %d write: %w", idx, err)
				return
			}
			buf := make([]byte, len(msg))
			if _, err := io.ReadFull(conn, buf); err != nil {
				errs <- fmt.Errorf("conn %d read: %w", idx, err)
				return
			}
			if string(buf) != string(msg) {
				errs <- fmt.Errorf("conn %d: expected %q, got %q", idx, msg, buf)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestE2E_Serve_PortForward_DomainDest(t *testing.T) {
	echoAddr := startLocalhostEchoServer(t)
	pfAddr := setupPortForwardE2E(t, asLocalhostAddr(t, echoAddr))

	echoRoundtrip(t, pfAddr, []byte("hello port-forward-domain"))
}

// ─────────────────────────────────────────────────────────────────────────────
// Raw serve tests
// ─────────────────────────────────────────────────────────────────────────────

// setupRawServeE2E launches a REM server + proxy-mode client configured for
// raw TCP forwarding to echoAddr. Returns the local raw port address that
// clients can connect to directly over TCP.
func setupRawServeE2E(t *testing.T, echoAddr string) string {
	t.Helper()
	serverPort := freePort(t)
	rawPort := freePort(t)
	alias := fmt.Sprintf("rawe2e%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start raw-serve server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", serverPort), 10*time.Second)

	// -l raw://0.0.0.0:rawPort  — local port the client listens on
	// -r echoAddr               — destination for raw forwarding
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://127.0.0.1:%d/?wrapper=raw -m proxy -l raw://0.0.0.0:%d -r %s -a %s --debug",
			serverPort, rawPort, echoAddr, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start raw-serve client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	rawAddr := fmt.Sprintf("127.0.0.1:%d", rawPort)
	waitForTCP(t, rawAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	return rawAddr
}

// TestE2E_Serve_Raw_DirectTCP verifies that a direct TCP connection to the
// raw serve listener is relayed through the tunnel and forwarded to the
// echo server.
func TestE2E_Serve_Raw_DirectTCP(t *testing.T) {
	echoAddr := startEchoServer(t)
	rawAddr := setupRawServeE2E(t, echoAddr)

	echoRoundtrip(t, rawAddr, []byte("hello raw-serve"))
}

// TestE2E_Serve_Raw_LargePayload sends a 256 KB payload through the raw
// serve tunnel and verifies it is echoed back correctly.
func TestE2E_Serve_Raw_LargePayload(t *testing.T) {
	echoAddr := startEchoServer(t)
	rawAddr := setupRawServeE2E(t, echoAddr)

	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	echoRoundtrip(t, rawAddr, payload)
}

// TestE2E_Serve_Raw_ConcurrentConns opens 5 simultaneous TCP connections
// through the raw serve tunnel to verify correct stream demultiplexing.
func TestE2E_Serve_Raw_ConcurrentConns(t *testing.T) {
	echoAddr := startEchoServer(t)
	rawAddr := setupRawServeE2E(t, echoAddr)

	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := []byte(fmt.Sprintf("raw-conn-%d", idx))
			conn, err := net.DialTimeout("tcp", rawAddr, 5*time.Second)
			if err != nil {
				errs <- fmt.Errorf("conn %d dial: %w", idx, err)
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := conn.Write(msg); err != nil {
				errs <- fmt.Errorf("conn %d write: %w", idx, err)
				return
			}
			buf := make([]byte, len(msg))
			if _, err := io.ReadFull(conn, buf); err != nil {
				errs <- fmt.Errorf("conn %d read: %w", idx, err)
				return
			}
			if string(buf) != string(msg) {
				errs <- fmt.Errorf("conn %d: expected %q, got %q", idx, msg, buf)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestE2E_Serve_Raw_DomainDest(t *testing.T) {
	echoAddr := startLocalhostEchoServer(t)
	rawAddr := setupRawServeE2E(t, asLocalhostAddr(t, echoAddr))

	echoRoundtrip(t, rawAddr, []byte("hello raw-domain"))
}
