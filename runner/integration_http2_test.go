//go:build !tinygo

package runner

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/chainreactors/rem/protocol/tunnel/http2"
	xsocks5 "github.com/chainreactors/rem/x/socks5"
)

func setupHTTP2TransportE2E(t *testing.T, scheme, query, forwardURL string) string {
	t.Helper()

	http2Port := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("%se2e%d", scheme, atomic.AddUint32(&testCounter, 1))
	serverURL := fmt.Sprintf("%s://0.0.0.0:%d/rem?%s", scheme, http2Port, query)
	clientURL := fmt.Sprintf("%s://127.0.0.1:%d/rem?%s", scheme, http2Port, query)

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

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", http2Port), 10*time.Second)

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
	time.Sleep(300 * time.Millisecond)
	return socksAddr
}

func startHTTP2ForwardSOCKS5Proxy(t *testing.T) string {
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

func TestE2E_HTTP2Transport_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello http2"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupHTTP2TransportE2E(t, "http2", "wrapper=raw", ""), ts.URL, "hello http2")
}

func TestE2E_HTTP2Transport_LargeTransfer(t *testing.T) {
	verifyLargeTransfer(t, setupHTTP2TransportE2E(t, "http2", "wrapper=raw", ""), 512*1024)
}

func TestE2E_HTTP2Transport_ConcurrentRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("http2-concurrent"))
	}))
	defer ts.Close()

	verifyConcurrent(t, setupHTTP2TransportE2E(t, "http2", "wrapper=raw", ""), ts.URL, "http2-concurrent", 5)
}

func TestE2E_HTTP2Transport_ForwardProxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("http2-forward"))
	}))
	defer ts.Close()

	verifyHTTP(
		t,
		setupHTTP2TransportE2E(t, "http2", "wrapper=raw", startHTTP2ForwardSOCKS5Proxy(t)),
		ts.URL,
		"http2-forward",
	)
}

func TestE2E_HTTP2Transport_TLSInTLS(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("http2-tlsintls"))
	}))
	defer ts.Close()

	verifyHTTP(
		t,
		setupHTTP2TransportE2E(t, "http2", "wrapper=raw&tlsintls=1", ""),
		ts.URL,
		"http2-tlsintls",
	)
}

func TestE2E_HTTP2STransport_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello http2s"))
	}))
	defer ts.Close()

	verifyHTTP(t, setupHTTP2TransportE2E(t, "http2s", "wrapper=raw", ""), ts.URL, "hello http2s")
}
