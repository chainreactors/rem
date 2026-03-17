//go:build !tinygo

package runner

// integration_transport_test.go - E2E tests for individual transport layers.
//
// Each test brings up a real REM server and client as separate OS processes
// (via TestHelperProcess), connects a SOCKS5 client through the tunnel, and
// verifies end-to-end data delivery against a local httptest.Server.
//
// Transport coverage:
//   - UDP/KCP   : TestE2E_UDP_*
//   - WebSocket : TestE2E_WebSocket_*
//   - StreamHTTP: TestE2E_StreamHTTP*
//   - TCP+TLS   : TestE2E_TLS_*
//   - TLSInTLS  : TestE2E_TLSInTLS_*
//   - ICMP/KCP  : TestE2E_ICMP_* (requires REM_TEST_ICMP=1 and root/admin)

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xsocks5 "github.com/chainreactors/rem/x/socks5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Setup helpers
// ─────────────────────────────────────────────────────────────────────────────

// setupUDPE2E launches a REM server (UDP/KCP transport) and a proxy-mode client
// as separate subprocesses. Because KCP runs over UDP there is no TCP socket to
// probe for readiness, so the helper sleeps briefly after starting the server.
func setupUDPE2E(t *testing.T) string {
	t.Helper()
	udpPort := freeUDPPort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("udpe2e%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s udp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", udpPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start UDP server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	// UDP/KCP cannot be probed via TCP; give the listener time to bind.
	time.Sleep(2 * time.Second)

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c udp://127.0.0.1:%d/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			udpPort, socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start UDP client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	return socksAddr
}

// setupWSE2E launches a REM server (WebSocket transport) and a proxy-mode client
// as separate subprocesses. WebSocket uses TCP underneath so standard TCP
// readiness probing works for the server port.
func setupWSE2E(t *testing.T) string {
	t.Helper()
	wsPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("wse2e%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s ws://0.0.0.0:%d/rem/?wrapper=raw -i 127.0.0.1 --no-sub", wsPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start WS server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", wsPort), 10*time.Second)

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c ws://127.0.0.1:%d/rem/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			wsPort, socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start WS client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	return socksAddr
}

// setupTCPE2E launches a REM server (plain TCP transport) and a proxy-mode
// client as separate subprocesses. Used by stress tests that need a plain TCP
// tunnel without TLS or additional wrappers.
func setupTCPE2E(t *testing.T) string {
	t.Helper()
	tcpPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("tcpe2e%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", tcpPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start TCP server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", tcpPort), 10*time.Second)

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://127.0.0.1:%d/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			tcpPort, socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start TCP client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	return socksAddr
}

// setupTCPTLSE2E launches a REM server and proxy-mode client over TCP with the
// provided TLS-related query parameters. The built-in TLS implementation uses a
// self-signed certificate with InsecureSkipVerify, so no external CA is
// required.
func setupTCPTLSE2E(t *testing.T, transportQuery, aliasPrefix string) string {
	t.Helper()
	tlsPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("%s%d", aliasPrefix, atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?%s -i 127.0.0.1 --no-sub", tlsPort, transportQuery),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start TLS server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", tlsPort), 10*time.Second)

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://127.0.0.1:%d/?%s -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			tlsPort, transportQuery, socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start TLS client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	return socksAddr
}

// setupTLSE2E launches a REM server (TCP transport with TLS enabled via the
// tls= query parameter) and a proxy-mode client as separate subprocesses.
func setupTLSE2E(t *testing.T) string {
	t.Helper()
	return setupTCPTLSE2E(t, "wrapper=raw&tls=1", "tlse2e")
}

// setupTLSInTLSE2E launches a REM server and proxy-mode client over TCP with
// both the outer TLS wrapper and the inner TLSInTLS wrapper enabled.
func setupTLSInTLSE2E(t *testing.T) string {
	t.Helper()
	return setupTCPTLSE2E(t, "wrapper=raw&tls=1&tlsintls=1", "tlsintls")
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared verification helpers
// ─────────────────────────────────────────────────────────────────────────────

// verifyHTTP performs a GET through socksAddr and checks the expected body.
func verifyHTTP(t *testing.T, socksAddr, targetURL, want string) {
	t.Helper()
	httpClient := newSOCKS5Client(t, socksAddr, 15*time.Second)
	resp, err := httpClient.Get(targetURL)
	if err != nil {
		t.Fatalf("GET %s through %s: %v", targetURL, socksAddr, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != want {
		t.Fatalf("expected %q, got %q", want, string(body))
	}
}

// verifyLargeTransfer sends a random payload of dataSize bytes through the tunnel
// and checks SHA-256 integrity at the receiver.
func verifyLargeTransfer(t *testing.T, socksAddr string, dataSize int) {
	t.Helper()
	data := make([]byte, dataSize)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", dataSize))
		w.Write(data)
	}))
	defer ts.Close()

	httpClient := newSOCKS5Client(t, socksAddr, 30*time.Second)
	resp, err := httpClient.Get(ts.URL)
	if err != nil {
		t.Fatalf("large transfer GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != dataSize {
		t.Fatalf("size mismatch: expected %d, got %d", dataSize, len(body))
	}
	if sha256.Sum256(body) != want {
		t.Fatal("SHA-256 mismatch – data corrupted in transit")
	}
}

// verifyConcurrent fires n concurrent GET requests through socksAddr and
// checks each response body equals want.
func verifyConcurrent(t *testing.T, socksAddr, targetURL, want string, n int) {
	t.Helper()
	httpClient := newSOCKS5Client(t, socksAddr, 15*time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := httpClient.Get(targetURL)
			if err != nil {
				errs <- fmt.Errorf("request %d: %w", idx, err)
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- fmt.Errorf("request %d read: %w", idx, err)
				return
			}
			if string(body) != want {
				errs <- fmt.Errorf("request %d: expected %q, got %q", idx, want, string(body))
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// waitForSOCKSHTTPReady performs a small HTTP GET through the SOCKS5 listener
// until the full proxy path is actually serving traffic, not just listening.
func waitForSOCKSHTTPReady(t *testing.T, socksAddr string, timeout time.Duration) {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	deadline := time.Now().Add(timeout)
	for {
		client := newSOCKS5Client(t, socksAddr, 5*time.Second)
		resp, err := client.Get(ts.URL)
		client.CloseIdleConnections()
		if err == nil && resp != nil && resp.StatusCode < 400 {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("SOCKS5 HTTP warmup through %s did not become ready within %v: %v", socksAddr, timeout, err)
			}
			t.Fatalf("SOCKS5 HTTP warmup through %s did not become ready within %v", socksAddr, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UDP / KCP
// ─────────────────────────────────────────────────────────────────────────────

func TestE2E_UDP_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello udp"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupUDPE2E(t), ts.URL, "hello udp")
}

// TestE2E_UDP_LargeTransfer sends 512 KB of random data through the KCP tunnel
// and verifies SHA-256 integrity. KCP's selective retransmission and ARQ layer
// must reassemble all fragments without loss.
func TestE2E_UDP_LargeTransfer(t *testing.T) {
	verifyLargeTransfer(t, setupUDPE2E(t), 512*1024)
}

// TestE2E_UDP_ConcurrentRequests exercises Yamux multiplexing on the KCP layer
// by issuing 5 parallel requests and verifying all responses.
func TestE2E_UDP_ConcurrentRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("udp-concurrent-ok"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupUDPE2E(t), ts.URL, "udp-concurrent-ok", 5)
}

// ─────────────────────────────────────────────────────────────────────────────
// WebSocket
// ─────────────────────────────────────────────────────────────────────────────

func TestE2E_WebSocket_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello websocket"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupWSE2E(t), ts.URL, "hello websocket")
}

// TestE2E_WebSocket_LargeTransfer sends 1 MB of random data over the WebSocket
// tunnel and verifies SHA-256 integrity.
func TestE2E_WebSocket_LargeTransfer(t *testing.T) {
	verifyLargeTransfer(t, setupWSE2E(t), 1<<20)
}

// TestE2E_WebSocket_ConcurrentRequests fires 8 parallel requests through the
// WebSocket tunnel to confirm correct Yamux stream demultiplexing.
func TestE2E_WebSocket_ConcurrentRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ws-concurrent-ok"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupWSE2E(t), ts.URL, "ws-concurrent-ok", 8)
}

// ─────────────────────────────────────────────────────────────────────────────
// TCP + TLS
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_TLS_SOCKS5Proxy verifies basic connectivity over a TCP tunnel wrapped
// with TLS (self-signed, InsecureSkipVerify).
func TestE2E_TLS_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello tls"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupTLSE2E(t), ts.URL, "hello tls")
}

// TestE2E_TLS_LargeTransfer sends 1 MB of random data through TLS and verifies
// SHA-256 integrity.
func TestE2E_TLS_LargeTransfer(t *testing.T) {
	verifyLargeTransfer(t, setupTLSE2E(t), 1<<20)
}

// TestE2E_TLS_ConcurrentRequests fires 8 parallel requests over the TLS tunnel.
func TestE2E_TLS_ConcurrentRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("tls-concurrent-ok"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupTLSE2E(t), ts.URL, "tls-concurrent-ok", 8)
}

// ─────────────────────────────────────────────────────────────────────────────
// TCP + TLSInTLS
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_TLSInTLS_SOCKS5Proxy verifies basic connectivity over a TCP tunnel
// wrapped with both TLS and the inner TLSInTLS layer.
func TestE2E_TLSInTLS_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello tlsintls"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupTLSInTLSE2E(t), ts.URL, "hello tlsintls")
}

// TestE2E_TLSInTLS_LargeTransfer sends 1 MB of random data through the double
// TLS tunnel and verifies SHA-256 integrity.
func TestE2E_TLSInTLS_LargeTransfer(t *testing.T) {
	verifyLargeTransfer(t, setupTLSInTLSE2E(t), 1<<20)
}

// TestE2E_TLSInTLS_ConcurrentRequests fires 8 parallel requests over the
// TLSInTLS tunnel.
func TestE2E_TLSInTLS_ConcurrentRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("tlsintls-concurrent-ok"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupTLSInTLSE2E(t), ts.URL, "tlsintls-concurrent-ok", 8)
}

// ─────────────────────────────────────────────────────────────────────────────
// ICMP / KCP
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_ICMP_SOCKS5Proxy verifies that REM can carry a full SOCKS5 proxy
// session over ICMP echo packets (using KCP for reliable delivery).
//
// Preconditions:
//   - Set REM_TEST_ICMP=1 in the environment.
//   - The process must have raw socket privileges (root on Linux,
//     Administrator on Windows, or CAP_NET_RAW capability).
//
// CI usage (Linux):
//
//	sudo -E REM_TEST_ICMP=1 go test -v -run '^TestE2E_ICMP' ./runner/
func TestE2E_ICMP_SOCKS5Proxy(t *testing.T) {
	if os.Getenv("REM_TEST_ICMP") == "" {
		t.Skip("set REM_TEST_ICMP=1 to run ICMP tests (requires root/admin and raw socket privileges)")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello icmp"))
	}))
	defer ts.Close()

	socksPort := freePort(t)
	alias := fmt.Sprintf("icmpe2e%d", atomic.AddUint32(&testCounter, 1))

	// ICMP server: binds to the loopback interface, no port (ICMP has none).
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		"REM_HELPER_CMD=--debug -s icmp://127.0.0.1/?wrapper=raw -i 127.0.0.1 --no-sub",
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start ICMP server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	// ICMP cannot be probed with TCP; give the raw socket time to open.
	time.Sleep(2 * time.Second)

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c icmp://127.0.0.1/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start ICMP client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)

	verifyHTTP(t, socksAddr, ts.URL, "hello icmp")
}

// TestE2E_ICMP_LargeTransfer sends 256 KB of random data through the ICMP
// tunnel and verifies SHA-256 integrity.
//
// Requires REM_TEST_ICMP=1 and root/admin privileges (same as
// TestE2E_ICMP_SOCKS5Proxy).
func TestE2E_ICMP_LargeTransfer(t *testing.T) {
	if os.Getenv("REM_TEST_ICMP") == "" {
		t.Skip("set REM_TEST_ICMP=1 to run ICMP tests (requires root/admin and raw socket privileges)")
	}

	socksPort := freePort(t)
	alias := fmt.Sprintf("icmplg%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		"REM_HELPER_CMD=--debug -s icmp://127.0.0.1/?wrapper=raw -i 127.0.0.1 --no-sub",
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start ICMP server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })
	time.Sleep(2 * time.Second)

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c icmp://127.0.0.1/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start ICMP client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)

	verifyLargeTransfer(t, socksAddr, 256*1024)
}

// ─────────────────────────────────────────────────────────────────────────────
// StreamHTTP transport
// ─────────────────────────────────────────────────────────────────────────────

func setupStreamHTTPTransportE2E(t *testing.T, scheme, query, forwardURL string) string {
	t.Helper()

	streamPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("%se2e%d", scheme, atomic.AddUint32(&testCounter, 1))
	serverURL := fmt.Sprintf("%s://0.0.0.0:%d/rem?%s", scheme, streamPort, query)
	clientURL := fmt.Sprintf("%s://127.0.0.1:%d/rem?%s", scheme, streamPort, query)

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s %s -i 127.0.0.1 --no-sub", serverURL),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start %s server subprocess: %v", scheme, err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", streamPort), 10*time.Second)

	forwardArg := ""
	if forwardURL != "" {
		forwardArg = " -f " + forwardURL
	}

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c %s -m proxy -l socks5://127.0.0.1:%d -a %s --debug%s",
			clientURL, socksPort, alias, forwardArg),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start %s client subprocess: %v", scheme, err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 20*time.Second)
	waitForSOCKSHTTPReady(t, socksAddr, 15*time.Second)
	return socksAddr
}

func startForwardSOCKS5Proxy(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start forward proxy listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	server, err := xsocks5.New(&xsocks5.Config{})
	if err != nil {
		t.Fatalf("new forward proxy: %v", err)
	}

	go func() {
		_ = server.Serve(ln)
	}()

	return "socks5://" + ln.Addr().String()
}

func TestE2E_StreamHTTPTransport_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello streamhttp"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupStreamHTTPTransportE2E(t, "streamhttp", "wrapper=raw", ""), ts.URL, "hello streamhttp")
}

func TestE2E_StreamHTTPTransport_LargeTransfer(t *testing.T) {
	verifyLargeTransfer(t, setupStreamHTTPTransportE2E(t, "streamhttp", "wrapper=raw", ""), 512*1024)
}

func TestE2E_StreamHTTPTransport_ConcurrentRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("streamhttp-concurrent"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupStreamHTTPTransportE2E(t, "streamhttp", "wrapper=raw", ""), ts.URL, "streamhttp-concurrent", 5)
}

func TestE2E_StreamHTTPTransport_ForwardProxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("streamhttp-forward"))
	}))
	defer ts.Close()

	verifyHTTP(
		t,
		setupStreamHTTPTransportE2E(t, "streamhttp", "wrapper=raw", startForwardSOCKS5Proxy(t)),
		ts.URL,
		"streamhttp-forward",
	)
}

func TestE2E_StreamHTTPTransport_TLSInTLS(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("streamhttp-tlsintls"))
	}))
	defer ts.Close()

	verifyHTTP(
		t,
		setupStreamHTTPTransportE2E(t, "streamhttp", "wrapper=raw&tls=1&tlsintls=1", ""),
		ts.URL,
		"streamhttp-tlsintls",
	)
}

func TestE2E_StreamHTTPSTransport_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello streamhttps"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupStreamHTTPTransportE2E(t, "streamhttps", "wrapper=raw", ""), ts.URL, "hello streamhttps")
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP transport
// ─────────────────────────────────────────────────────────────────────────────

// setupHTTPTransportE2E launches a REM server (HTTP transport) and a proxy-mode
// client as separate subprocesses. HTTP transport uses long-polling over HTTP.
func setupHTTPTransportE2E(t *testing.T) string {
	t.Helper()
	httpPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("httpe2e%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s http://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", httpPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start HTTP transport server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", httpPort), 10*time.Second)
	time.Sleep(1 * time.Second) // let server fully initialize yamux before client connects

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c http://127.0.0.1:%d/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			httpPort, socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start HTTP transport client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForSOCKSHTTPReady(t, socksAddr, 20*time.Second)
	return socksAddr
}

// TestE2E_HTTPTransport_SOCKS5Proxy verifies basic connectivity over HTTP
// transport (long-polling).
func TestE2E_HTTPTransport_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello http-transport"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupHTTPTransportE2E(t), ts.URL, "hello http-transport")
}

// TestE2E_HTTPTransport_LargeTransfer sends 512 KB of random data through the
// HTTP transport tunnel and verifies SHA-256 integrity.
func TestE2E_HTTPTransport_LargeTransfer(t *testing.T) {
	verifyLargeTransfer(t, setupHTTPTransportE2E(t), 512*1024)
}

// TestE2E_HTTPTransport_ConcurrentRequests fires 5 parallel requests through
// the HTTP transport tunnel to verify multiplexing.
func TestE2E_HTTPTransport_ConcurrentRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("http-transport-concurrent"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupHTTPTransportE2E(t), ts.URL, "http-transport-concurrent", 5)
}

// ─────────────────────────────────────────────────────────────────────────────
// Unix domain socket transport
// ─────────────────────────────────────────────────────────────────────────────

// setupUnixE2E launches a REM server (Unix domain socket transport) and a
// proxy-mode client as separate subprocesses. Unix sockets are only available
// on Linux/macOS.
func setupUnixE2E(t *testing.T) string {
	t.Helper()
	socksPort := freePort(t)
	alias := fmt.Sprintf("unixe2e%d", atomic.AddUint32(&testCounter, 1))
	socketPath := fmt.Sprintf("/tmp/rem-test-%s.sock", alias)

	// Server: -s unix:///tmp/rem-test-XXX.sock?wrapper=raw
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s unix://%s?wrapper=raw -i 127.0.0.1 --no-sub", socketPath),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start Unix server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	// Wait for socket file to be created
	time.Sleep(2 * time.Second)

	// Client: -c unix:///tmp/rem-test-XXX.sock?wrapper=raw -m proxy -l socks5://...
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c unix://%s?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			socketPath, socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start Unix client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	return socksAddr
}

// TestE2E_Unix_SOCKS5Proxy verifies basic connectivity over Unix domain socket.
// This test only runs on Linux/macOS.
func TestE2E_Unix_SOCKS5Proxy(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("Unix domain sockets not supported on Windows")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello unix"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupUnixE2E(t), ts.URL, "hello unix")
}

// TestE2E_Unix_LargeTransfer sends 512 KB of random data through the Unix
// socket tunnel and verifies SHA-256 integrity.
func TestE2E_Unix_LargeTransfer(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("Unix domain sockets not supported on Windows")
	}

	verifyLargeTransfer(t, setupUnixE2E(t), 512*1024)
}

// ─────────────────────────────────────────────────────────────────────────────
// DNS transport (KCP over UDP port 53)
// ─────────────────────────────────────────────────────────────────────────────

// setupDNSE2E launches a REM server (DNS transport on port 53) and a proxy-mode
// client as separate subprocesses. This tests the DNS tunnel without requiring
// an actual DNS server.
func setupDNSE2E(t *testing.T) string {
	t.Helper()
	dnsPort := freeUDPPort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("dnse2e%d", atomic.AddUint32(&testCounter, 1))

	// Server: -s dns://0.0.0.0:5353/?wrapper=raw&domain=rem.test.local
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s dns://0.0.0.0:%d/?wrapper=raw&domain=rem.test.local&interval=50 -i 127.0.0.1 --no-sub", dnsPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start DNS server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	// DNS/KCP cannot be probed via TCP; give the listener time to bind.
	// DNS transport uses TXT records and needs extra time for KCP handshake.
	time.Sleep(5 * time.Second)

	// Client: -c dns://127.0.0.1:5353/?wrapper=raw&domain=rem.test.local -m proxy -l socks5://...
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c dns://127.0.0.1:%d/?wrapper=raw&domain=rem.test.local&interval=50 -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			dnsPort, socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start DNS client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 40*time.Second)
	time.Sleep(300 * time.Millisecond)
	return socksAddr
}

// TestE2E_DNS_SOCKS5Proxy verifies basic connectivity over DNS transport
// (KCP over UDP port 5353).
func TestE2E_DNS_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello dns"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupDNSE2E(t), ts.URL, "hello dns")
}

// TestE2E_DNS_LargeTransfer sends 16 KB of random data through the DNS
// tunnel and verifies SHA-256 integrity. DNS transport has very limited
// bandwidth (data is encoded in DNS TXT labels with ~130-byte MTU), so
// we keep the payload small to fit within the 30s HTTP client timeout.
func TestE2E_DNS_LargeTransfer(t *testing.T) {
	verifyLargeTransfer(t, setupDNSE2E(t), 16*1024)
}

// ───────────────────────────────────────────────────────────��─────────────────
// Stress tests (extreme scenarios)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_TCP_VeryLargeTransfer sends 256 MB of random data through the TCP
// tunnel and verifies SHA-256 integrity. This is a stress test for large file
// transfers.
func TestE2E_TCP_VeryLargeTransfer(t *testing.T) {
	if os.Getenv("REM_STRESS") == "" {
		t.Skip("set REM_STRESS=1 to run stress tests")
	}

	verifyLargeTransfer(t, setupTCPE2E(t), 256*1024*1024) // 256 MB
}

// TestE2E_TCP_HighConcurrency fires 100 concurrent requests through the TCP
// tunnel to verify high-load multiplexing and stream isolation.
func TestE2E_TCP_HighConcurrency(t *testing.T) {
	if os.Getenv("REM_STRESS") == "" {
		t.Skip("set REM_STRESS=1 to run stress tests")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("tcp-high-concurrency"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupTCPE2E(t), ts.URL, "tcp-high-concurrency", 100)
}

// TestE2E_UDP_VeryLargeTransfer sends 256 MB of random data through the UDP/KCP
// tunnel and verifies SHA-256 integrity.
func TestE2E_UDP_VeryLargeTransfer(t *testing.T) {
	if os.Getenv("REM_STRESS") == "" {
		t.Skip("set REM_STRESS=1 to run stress tests")
	}

	verifyLargeTransfer(t, setupUDPE2E(t), 256*1024*1024) // 256 MB
}

// TestE2E_UDP_HighConcurrency fires 100 concurrent requests through the UDP/KCP
// tunnel to verify high-load multiplexing.
func TestE2E_UDP_HighConcurrency(t *testing.T) {
	if os.Getenv("REM_STRESS") == "" {
		t.Skip("set REM_STRESS=1 to run stress tests")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("udp-high-concurrency"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupUDPE2E(t), ts.URL, "udp-high-concurrency", 100)
}

// TestE2E_WebSocket_VeryLargeTransfer sends 256 MB of random data through the
// WebSocket tunnel and verifies SHA-256 integrity.
func TestE2E_WebSocket_VeryLargeTransfer(t *testing.T) {
	if os.Getenv("REM_STRESS") == "" {
		t.Skip("set REM_STRESS=1 to run stress tests")
	}

	verifyLargeTransfer(t, setupWSE2E(t), 256*1024*1024) // 256 MB
}

// TestE2E_WebSocket_HighConcurrency fires 100 concurrent requests through the
// WebSocket tunnel to verify high-load multiplexing.
func TestE2E_WebSocket_HighConcurrency(t *testing.T) {
	if os.Getenv("REM_STRESS") == "" {
		t.Skip("set REM_STRESS=1 to run stress tests")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ws-high-concurrency"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupWSE2E(t), ts.URL, "ws-high-concurrency", 100)
}
