//go:build !tinygo

package runner

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/rem/agent"
	"golang.org/x/net/proxy"
)

// TestHelperKeepaliveProcess is a subprocess helper that configures short
// keepalive intervals for fast liveness detection in tests.
func TestHelperKeepaliveProcess(t *testing.T) {
	if os.Getenv("REM_KEEPALIVE_HELPER") == "" {
		t.Skip("subprocess helper")
	}
	// Short keepalive: 2s interval, 3 missed = 6s detection
	agent.SetKeepaliveConfig(2*time.Second, 3)

	cmd := os.Getenv("REM_HELPER_CMD")
	if cmd == "" {
		t.Fatal("REM_HELPER_CMD not set")
	}
	console, err := NewConsoleWithCMD(cmd)
	if err != nil {
		t.Fatalf("NewConsoleWithCMD: %v", err)
	}
	console.Run()
}

// startKeepaliveProc starts a subprocess with short keepalive config.
func startKeepaliveProc(t *testing.T, cmd string) *exec.Cmd {
	t.Helper()
	proc := exec.Command(os.Args[0], "-test.run=^TestHelperKeepaliveProcess$", "-test.v", "-test.timeout=120s")
	proc.Env = append(os.Environ(),
		"REM_KEEPALIVE_HELPER=1",
		fmt.Sprintf("REM_HELPER_CMD=%s", cmd),
	)
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	if err := proc.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	return proc
}

func newKeepaliveSOCKS5Client(socksAddr string, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Dial: func(network, addr string) (net.Conn, error) {
				dialer, err := proxy.SOCKS5("tcp", socksAddr, socksAuth(), proxy.Direct)
				if err != nil {
					return nil, err
				}
				return dialer.Dial(network, addr)
			},
		},
		Timeout: timeout,
	}
}

// TestE2E_Keepalive_ServerKill_ClientRecovers verifies the full death-detection
// and auto-reconnect path:
//
//  1. Start server1 + client (short keepalive: 2s interval, 3 missed)
//  2. Verify HTTP through SOCKS5 works (baseline)
//  3. Kill server1 — client's keepalive detects death within ~6s
//  4. Start server2 on the same port
//  5. Client auto-reconnects via Console.Run() infinite retry loop
//  6. Verify HTTP works again through the same SOCKS5 port
func TestE2E_Keepalive_ServerKill_ClientRecovers(t *testing.T) {
	// Echo HTTP server (target)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("keepalive-ok"))
	}))
	defer ts.Close()

	serverPort := freePort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("ka%d", atomic.AddUint32(&testCounter, 1))
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)

	serverCmd := fmt.Sprintf("--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort)
	clientCmd := fmt.Sprintf("-c tcp://%s/?wrapper=raw -m proxy -l socks5://127.0.0.1:%d -a %s --debug",
		serverAddr, socksPort, alias)

	// ── Phase 1: Start server1 + client ──
	t.Log("Phase 1: starting server1 + client")
	server1 := startKeepaliveProc(t, serverCmd)
	t.Cleanup(func() { server1.Process.Kill(); server1.Wait() })
	waitForTCP(t, serverAddr, 10*time.Second)

	client := startKeepaliveProc(t, clientCmd)
	t.Cleanup(func() { client.Process.Kill(); client.Wait() })
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(500 * time.Millisecond)

	// ── Phase 2: Baseline ──
	t.Log("Phase 2: baseline HTTP check")
	httpClient := newKeepaliveSOCKS5Client(socksAddr, 10*time.Second)
	resp, err := httpClient.Get(ts.URL)
	if err != nil {
		t.Fatalf("baseline HTTP: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "keepalive-ok" {
		t.Fatalf("baseline body = %q, want 'keepalive-ok'", body)
	}
	t.Log("Phase 2: PASS")

	// ── Phase 3: Kill server1 ──
	t.Logf("Phase 3: killing server1 at %v", time.Now().Format("15:04:05"))
	server1.Process.Kill()
	server1.Wait()

	// Client keepalive: 2s × 3 = ~6s detection, then retry backoff starts.
	// Wait enough for detection + one retry attempt.
	t.Log("Phase 3: waiting for keepalive to detect server death (~6-10s)...")
	time.Sleep(10 * time.Second)

	// ── Phase 4: Start server2 on same port ──
	t.Logf("Phase 4: starting server2 at %v", time.Now().Format("15:04:05"))
	server2 := startKeepaliveProc(t, serverCmd)
	t.Cleanup(func() { server2.Process.Kill(); server2.Wait() })
	waitForTCP(t, serverAddr, 10*time.Second)

	// ── Phase 5: Wait for auto-reconnect and verify ──
	t.Log("Phase 5: waiting for client to auto-reconnect...")
	deadline := time.Now().Add(30 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		httpClient2 := newKeepaliveSOCKS5Client(socksAddr, 5*time.Second)
		resp, err := httpClient2.Get(ts.URL)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) == "keepalive-ok" {
			recovered = true
			break
		}
		time.Sleep(1 * time.Second)
	}

	if !recovered {
		t.Fatal("Phase 5: FAIL — client never recovered after server restart")
	}
	t.Logf("Phase 5: PASS — client recovered at %v", time.Now().Format("15:04:05"))
}
