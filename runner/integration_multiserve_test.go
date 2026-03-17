//go:build !tinygo

package runner

// integration_multiserve_test.go - E2E tests for multi-serve (multiple -l/-r pairs).
//
// Tests verify that a single client connection can fork multiple serve types
// sharing one yamux session. This exercises the ExtraServes + auto-Fork path
// added in the multi-serve feature.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Multi-serve helpers
// ─────────────────────────────────────────────────────────────────────────────

// setupMultiServeSOCKS5HTTPE2E launches a REM server and a client with two
// serves: SOCKS5 + HTTP proxy, both in proxy mode sharing one yamux session.
// Returns (socksAddr, httpProxyAddr).
func setupMultiServeSOCKS5HTTPE2E(t *testing.T) (string, string) {
	t.Helper()
	serverPort := freePort(t)
	socksPort := freePort(t)
	httpPort := freePort(t)
	alias := fmt.Sprintf("ms_sh%d", atomic.AddUint32(&testCounter, 1))

	// --- server subprocess ---
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start multi-serve server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	waitForTCP(t, serverAddr, 10*time.Second)

	// --- client subprocess with two -l flags ---
	// Primary serve: socks5, Extra serve (auto-forked): http
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://%s/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -l http://127.0.0.1:%d -a %s --debug",
			serverAddr, socksPort, httpPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start multi-serve client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	httpProxyAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)

	waitForTCP(t, socksAddr, 15*time.Second)
	waitForTCP(t, httpProxyAddr, 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	return socksAddr, httpProxyAddr
}

// setupMultiServeDualSOCKS5E2E launches a server and a client with two
// SOCKS5 serves on different ports, sharing one yamux session.
// Returns (socksAddr1, socksAddr2).
func setupMultiServeDualSOCKS5E2E(t *testing.T) (string, string) {
	t.Helper()
	serverPort := freePort(t)
	socksPort1 := freePort(t)
	socksPort2 := freePort(t)
	alias := fmt.Sprintf("ms_ds%d", atomic.AddUint32(&testCounter, 1))

	// --- server subprocess ---
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	waitForTCP(t, serverAddr, 10*time.Second)

	// --- client subprocess with two SOCKS5 serves ---
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://%s/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -l socks5://127.0.0.1:%d -a %s --debug",
			serverAddr, socksPort1, socksPort2, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	addr1 := fmt.Sprintf("127.0.0.1:%d", socksPort1)
	addr2 := fmt.Sprintf("127.0.0.1:%d", socksPort2)

	waitForTCP(t, addr1, 15*time.Second)
	waitForTCP(t, addr2, 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	return addr1, addr2
}

// ─────────────────────────────────────────────────────────────────────────────
// Multi-serve: SOCKS5 + HTTP proxy
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_MultiServe_SOCKS5_HTTP verifies that a single client with two serves
// (socks5 + http proxy) can serve traffic through both simultaneously.
func TestE2E_MultiServe_SOCKS5_HTTP(t *testing.T) {
	// Plain HTTP for SOCKS5 test
	tsPlain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("multi-serve-ok"))
	}))
	defer tsPlain.Close()

	// TLS server for HTTP proxy CONNECT test
	tsTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("multi-serve-https"))
	}))
	defer tsTLS.Close()

	socksAddr, httpProxyAddr := setupMultiServeSOCKS5HTTPE2E(t)

	// Test SOCKS5 serve (plain HTTP)
	socksClient := newSOCKS5Client(t, socksAddr, 10*time.Second)
	resp, err := socksClient.Get(tsPlain.URL)
	if err != nil {
		t.Fatalf("SOCKS5 GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "multi-serve-ok" {
		t.Fatalf("SOCKS5: expected %q, got %q", "multi-serve-ok", body)
	}

	// Test HTTP proxy serve (CONNECT for HTTPS)
	proxyURL, _ := url.Parse("http://remno1:0onmer@" + httpProxyAddr)
	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: tsTLS.Client().Transport.(*http.Transport).TLSClientConfig,
		},
		Timeout: 10 * time.Second,
	}
	resp, err = httpClient.Get(tsTLS.URL)
	if err != nil {
		t.Fatalf("HTTP proxy GET: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "multi-serve-https" {
		t.Fatalf("HTTP proxy: expected %q, got %q", "multi-serve-https", body)
	}
}

// TestE2E_MultiServe_DualSOCKS5_Concurrent sends concurrent requests through
// two SOCKS5 serves simultaneously to verify they don't interfere with each
// other when sharing a yamux session.
func TestE2E_MultiServe_DualSOCKS5_Concurrent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("concurrent-dual"))
	}))
	defer ts.Close()

	addr1, addr2 := setupMultiServeDualSOCKS5E2E(t)

	const reqsPerServe = 5
	var wg sync.WaitGroup
	errs := make(chan error, reqsPerServe*2)

	// SOCKS5[1] concurrent requests
	for i := 0; i < reqsPerServe; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			client := newSOCKS5Client(t, addr1, 10*time.Second)
			resp, err := client.Get(ts.URL)
			if err != nil {
				errs <- fmt.Errorf("socks5_1[%d]: %w", idx, err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "concurrent-dual" {
				errs <- fmt.Errorf("socks5_1[%d]: expected %q, got %q", idx, "concurrent-dual", body)
			}
		}(i)
	}

	// SOCKS5[2] concurrent requests (through forked agent)
	for i := 0; i < reqsPerServe; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			client := newSOCKS5Client(t, addr2, 10*time.Second)
			resp, err := client.Get(ts.URL)
			if err != nil {
				errs <- fmt.Errorf("socks5_2[%d]: %w", idx, err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "concurrent-dual" {
				errs <- fmt.Errorf("socks5_2[%d]: expected %q, got %q", idx, "concurrent-dual", body)
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
// Multi-serve: Dual SOCKS5
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_MultiServe_DualSOCKS5 verifies that two SOCKS5 serves on different
// ports coexist on a single yamux session. Both should independently serve
// traffic to the same HTTP target.
func TestE2E_MultiServe_DualSOCKS5(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("dual-socks5-ok"))
	}))
	defer ts.Close()

	addr1, addr2 := setupMultiServeDualSOCKS5E2E(t)

	// Test first SOCKS5
	client1 := newSOCKS5Client(t, addr1, 10*time.Second)
	resp, err := client1.Get(ts.URL)
	if err != nil {
		t.Fatalf("SOCKS5[1] GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "dual-socks5-ok" {
		t.Fatalf("SOCKS5[1]: expected %q, got %q", "dual-socks5-ok", body)
	}

	// Test second SOCKS5 (forked)
	client2 := newSOCKS5Client(t, addr2, 10*time.Second)
	resp, err = client2.Get(ts.URL)
	if err != nil {
		t.Fatalf("SOCKS5[2] GET: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "dual-socks5-ok" {
		t.Fatalf("SOCKS5[2]: expected %q, got %q", "dual-socks5-ok", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Multi-serve: backward compatibility (single serve)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_MultiServe_SingleServeCompat verifies that a single -l/-r pair
// still works exactly as before the multi-serve change.
func TestE2E_MultiServe_SingleServeCompat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("compat-ok"))
	}))
	defer ts.Close()

	// setupE2E uses the original single-serve setup
	socksAddr := setupE2E(t)
	httpClient := newSOCKS5Client(t, socksAddr, 10*time.Second)

	resp, err := httpClient.Get(ts.URL)
	if err != nil {
		t.Fatalf("backward-compat SOCKS5 GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "compat-ok" {
		t.Fatalf("expected %q, got %q", "compat-ok", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Multi-serve: SOCKS5 + Fetch (reverse mode, verifies fetch inbound)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_MultiServe_SOCKS5_Fetch verifies the multi-serve scenario with
// fetch as the extra serve. In non-WASM builds, fetch only registers
// an Inbound (no Outbound), so the forked serve's Outbound creation fails on
// the server side. This test verifies:
//  1. The fetch fork failure is logged but doesn't crash
//  2. The primary SOCKS5 serve remains fully functional
//
// This is the closest approximation to the WASM fetch scenario in a native test.
// In WASM builds, fetch also registers an Outbound so both serves work.
func TestE2E_MultiServe_SOCKS5_Fetch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fetch-socks-ok"))
	}))
	defer ts.Close()

	serverPort := freePort(t)
	socksPort := freePort(t)
	fpPort := freePort(t)
	alias := fmt.Sprintf("ms_fp%d", atomic.AddUint32(&testCounter, 1))

	// --- server subprocess ---
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	waitForTCP(t, serverAddr, 10*time.Second)

	// --- client subprocess: proxy mode, socks5 (primary) + fetch (extra) ---
	// In proxy mode, client does handlerInbound → InboundCreate for the serve.
	// fetch has Inbound registered (passthrough) on all platforms.
	// Server does handlerOutbound → OutboundCreate. fetch Outbound is
	// NOT registered in non-WASM, so the server-side fork will fail.
	// The primary SOCKS5 serve should remain unaffected.
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://%s/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -l fetch://127.0.0.1:%d -a %s --debug",
			serverAddr, socksPort, fpPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(500 * time.Millisecond)

	// Verify SOCKS5 primary serve works despite fetch fork failure
	httpClient := newSOCKS5Client(t, socksAddr, 10*time.Second)
	resp, err := httpClient.Get(ts.URL)
	if err != nil {
		t.Fatalf("SOCKS5 GET through multi-serve with fetch: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "fetch-socks-ok" {
		t.Fatalf("expected %q, got %q", "fetch-socks-ok", body)
	}
	t.Log("SOCKS5 primary serve works; fetch fork expected to fail gracefully in non-WASM")
}

// ─────────────────────────────────────────────────────────────────────────────
// Multi-serve: N-SOCKS5 generic helper
// ─────────────────────────────────────────────────────────────────────────────

// setupMultiServeNSOCKS5E2E launches a server and a client with N SOCKS5
// serves on different ports, all sharing one yamux session.
// Returns a slice of SOCKS5 addresses (len == n).
func setupMultiServeNSOCKS5E2E(t *testing.T, n int) []string {
	t.Helper()
	if n < 1 {
		t.Fatal("need at least 1 serve")
	}

	serverPort := freePort(t)
	socksPorts := make([]int, n)
	for i := range socksPorts {
		socksPorts[i] = freePort(t)
	}
	alias := fmt.Sprintf("ms_n%d_%d", n, atomic.AddUint32(&testCounter, 1))

	// --- server subprocess ---
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start server subprocess: %v", err)
	}
	t.Cleanup(func() { serverProc.Process.Kill(); serverProc.Wait() })

	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	waitForTCP(t, serverAddr, 10*time.Second)

	// --- build client cmdline with N -l flags ---
	cmd := fmt.Sprintf("-c tcp://%s/?wrapper=raw -m proxy", serverAddr)
	for _, port := range socksPorts {
		cmd += fmt.Sprintf(" -l socks5://127.0.0.1:%d", port)
	}
	cmd += fmt.Sprintf(" -a %s --debug", alias)

	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=%s", cmd),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start client subprocess: %v", err)
	}
	t.Cleanup(func() { clientProc.Process.Kill(); clientProc.Wait() })

	addrs := make([]string, n)
	for i, port := range socksPorts {
		addrs[i] = fmt.Sprintf("127.0.0.1:%d", port)
	}

	for _, addr := range addrs {
		waitForTCP(t, addr, 15*time.Second)
	}
	time.Sleep(500 * time.Millisecond)
	return addrs
}

// ─────────────────────────────────────────────────────────────────────────────
// Multi-serve: 2-pair SOCKS5 (sequential + concurrent)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_MultiServe_2Pair verifies 2 SOCKS5 serves (1 primary + 1 fork)
// each independently serve traffic, both sequentially and concurrently.
func TestE2E_MultiServe_2Pair(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("2pair-ok"))
	}))
	defer ts.Close()

	addrs := setupMultiServeNSOCKS5E2E(t, 2)

	// Sequential: each serve individually
	for i, addr := range addrs {
		client := newSOCKS5Client(t, addr, 10*time.Second)
		resp, err := client.Get(ts.URL)
		if err != nil {
			t.Fatalf("SOCKS5[%d] sequential GET: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "2pair-ok" {
			t.Fatalf("SOCKS5[%d]: expected %q, got %q", i, "2pair-ok", body)
		}
		t.Logf("SOCKS5[%d] (%s) sequential: OK", i, addr)
	}

	// Concurrent: 3 requests per serve simultaneously
	const reqsPerServe = 3
	var wg sync.WaitGroup
	errs := make(chan error, reqsPerServe*len(addrs))
	for i, addr := range addrs {
		for j := 0; j < reqsPerServe; j++ {
			wg.Add(1)
			go func(serveIdx, reqIdx int, a string) {
				defer wg.Done()
				client := newSOCKS5Client(t, a, 10*time.Second)
				resp, err := client.Get(ts.URL)
				if err != nil {
					errs <- fmt.Errorf("SOCKS5[%d] concurrent[%d]: %w", serveIdx, reqIdx, err)
					return
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(body) != "2pair-ok" {
					errs <- fmt.Errorf("SOCKS5[%d] concurrent[%d]: expected %q, got %q", serveIdx, reqIdx, "2pair-ok", body)
				}
			}(i, j, addr)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	t.Log("2-pair: all sequential and concurrent requests passed")
}

// ─────────────────────────────────────────────────────────────────────────────
// Multi-serve: 3-pair SOCKS5 (sequential + concurrent)
// ─────────────────────────────────────────────────────────────────────────────

// TestE2E_MultiServe_3Pair verifies 3 SOCKS5 serves (1 primary + 2 forks)
// each independently serve traffic, both sequentially and concurrently.
func TestE2E_MultiServe_3Pair(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("3pair-ok"))
	}))
	defer ts.Close()

	addrs := setupMultiServeNSOCKS5E2E(t, 3)

	// Sequential: each serve individually
	for i, addr := range addrs {
		client := newSOCKS5Client(t, addr, 10*time.Second)
		resp, err := client.Get(ts.URL)
		if err != nil {
			t.Fatalf("SOCKS5[%d] sequential GET: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "3pair-ok" {
			t.Fatalf("SOCKS5[%d]: expected %q, got %q", i, "3pair-ok", body)
		}
		t.Logf("SOCKS5[%d] (%s) sequential: OK", i, addr)
	}

	// Concurrent: 3 requests per serve simultaneously (9 total)
	const reqsPerServe = 3
	var wg sync.WaitGroup
	errs := make(chan error, reqsPerServe*len(addrs))
	for i, addr := range addrs {
		for j := 0; j < reqsPerServe; j++ {
			wg.Add(1)
			go func(serveIdx, reqIdx int, a string) {
				defer wg.Done()
				client := newSOCKS5Client(t, a, 10*time.Second)
				resp, err := client.Get(ts.URL)
				if err != nil {
					errs <- fmt.Errorf("SOCKS5[%d] concurrent[%d]: %w", serveIdx, reqIdx, err)
					return
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(body) != "3pair-ok" {
					errs <- fmt.Errorf("SOCKS5[%d] concurrent[%d]: expected %q, got %q", serveIdx, reqIdx, "3pair-ok", body)
				}
			}(i, j, addr)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	t.Log("3-pair: all sequential and concurrent requests passed")
}

// ─────────────────────────────────────────────────────────────────────────────
// Unit tests: CLI parsing for multi-serve
// ─────────────────────────────────────────────────────────────────────────────

// TestMultiServeParsing verifies that multiple -l/-r flags are correctly parsed
// into ExtraServes in the RunnerConfig.
func TestMultiServeParsing(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantExtras   int
		wantLocalSch []string // expected LocalURL schemes for extras
	}{
		{
			name:       "single serve, no extras",
			args:       []string{"-c", "tcp://127.0.0.1:1234/?wrapper=raw", "-m", "proxy", "-l", "socks5://127.0.0.1:1080"},
			wantExtras: 0,
		},
		{
			name:         "two serves: socks5 + http",
			args:         []string{"-c", "tcp://127.0.0.1:1234/?wrapper=raw", "-m", "proxy", "-l", "socks5://127.0.0.1:1080", "-l", "http://127.0.0.1:8080"},
			wantExtras:   1,
			wantLocalSch: []string{"http"},
		},
		{
			name:         "two serves: socks5 + fetch (reverse)",
			args:         []string{"-c", "tcp://127.0.0.1:1234/?wrapper=raw", "-m", "reverse", "-r", "socks5://0.0.0.0:1080", "-l", "socks5://", "-r", "fetch://0.0.0.0:18080", "-l", "fetch://"},
			wantExtras:   1,
			wantLocalSch: []string{"fetch"},
		},
		{
			name:         "three serves",
			args:         []string{"-c", "tcp://127.0.0.1:1234/?wrapper=raw", "-m", "proxy", "-l", "socks5://127.0.0.1:1080", "-l", "http://127.0.0.1:8080", "-l", "forward://0.0.0.0:9090"},
			wantExtras:   2,
			wantLocalSch: []string{"http", "forward"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opt Options
			if err := opt.ParseArgs(tt.args); err != nil {
				t.Fatalf("ParseArgs: %v", err)
			}
			cfg, err := opt.Prepare()
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if len(cfg.ExtraServes) != tt.wantExtras {
				t.Fatalf("expected %d extra serves, got %d", tt.wantExtras, len(cfg.ExtraServes))
			}
			for i, wantSch := range tt.wantLocalSch {
				gotSch := cfg.ExtraServes[i].LocalURL.Scheme
				if gotSch != wantSch {
					t.Errorf("extra[%d] LocalURL.Scheme = %q, want %q", i, gotSch, wantSch)
				}
			}
		})
	}
}

// TestMultiServeDefaultSchemes verifies that ExtraServes get the correct default
// schemes applied for both proxy and reverse modes.
func TestMultiServeDefaultSchemes(t *testing.T) {
	// proxy mode: LocalURL defaults to socks5, RemoteURL defaults to raw
	t.Run("proxy defaults", func(t *testing.T) {
		var opt Options
		err := opt.ParseArgs([]string{
			"-c", "tcp://127.0.0.1:1234/?wrapper=raw",
			"-m", "proxy",
			"-l", "socks5://127.0.0.1:1080",
			"-l", "127.0.0.1:8080", // no scheme → default
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := opt.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.ExtraServes) != 1 {
			t.Fatalf("expected 1 extra, got %d", len(cfg.ExtraServes))
		}
		if cfg.ExtraServes[0].LocalURL.Scheme != "socks5" {
			t.Errorf("extra LocalURL.Scheme = %q, want socks5", cfg.ExtraServes[0].LocalURL.Scheme)
		}
		if cfg.ExtraServes[0].RemoteURL.Scheme != "raw" {
			t.Errorf("extra RemoteURL.Scheme = %q, want raw", cfg.ExtraServes[0].RemoteURL.Scheme)
		}
	})

	// reverse mode: RemoteURL defaults to socks5, LocalURL defaults to raw
	t.Run("reverse defaults", func(t *testing.T) {
		var opt Options
		err := opt.ParseArgs([]string{
			"-c", "tcp://127.0.0.1:1234/?wrapper=raw",
			"-m", "reverse",
			"-r", "socks5://0.0.0.0:1080",
			"-l", "socks5://",
			"-r", "0.0.0.0:8080", // no scheme → default
			"-l", "", // no scheme → default
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := opt.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.ExtraServes) != 1 {
			t.Fatalf("expected 1 extra, got %d", len(cfg.ExtraServes))
		}
		if cfg.ExtraServes[0].RemoteURL.Scheme != "socks5" {
			t.Errorf("extra RemoteURL.Scheme = %q, want socks5", cfg.ExtraServes[0].RemoteURL.Scheme)
		}
		if cfg.ExtraServes[0].LocalURL.Scheme != "raw" {
			t.Errorf("extra LocalURL.Scheme = %q, want raw", cfg.ExtraServes[0].LocalURL.Scheme)
		}
	})
}

// TestMultiServeUnbalancedPairs verifies that unbalanced -l/-r counts work
// correctly: missing values default to empty (which gets default scheme).
func TestMultiServeUnbalancedPairs(t *testing.T) {
	// Two -l but only one -r → extra has empty RemoteURL
	var opt Options
	err := opt.ParseArgs([]string{
		"-c", "tcp://127.0.0.1:1234/?wrapper=raw",
		"-m", "proxy",
		"-l", "socks5://127.0.0.1:1080",
		"-l", "http://127.0.0.1:8080",
		// only one -r (or none)
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := opt.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ExtraServes) != 1 {
		t.Fatalf("expected 1 extra, got %d", len(cfg.ExtraServes))
	}
	// RemoteURL should default to raw (proxy mode default)
	if cfg.ExtraServes[0].RemoteURL.Scheme != "raw" {
		t.Errorf("extra RemoteURL.Scheme = %q, want raw", cfg.ExtraServes[0].RemoteURL.Scheme)
	}
	if cfg.ExtraServes[0].LocalURL.Scheme != "http" {
		t.Errorf("extra LocalURL.Scheme = %q, want http", cfg.ExtraServes[0].LocalURL.Scheme)
	}
}
