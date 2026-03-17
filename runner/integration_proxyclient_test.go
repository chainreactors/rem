//go:build !tinygo && proxyclient_shadowsocks

package runner

// integration_proxyclient_test.go - E2E tests for Shadowsocks and Trojan
// proxy integration.
//
// Test coverage:
//   - SS client via -x (outbound proxy chain): SOCKS5 → tunnel → -x ss:// → SS proxy → target
//   - SS client via -f (forward proxy):        SOCKS5 → -f ss:// → SS proxy → tunnel → target
//   - SS serve (-l ss://):                     SS client → SS inbound → tunnel → target
//   - Trojan serve (-l trojan://):             Trojan client → Trojan inbound → tunnel → target
//
// Build tag: proxyclient_shadowsocks is required for proxyclient to support
// the ss:// scheme used in -x and -f flags.

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
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

	ss "github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"

	_ "github.com/chainreactors/rem/protocol/serve/shadowsocks"
	_ "github.com/chainreactors/rem/protocol/serve/trojan"
)

// ─────────────────────────────────────────────────────────────────────────────
// Standalone Shadowsocks proxy server
// ─────────────────────────────────────────────────────────────────────────────

// startSSProxyServer starts a standalone Shadowsocks proxy server that accepts
// SS-encrypted connections, reads the SOCKS5 target address, dials it, and
// relays data bidirectionally. Returns the listener address.
func startSSProxyServer(t *testing.T, password, cipher string) string {
	t.Helper()

	ciph, err := ss.PickCipher(cipher, nil, password)
	if err != nil {
		t.Fatalf("PickCipher: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
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
				sc := ciph.StreamConn(c)

				tgt, err := socks.ReadAddr(sc)
				if err != nil {
					return
				}

				rc, err := net.DialTimeout("tcp", tgt.String(), 5*time.Second)
				if err != nil {
					return
				}
				defer rc.Close()

				// bidirectional copy
				done := make(chan struct{}, 2)
				go func() { io.Copy(rc, sc); done <- struct{}{} }()
				go func() { io.Copy(sc, rc); done <- struct{}{} }()
				<-done
			}(conn)
		}
	}()

	return ln.Addr().String()
}

// ─────────────────────────────────────────────────────────────────────────────
// SS client helpers (outbound -x and forward -f)
// ─────────────────────────────────────────────────────────────────────────────

// setupSSClientOutboundE2E launches:
//  1. REM server with -x ss://... (outbound proxy via SS)
//  2. REM client with SOCKS5 listener
//
// Flow: SOCKS5 client → REM client → tunnel → REM server -x ss:// → SS proxy → target
func setupSSClientOutboundE2E(t *testing.T, ssProxyAddr string) string {
	t.Helper()
	serverPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("ssxe2e%d", atomic.AddUint32(&testCounter, 1))

	// Server with outbound proxy (-x) pointing to SS proxy
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub -x ss://aes-256-gcm:testpass@%s",
			serverPort, ssProxyAddr),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start SS outbound server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", serverPort), 10*time.Second)

	// Client with SOCKS5 listener
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://127.0.0.1:%d/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			serverPort, socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start SS outbound client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	return socksAddr
}

// setupSSClientForwardE2E launches:
//  1. REM server (plain)
//  2. REM client with -f ss://... (forward proxy via SS)
//
// Flow: SOCKS5 client → REM client -f ss:// → SS proxy → REM server → target
func setupSSClientForwardE2E(t *testing.T, ssProxyAddr string) string {
	t.Helper()
	serverPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("ssfe2e%d", atomic.AddUint32(&testCounter, 1))

	// Plain server (no proxy flags)
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start SS forward server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", serverPort), 10*time.Second)

	// Client with forward proxy (-f) through SS proxy
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://127.0.0.1:%d/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug -f ss://aes-256-gcm:testpass@%s",
			serverPort, socksPort, alias, ssProxyAddr),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start SS forward client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	return socksAddr
}

// ─────────────────────────────────────────────────────────────────────────────
// SS serve helpers (-l ss://)
// ─────────────────────────────────────────────────────────────────────────────

// setupSSServeE2E launches:
//  1. REM server (plain TCP)
//  2. REM client with -l ss://... (Shadowsocks inbound serve)
//
// Returns the SS serve address that an SS client can connect to.
func setupSSServeE2E(t *testing.T) string {
	t.Helper()
	serverPort := freePort(t)
	ssPort := freePort(t)
	alias := fmt.Sprintf("ssse2e%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start SS serve server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", serverPort), 10*time.Second)

	// Client with SS inbound (-l ss://)
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://127.0.0.1:%d/?wrapper=raw -m proxy -l ss://0.0.0.0:%d/?password=testpass -a %s --debug",
			serverPort, ssPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start SS serve client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	ssAddr := fmt.Sprintf("127.0.0.1:%d", ssPort)
	waitForTCP(t, ssAddr, 15*time.Second)
	// Extra wait: the waitForTCP probe creates a bare TCP connection to the SS
	// port, which consumes a bridge that fails on ReadAddr (expected). Give the
	// bridge time to drain before real SS connections come in.
	time.Sleep(2 * time.Second)
	return ssAddr
}

// dialSS creates an SS-encrypted connection to ssAddr and sends the SOCKS5
// address header for targetAddr. Returns a net.Conn ready for data transfer.
func dialSS(t *testing.T, ssAddr, targetAddr, password string) net.Conn {
	t.Helper()

	ciph, err := ss.PickCipher("aes-256-gcm", nil, password)
	if err != nil {
		t.Fatalf("PickCipher: %v", err)
	}

	conn, err := net.DialTimeout("tcp", ssAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial SS %s: %v", ssAddr, err)
	}

	sc := ciph.StreamConn(conn)

	// Write SOCKS5 address header for the target
	tgt := socks.ParseAddr(targetAddr)
	if tgt == nil {
		conn.Close()
		t.Fatalf("ParseAddr(%s) returned nil", targetAddr)
	}
	if _, err := sc.Write(tgt); err != nil {
		conn.Close()
		t.Fatalf("write SS target addr: %v", err)
	}

	return sc
}

// ssEchoRoundtrip connects to an SS serve, sends msg through to an echo server,
// and verifies the echo response matches.
func ssEchoRoundtrip(t *testing.T, ssAddr, echoAddr, password string, msg []byte) {
	t.Helper()

	conn := dialSS(t, ssAddr, echoAddr, password)
	defer conn.Close()

	conn.(interface{ SetDeadline(time.Time) error }).SetDeadline(time.Now().Add(10 * time.Second))

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
// Trojan serve helpers (-l trojan://)
// ─────────────────────────────────────────────────────────────────────────────

// setupTrojanServeE2E launches:
//  1. REM server (plain TCP)
//  2. REM client with -l trojan://... (Trojan inbound serve)
//
// Returns the Trojan serve address that a Trojan client can connect to.
func setupTrojanServeE2E(t *testing.T) string {
	t.Helper()
	serverPort := freePort(t)
	trojanPort := freePort(t)
	alias := fmt.Sprintf("trje2e%d", atomic.AddUint32(&testCounter, 1))

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start Trojan serve server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", serverPort), 10*time.Second)

	// Client with Trojan inbound (-l trojan://)
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://127.0.0.1:%d/?wrapper=raw -m proxy -l trojan://0.0.0.0:%d/?password=testpass -a %s --debug",
			serverPort, trojanPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start Trojan serve client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	trojanAddr := fmt.Sprintf("127.0.0.1:%d", trojanPort)
	waitForTCP(t, trojanAddr, 15*time.Second)
	// Extra wait: the waitForTCP probe creates a bare TCP connection to the
	// Trojan port. The Trojan Relay wraps it in TLS, which fails. Give the
	// bridge time to drain before real Trojan connections come in.
	time.Sleep(2 * time.Second)
	return trojanAddr
}

// sha224Hex computes SHA-224 of s and returns the 56-character hex string,
// matching the trojan protocol token format.
func sha224Hex(s string) string {
	h := sha256.New224()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// dialTrojan creates a Trojan protocol connection to trojanAddr, targeting
// targetAddr with the given password. The wire format is:
//
//	[56B sha224(password)] [CRLF] [CMD=0x01] [ATYP] [ADDR] [PORT BE] [CRLF]
//
// Returns a net.Conn (TLS) ready for data transfer after the header.
func dialTrojan(t *testing.T, trojanAddr, targetAddr, password string) net.Conn {
	t.Helper()

	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp",
		trojanAddr,
		&tls.Config{InsecureSkipVerify: true},
	)
	if err != nil {
		t.Fatalf("TLS dial %s: %v", trojanAddr, err)
	}

	// Build trojan request header
	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		tlsConn.Close()
		t.Fatalf("SplitHostPort(%s): %v", targetAddr, err)
	}
	portNum := 0
	fmt.Sscanf(portStr, "%d", &portNum)

	token := sha224Hex(password)

	var header []byte
	// Token (56 bytes)
	header = append(header, []byte(token)...)
	// CRLF
	header = append(header, 0x0D, 0x0A)
	// Command: Connect
	header = append(header, 0x01)

	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil {
		// IPv4
		header = append(header, 0x01)
		header = append(header, ip4...)
	} else if ip != nil {
		// IPv6
		header = append(header, 0x04)
		header = append(header, ip.To16()...)
	} else {
		// Domain
		header = append(header, 0x03)
		header = append(header, byte(len(host)))
		header = append(header, []byte(host)...)
	}

	// Port (big-endian)
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, uint16(portNum))
	header = append(header, portBuf...)
	// CRLF
	header = append(header, 0x0D, 0x0A)

	if _, err := tlsConn.Write(header); err != nil {
		tlsConn.Close()
		t.Fatalf("write trojan header: %v", err)
	}

	return tlsConn
}

// trojanEchoRoundtrip connects to a Trojan serve, sends msg through to an echo
// server, and verifies the echo response matches.
func trojanEchoRoundtrip(t *testing.T, trojanAddr, echoAddr, password string, msg []byte) {
	t.Helper()

	conn := dialTrojan(t, trojanAddr, echoAddr, password)
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
// SS client tests (-x outbound proxy)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_ProxyClient_SS_Outbound_Echo verifies HTTP through:
// SOCKS5 client → REM client → tunnel → REM server -x ss:// → SS proxy → HTTP server
func TestE2E_ProxyClient_SS_Outbound_Echo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello ss outbound"))
	}))
	defer ts.Close()

	ssProxyAddr := startSSProxyServer(t, "testpass", "aes-256-gcm")
	socksAddr := setupSSClientOutboundE2E(t, ssProxyAddr)
	verifyHTTP(t, socksAddr, ts.URL, "hello ss outbound")
}

// ─────────────────────────────────────────────────────────────────────────────
// SS client tests (-f forward proxy)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_ProxyClient_SS_Forward_Echo verifies HTTP through:
// SOCKS5 client → REM client -f ss:// → SS proxy → REM server → HTTP server
func TestE2E_ProxyClient_SS_Forward_Echo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello ss forward"))
	}))
	defer ts.Close()

	ssProxyAddr := startSSProxyServer(t, "testpass", "aes-256-gcm")
	socksAddr := setupSSClientForwardE2E(t, ssProxyAddr)
	verifyHTTP(t, socksAddr, ts.URL, "hello ss forward")
}

// ─────────────────────────────────────────────────────────────────────────────
// SS serve tests (-l ss://)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_ProxyClient_SS_Serve_Echo verifies echo through:
// SS client → SS inbound → tunnel → REM server → echo server
func TestE2E_ProxyClient_SS_Serve_Echo(t *testing.T) {
	echoAddr := startEchoServer(t)
	ssAddr := setupSSServeE2E(t)
	ssEchoRoundtrip(t, ssAddr, echoAddr, "testpass", []byte("hello ss serve"))
}

// TestE2E_ProxyClient_SS_Serve_LargePayload sends 128KB through SS serve.
func TestE2E_ProxyClient_SS_Serve_LargePayload(t *testing.T) {
	echoAddr := startEchoServer(t)
	ssAddr := setupSSServeE2E(t)

	payload := make([]byte, 128*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	ssEchoRoundtrip(t, ssAddr, echoAddr, "testpass", payload)
}

// TestE2E_ProxyClient_SS_Serve_ConcurrentConns opens 5 concurrent SS connections.
func TestE2E_ProxyClient_SS_Serve_ConcurrentConns(t *testing.T) {
	echoAddr := startEchoServer(t)
	ssAddr := setupSSServeE2E(t)

	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := []byte(fmt.Sprintf("ss-conn-%d-hello", idx))

			conn := dialSS(t, ssAddr, echoAddr, "testpass")
			defer conn.Close()

			conn.(interface{ SetDeadline(time.Time) error }).SetDeadline(time.Now().Add(10 * time.Second))

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

// ─────────────────────────────────────────────────────────────────────────────
// Trojan serve tests (-l trojan://)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_ProxyClient_Trojan_Serve_Echo verifies echo through:
// Trojan client → Trojan inbound → tunnel → REM server → echo server
func TestE2E_ProxyClient_Trojan_Serve_Echo(t *testing.T) {
	echoAddr := startEchoServer(t)
	trojanAddr := setupTrojanServeE2E(t)
	trojanEchoRoundtrip(t, trojanAddr, echoAddr, "testpass", []byte("hello trojan serve"))
}

// TestE2E_ProxyClient_Trojan_Serve_LargePayload sends 128KB through Trojan serve.
func TestE2E_ProxyClient_Trojan_Serve_LargePayload(t *testing.T) {
	echoAddr := startEchoServer(t)
	trojanAddr := setupTrojanServeE2E(t)

	payload := make([]byte, 128*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	trojanEchoRoundtrip(t, trojanAddr, echoAddr, "testpass", payload)
}

// TestE2E_ProxyClient_Trojan_Serve_ConcurrentConns opens 5 concurrent Trojan connections.
func TestE2E_ProxyClient_Trojan_Serve_ConcurrentConns(t *testing.T) {
	echoAddr := startEchoServer(t)
	trojanAddr := setupTrojanServeE2E(t)

	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := []byte(fmt.Sprintf("trojan-conn-%d-hello", idx))

			conn := dialTrojan(t, trojanAddr, echoAddr, "testpass")
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
