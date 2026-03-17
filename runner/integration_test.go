//go:build !tinygo

package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/protocol/core"
	_ "github.com/chainreactors/rem/protocol/tunnel/memory"
	"github.com/chainreactors/rem/x/utils"
	"golang.org/x/net/proxy"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func init() {
	utils.Log = logs.NewLogger(logs.DebugLevel)
}

// TestHelperProcess is not a real test. It is invoked as a subprocess by
// setupE2E so that the REM server and client each get their own OS process
// (and therefore their own agent.Agents map), which is required because the
// Redirect message routing assumes a single agent per alias per process.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("REM_HELPER") == "" {
		t.Skip("subprocess helper – skipped in normal test runs")
	}
	cmd := os.Getenv("REM_HELPER_CMD")
	if cmd == "" {
		t.Fatal("REM_HELPER_CMD not set")
	}
	console, err := NewConsoleWithCMD(cmd)
	if err != nil {
		t.Fatalf("NewConsoleWithCMD: %v", err)
	}
	// Run blocks forever (server: accept loop, client: handler loop).
	// The parent test kills the process on cleanup.
	console.Run()
}

// freePort finds an available TCP port on 127.0.0.1.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

var testCounter uint32

// setupE2E launches a REM server and client as separate subprocesses,
// waits for the SOCKS5 listener to be ready, and returns its address.
func setupE2E(t *testing.T) string {
	t.Helper()
	serverPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("e2e%d", atomic.AddUint32(&testCounter, 1))

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
	t.Cleanup(func() {
		serverProc.Process.Kill()
		serverProc.Wait()
	})

	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	waitForTCP(t, serverAddr, 10*time.Second)

	// --- client subprocess ---
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://%s/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
			serverAddr, socksPort, alias),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start client subprocess: %v", err)
	}
	t.Cleanup(func() {
		clientProc.Process.Kill()
		clientProc.Wait()
	})

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)

	// Let the probe-created bridge drain so it doesn't interfere.
	time.Sleep(500 * time.Millisecond)
	return socksAddr
}

// waitForTCP polls a TCP address until it accepts connections.
func waitForTCP(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("TCP at %s not ready within %v", addr, timeout)
}

func socksAuth() *proxy.Auth {
	return &proxy.Auth{User: "remno1", Password: "0onmer"}
}

func newSOCKS5Client(t *testing.T, socksAddr string, timeout time.Duration) *http.Client {
	t.Helper()
	dialer, err := proxy.SOCKS5("tcp", socksAddr, socksAuth(), proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Dial: dialer.Dial,
		},
		Timeout: timeout,
	}
}

func hasRegisteredTunnel(items []string, tunnel string) bool {
	for _, item := range items {
		if item == tunnel || strings.HasPrefix(item, tunnel+" ") {
			return true
		}
	}
	return false
}

func requireWGTunnelRegistered(t *testing.T) {
	t.Helper()
	if hasRegisteredTunnel(core.GetRegisteredDialers(), core.WireGuardTunnel) &&
		hasRegisteredTunnel(core.GetRegisteredListeners(), core.WireGuardTunnel) {
		return
	}
	t.Skip("wireguard tunnel is not linked into this test binary; run the WireGuard suite with the Go 1.20/build.sh configuration")
}

func buildTestWGURL(host string, port int, q url.Values) string {
	u := &url.URL{
		Scheme: "wireguard",
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   "/",
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func buildTestWGServerURL(port int, privateKey string, peerPublicKeys []string) string {
	q := url.Values{}
	q.Set("private_key", privateKey)
	q.Set("wrapper", "raw")
	if len(peerPublicKeys) > 0 {
		q.Set("peer_public_key", strings.Join(peerPublicKeys, ","))
	}
	return buildTestWGURL("0.0.0.0", port, q)
}

func buildTestWGClientURL(port int, privateKey, peerPublicKey string) string {
	q := url.Values{}
	q.Set("private_key", privateKey)
	q.Set("peer_public_key", peerPublicKey)
	q.Set("wrapper", "raw")
	return buildTestWGURL("127.0.0.1", port, q)
}

func httpGetEventually(t *testing.T, httpClient *http.Client, targetURL string, timeout time.Duration) (*http.Response, []byte) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		resp, err := httpClient.Get(targetURL)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil {
				return resp, body
			}
			lastErr = readErr
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP GET %s did not succeed within %v: %v", targetURL, timeout, lastErr)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// ---------- E2E tests ----------

func TestE2E_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello rem"))
	}))
	defer ts.Close()

	socksAddr := setupE2E(t)
	httpClient := newSOCKS5Client(t, socksAddr, 10*time.Second)

	resp, err := httpClient.Get(ts.URL)
	if err != nil {
		t.Fatalf("HTTP GET through SOCKS5: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello rem" {
		t.Fatalf("expected %q, got %q", "hello rem", body)
	}
}

func TestE2E_SOCKS5_LargeTransfer(t *testing.T) {
	const dataSize = 1 << 20
	data := make([]byte, dataSize)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	expectedHash := sha256.Sum256(data)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", dataSize))
		w.Write(data)
	}))
	defer ts.Close()

	socksAddr := setupE2E(t)
	httpClient := newSOCKS5Client(t, socksAddr, 30*time.Second)

	resp, err := httpClient.Get(ts.URL)
	if err != nil {
		t.Fatalf("HTTP GET: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != dataSize {
		t.Fatalf("expected %d bytes, got %d", dataSize, len(body))
	}
	actualHash := sha256.Sum256(body)
	if actualHash != expectedHash {
		t.Fatal("SHA256 mismatch on 1MB transfer")
	}
}

func TestE2E_SOCKS5_ConcurrentRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("concurrent-ok"))
	}))
	defer ts.Close()

	socksAddr := setupE2E(t)
	httpClient := newSOCKS5Client(t, socksAddr, 10*time.Second)

	const numRequests = 10
	var wg sync.WaitGroup
	errs := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := httpClient.Get(ts.URL)
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
			if string(body) != "concurrent-ok" {
				errs <- fmt.Errorf("request %d: expected %q, got %q", idx, "concurrent-ok", body)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// ---------- Memory Bridge E2E ----------

// TestMemoryBridgeProcess is a subprocess helper that:
//  1. Connects to a rem server as a proxy client with a memory+socks5 listener
//  2. Dials an echo server through the memory bridge (proxyclient → memory → SOCKS5 → target)
//  3. Verifies echo roundtrip
//
// This exercises the exact same path as MemoryDial in the TinyGo WASM export.
// The key insight is that memory bridge requires proxy mode (not reverse), so that:
//   - CLIENT has Inbound (reads SOCKS5 from memory pipe, relays through bridge)
//   - SERVER has Outbound (reads from bridge, dials target)
func TestMemoryBridgeProcess(t *testing.T) {
	if os.Getenv("REM_MEMORY_BRIDGE") == "" {
		t.Skip("subprocess helper – skipped in normal test runs")
	}
	serverAddr := os.Getenv("REM_SERVER_ADDR")
	echoAddr := os.Getenv("REM_ECHO_ADDR")
	if serverAddr == "" || echoAddr == "" {
		t.Fatal("REM_SERVER_ADDR and REM_ECHO_ADDR must be set")
	}

	memoryPipe := "memory"

	// 1. Connect as proxy client with memory+socks5 listener
	// Same pattern as newRemProxyClient in console_std.go:
	//   -c <server> -m proxy -l memory+socks5://:@<memhandle>
	cmd := fmt.Sprintf("-c tcp://%s/?wrapper=raw -m proxy -l memory+socks5://:@%s --debug", serverAddr, memoryPipe)
	console, err := NewConsoleWithCMD(cmd)
	if err != nil {
		t.Fatalf("NewConsoleWithCMD: %v", err)
	}

	a, err := console.Dial(console.ConsoleURL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	// 2. Run Handler in goroutine (HandlerInit + message loop)
	go func() {
		if err := a.Handler(); err != nil {
			t.Logf("Handler error: %v", err)
		}
	}()

	// Wait for agent to be fully initialized
	deadline := time.Now().Add(10 * time.Second)
	for !a.Init && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !a.Init {
		t.Fatal("agent did not initialize within timeout")
	}

	// 3. Memory dial through proxyclient (same path as MemoryDial in export)
	memURL := &url.URL{Scheme: "memory", Host: memoryPipe}
	memClient, err := proxyclient.NewClient(memURL)
	if err != nil {
		t.Fatalf("proxyclient.NewClient: %v", err)
	}

	conn, err := memClient(context.Background(), "tcp", echoAddr)
	if err != nil {
		t.Fatalf("memory dial to %s: %v", echoAddr, err)
	}
	defer conn.Close()

	// 4. Echo roundtrip
	msg := []byte("hello memory bridge e2e")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, len(msg))
	if tc, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		tc.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: expected %q, got %q", msg, buf)
	}

	t.Log("memory bridge e2e: PASSED")
}

// TestE2E_MemoryBridge tests the full memory bridge path with a real rem server:
//
//	proxyclient.NewClient(memory://memory) → MemoryDialer → net.Pipe
//	  → CLIENT Inbound.Relay (reads SOCKS5 from pipe, relays through bridge)
//	  → SERVER Outbound.Handle (reads from bridge, dials target via NetDialer)
//	  → echo server
//
// This is the exact path used by malefic-core's REMClient and MemoryDial in WASM export.
func TestE2E_MemoryBridge(t *testing.T) {
	// 1. Start echo TCP server
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

	// 2. Start rem server subprocess
	serverPort := freePort(t)
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
	t.Cleanup(func() {
		serverProc.Process.Kill()
		serverProc.Wait()
	})

	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	waitForTCP(t, serverAddr, 10*time.Second)

	// 3. Start memory bridge subprocess (proxy client + memory dial + echo test)
	bridgeProc := exec.Command(os.Args[0], "-test.run=^TestMemoryBridgeProcess$", "-test.v", "-test.timeout=60s")
	bridgeProc.Env = append(os.Environ(),
		"REM_MEMORY_BRIDGE=1",
		fmt.Sprintf("REM_SERVER_ADDR=%s", serverAddr),
		fmt.Sprintf("REM_ECHO_ADDR=%s", echoAddr),
	)
	bridgeProc.Stdout = os.Stdout
	bridgeProc.Stderr = os.Stderr
	if err := bridgeProc.Start(); err != nil {
		t.Fatalf("start memory bridge subprocess: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- bridgeProc.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("memory bridge subprocess failed: %v", err)
		}
	case <-time.After(45 * time.Second):
		bridgeProc.Process.Kill()
		t.Fatal("memory bridge subprocess timed out")
	}
}

// ---------- WireGuard E2E ----------

// freeUDPPort finds an available UDP port on 127.0.0.1.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	a, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	l, err := net.ListenUDP("udp", a)
	if err != nil {
		t.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	return port
}

// genTestWGKeys generates a WireGuard key pair (hex-encoded private, public).
func genTestWGKeys(t *testing.T) (privHex, pubHex string) {
	t.Helper()
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PublicKey()
	return hex.EncodeToString(priv[:]), hex.EncodeToString(pub[:])
}

// setupWGE2E launches a WireGuard-based REM server and client as subprocesses
// with pre-configured keys, waits for the SOCKS5 listener, and returns its address.
func setupWGE2E(t *testing.T) string {
	t.Helper()
	requireWGTunnelRegistered(t)
	wgPort := freeUDPPort(t)
	socksPort := freePort(t)

	srvPriv, srvPub := genTestWGKeys(t)
	cliPriv, cliPub := genTestWGKeys(t)

	// --- server subprocess ---
	serverCmd := fmt.Sprintf("--debug -s %s -i 127.0.0.1 --no-sub",
		buildTestWGServerURL(wgPort, srvPriv, []string{cliPub}))
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=%s", serverCmd),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start WG server subprocess: %v", err)
	}
	t.Cleanup(func() {
		serverProc.Process.Kill()
		serverProc.Wait()
	})

	// WG is UDP — can't use waitForTCP to probe server readiness.
	time.Sleep(2 * time.Second)

	// --- client subprocess ---
	clientCmd := fmt.Sprintf("-c %s -m proxy -l socks5://127.0.0.1:%d --debug",
		buildTestWGClientURL(wgPort, cliPriv, srvPub), socksPort)
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	clientProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=%s", clientCmd),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start WG client subprocess: %v", err)
	}
	t.Cleanup(func() {
		clientProc.Process.Kill()
		clientProc.Wait()
	})

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 30*time.Second)
	time.Sleep(500 * time.Millisecond)
	return socksAddr
}

// setupWGMultiClientE2E launches a WG server with two pre-configured peers
// and two client subprocesses, each with its own SOCKS5 port.
func setupWGMultiClientE2E(t *testing.T) (socksAddr1, socksAddr2 string) {
	t.Helper()
	requireWGTunnelRegistered(t)
	wgPort := freeUDPPort(t)
	socksPort1 := freePort(t)
	socksPort2 := freePort(t)
	alias1 := fmt.Sprintf("wgmc1_%d", atomic.AddUint32(&testCounter, 1))
	alias2 := fmt.Sprintf("wgmc2_%d", atomic.AddUint32(&testCounter, 1))

	srvPriv, srvPub := genTestWGKeys(t)
	cli1Priv, cli1Pub := genTestWGKeys(t)
	cli2Priv, cli2Pub := genTestWGKeys(t)

	// --- server subprocess (two peers) ---
	serverCmd := fmt.Sprintf("--debug -s %s -i 127.0.0.1 --no-sub",
		buildTestWGServerURL(wgPort, srvPriv, []string{cli1Pub, cli2Pub}))
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=%s", serverCmd),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start WG server subprocess: %v", err)
	}
	t.Cleanup(func() {
		serverProc.Process.Kill()
		serverProc.Wait()
	})

	time.Sleep(2 * time.Second)

	// --- client 1 subprocess ---
	cli1Cmd := fmt.Sprintf("-c %s -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
		buildTestWGClientURL(wgPort, cli1Priv, srvPub), socksPort1, alias1)
	cli1Proc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	cli1Proc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=%s", cli1Cmd),
	)
	cli1Proc.Stdout = os.Stdout
	cli1Proc.Stderr = os.Stderr
	if err := cli1Proc.Start(); err != nil {
		t.Fatalf("start WG client1 subprocess: %v", err)
	}
	t.Cleanup(func() {
		cli1Proc.Process.Kill()
		cli1Proc.Wait()
	})

	// --- client 2 subprocess ---
	cli2Cmd := fmt.Sprintf("-c %s -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
		buildTestWGClientURL(wgPort, cli2Priv, srvPub), socksPort2, alias2)
	cli2Proc := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	cli2Proc.Env = append(os.Environ(),
		"REM_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=%s", cli2Cmd),
	)
	cli2Proc.Stdout = os.Stdout
	cli2Proc.Stderr = os.Stderr
	if err := cli2Proc.Start(); err != nil {
		t.Fatalf("start WG client2 subprocess: %v", err)
	}
	t.Cleanup(func() {
		cli2Proc.Process.Kill()
		cli2Proc.Wait()
	})

	socksAddr1 = fmt.Sprintf("127.0.0.1:%d", socksPort1)
	socksAddr2 = fmt.Sprintf("127.0.0.1:%d", socksPort2)
	waitForTCP(t, socksAddr1, 30*time.Second)
	waitForTCP(t, socksAddr2, 30*time.Second)
	time.Sleep(500 * time.Millisecond)
	return socksAddr1, socksAddr2
}

// TestE2E_WG_SOCKS5Proxy verifies basic HTTP through WG tunnel SOCKS5.
func TestE2E_WG_SOCKS5Proxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello wg"))
	}))
	defer ts.Close()

	socksAddr := setupWGE2E(t)
	httpClient := newSOCKS5Client(t, socksAddr, 3*time.Second)

	_, body := httpGetEventually(t, httpClient, ts.URL, 15*time.Second)
	if string(body) != "hello wg" {
		t.Fatalf("expected %q, got %q", "hello wg", body)
	}
	t.Logf("WG SOCKS5 proxy OK")
}

// TestE2E_TCP_Stability runs a 10-minute stability test that sends an HTTP GET
// to baidu.com every 10 seconds through SOCKS5→TCP tunnel, verifying the full
// data path remains healthy over time. Requires REM_STABILITY=1 to run.
func TestE2E_TCP_Stability(t *testing.T) {
	if os.Getenv("REM_STABILITY") == "" {
		t.Skip("set REM_STABILITY=1 to run 10-min stability test")
	}

	const (
		testDuration  = 10 * time.Minute
		probeInterval = 10 * time.Second
		httpTimeout   = 15 * time.Second
		targetURL     = "http://www.baidu.com"
	)

	socksAddr := setupE2E(t)

	deadline := time.Now().Add(testDuration)
	total, success, fail := 0, 0, 0
	maxConsecFails := 0
	consecFails := 0
	startTime := time.Now()

	for time.Now().Before(deadline) {
		total++
		elapsed := time.Since(startTime).Truncate(time.Second)

		httpClient := newSOCKS5Client(t, socksAddr, httpTimeout)
		resp, err := httpClient.Get(targetURL)
		if err != nil {
			fail++
			consecFails++
			if consecFails > maxConsecFails {
				maxConsecFails = consecFails
			}
			t.Logf("[%v] probe %d FAIL: %v (consec=%d)", elapsed, total, err, consecFails)
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && len(body) > 0 {
				success++
				consecFails = 0
			} else {
				fail++
				consecFails++
				if consecFails > maxConsecFails {
					maxConsecFails = consecFails
				}
				t.Logf("[%v] probe %d FAIL: status=%d bodyLen=%d", elapsed, total, resp.StatusCode, len(body))
			}
		}
		httpClient.CloseIdleConnections()

		// Periodic summary every 10 probes
		if total%10 == 0 {
			rate := float64(success) / float64(total) * 100
			t.Logf("[%v] === %d probes: %d ok, %d fail (%.1f%% success) ===",
				elapsed, total, success, fail, rate)
		}

		if consecFails >= 5 {
			t.Fatalf("5 consecutive failures at probe %d — tunnel likely dead", total)
		}

		time.Sleep(probeInterval)
	}

	rate := float64(success) / float64(total) * 100
	t.Logf("========== STABILITY TEST COMPLETE ==========")
	t.Logf("Duration:           %v", time.Since(startTime).Truncate(time.Second))
	t.Logf("Total probes:       %d", total)
	t.Logf("Success:            %d (%.1f%%)", success, rate)
	t.Logf("Fail:               %d", fail)
	t.Logf("Max consec. fails:  %d", maxConsecFails)

	if rate < 95.0 {
		t.Fatalf("success rate %.1f%% below 95%% threshold", rate)
	}
}

// TestE2E_WG_MultiClient verifies two clients independently access an HTTP service,
// including concurrent requests to ensure they don't interfere with each other.
func TestE2E_WG_MultiClient(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("multi-client-ok"))
	}))
	defer ts.Close()

	socksAddr1, socksAddr2 := setupWGMultiClientE2E(t)

	// Basic connectivity check for each client
	for i, addr := range []string{socksAddr1, socksAddr2} {
		httpClient := newSOCKS5Client(t, addr, 3*time.Second)
		_, body := httpGetEventually(t, httpClient, ts.URL, 15*time.Second)
		if string(body) != "multi-client-ok" {
			t.Fatalf("client%d: expected %q, got %q", i+1, "multi-client-ok", body)
		}
	}

	// Concurrent requests from both clients simultaneously
	const numPerClient = 3
	var wg sync.WaitGroup
	errs := make(chan error, numPerClient*2)

	for ci, addr := range []string{socksAddr1, socksAddr2} {
		httpClient := newSOCKS5Client(t, addr, 15*time.Second)
		for i := 0; i < numPerClient; i++ {
			wg.Add(1)
			go func(clientIdx, reqIdx int) {
				defer wg.Done()
				resp, err := httpClient.Get(ts.URL)
				if err != nil {
					errs <- fmt.Errorf("client%d req%d: %w", clientIdx+1, reqIdx, err)
					return
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					errs <- fmt.Errorf("client%d req%d read: %w", clientIdx+1, reqIdx, err)
					return
				}
				if string(body) != "multi-client-ok" {
					errs <- fmt.Errorf("client%d req%d: expected %q, got %q", clientIdx+1, reqIdx, "multi-client-ok", body)
				}
			}(ci, i)
		}
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
