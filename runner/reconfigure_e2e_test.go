//go:build !tinygo

package runner

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
)

// mockTransportAddr implements net.Addr + configurable (SetOption) for the
// client subprocess. It records all received options so we can write them
// to a report file for the parent test to verify.
type mockTransportAddr struct {
	mu      sync.Mutex
	options map[string]string
}

func (a *mockTransportAddr) Network() string { return "mock" }
func (a *mockTransportAddr) String() string  { return "mock://reconfigure-test" }
func (a *mockTransportAddr) SetOption(key, value string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.options == nil {
		a.options = make(map[string]string)
	}
	a.options[key] = value
}
func (a *mockTransportAddr) snapshot() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make(map[string]string, len(a.options))
	for k, v := range a.options {
		cp[k] = v
	}
	return cp
}

// ---------- Server subprocess ----------

// TestHelperReconfigServer is a subprocess that:
//  1. Runs a TCP server
//  2. Waits for a client agent to connect
//  3. Sends a Reconfigure message with test options
//  4. Blocks forever (parent kills on cleanup)
func TestHelperReconfigServer(t *testing.T) {
	if os.Getenv("REM_RECONFIG_SERVER") == "" {
		t.Skip("subprocess helper")
	}
	utils.Log = logs.NewLogger(logs.DebugLevel)
	cmd := os.Getenv("REM_HELPER_CMD")
	expectedAlias := os.Getenv("REM_EXPECTED_ALIAS")

	console, err := NewConsoleWithCMD(cmd)
	if err != nil {
		t.Fatalf("NewConsoleWithCMD: %v", err)
	}

	// Run server in background goroutine (blocks in accept loop)
	go console.Run()

	// Poll until the expected client agent appears
	var target *agent.Agent
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if a, ok := agent.Agents.Get(expectedAlias); ok && a.Init {
			target = a
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if target == nil {
		t.Fatalf("client agent %q never connected", expectedAlias)
	}

	t.Logf("client agent %q connected, sending Reconfigure", expectedAlias)

	// Send Reconfigure with test options
	err = target.Send(&message.Reconfigure{
		Options: map[string]string{
			"interval": "7777",
			"custom":   "hello_from_server",
		},
	})
	if err != nil {
		t.Fatalf("Send Reconfigure: %v", err)
	}
	t.Logf("Reconfigure sent")

	// Block forever — parent test kills the process on cleanup
	select {}
}

// ---------- Client subprocess ----------

// TestHelperReconfigClient is a subprocess that:
//  1. Dials the server
//  2. Sets a mock TransportAddr
//  3. Runs Handler() and waits for Reconfigure to arrive
//  4. Writes received options to a report file
func TestHelperReconfigClient(t *testing.T) {
	if os.Getenv("REM_RECONFIG_CLIENT") == "" {
		t.Skip("subprocess helper")
	}
	utils.Log = logs.NewLogger(logs.DebugLevel)
	cmd := os.Getenv("REM_HELPER_CMD")
	reportPath := os.Getenv("REM_REPORT_PATH")

	console, err := NewConsoleWithCMD(cmd)
	if err != nil {
		t.Fatalf("NewConsoleWithCMD: %v", err)
	}

	a, err := console.Dial(console.ConsoleURL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	// Inject mock TransportAddr that records SetOption calls
	mockAddr := &mockTransportAddr{}
	a.TransportAddr = mockAddr

	// Run Handler in background (blocks in message loop)
	handlerDone := make(chan error, 1)
	go func() {
		handlerDone <- a.Handler()
	}()

	// Wait for Reconfigure to arrive (poll mockAddr for expected key)
	deadline := time.Now().Add(15 * time.Second)
	received := false
	for time.Now().Before(deadline) {
		snap := mockAddr.snapshot()
		if snap["interval"] != "" {
			received = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !received {
		t.Fatalf("Reconfigure not received within timeout")
	}

	// Write report
	snap := mockAddr.snapshot()
	data, _ := json.Marshal(snap)
	if err := os.WriteFile(reportPath, data, 0644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("report written: %s", string(data))
}

// ---------- Parent test ----------

// TestE2E_Reconfigure verifies the full server→client Reconfigure flow:
//  1. Server subprocess accepts client connection
//  2. Server sends Reconfigure{interval: "7777", custom: "hello_from_server"}
//  3. Client subprocess receives it via handleReconfigure → TransportAddr.SetOption
//  4. Client writes received options to a report file
//  5. Parent verifies report contents
func TestE2E_Reconfigure(t *testing.T) {
	serverPort := freePort(t)
	alias := fmt.Sprintf("reconfig%d", atomic.AddUint32(&testCounter, 1))
	reportPath := fmt.Sprintf("%s/reconfigure_report.json", t.TempDir())

	// --- server subprocess ---
	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperReconfigServer$", "-test.v", "-test.timeout=60s")
	serverProc.Env = append(os.Environ(),
		"REM_RECONFIG_SERVER=1",
		fmt.Sprintf("REM_HELPER_CMD=--debug -s tcp://0.0.0.0:%d/?wrapper=raw -i 127.0.0.1 --no-sub", serverPort),
		fmt.Sprintf("REM_EXPECTED_ALIAS=%s", alias),
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

	// --- client subprocess ---
	clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperReconfigClient$", "-test.v", "-test.timeout=60s")
	clientProc.Env = append(os.Environ(),
		"REM_RECONFIG_CLIENT=1",
		fmt.Sprintf("REM_HELPER_CMD=-c tcp://%s/?wrapper=raw -m proxy -l socks5://127.0.0.1:0 -a %s --debug",
			serverAddr, alias),
		fmt.Sprintf("REM_REPORT_PATH=%s", reportPath),
	)
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr
	if err := clientProc.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}

	// Wait for client to finish (it exits after receiving Reconfigure and writing report)
	done := make(chan error, 1)
	go func() { done <- clientProc.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("client subprocess failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		clientProc.Process.Kill()
		t.Fatal("client subprocess timed out")
	}

	// --- verify report ---
	reportData, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}

	var received map[string]string
	if err := json.Unmarshal(reportData, &received); err != nil {
		t.Fatalf("parse report: %v", err)
	}

	t.Logf("received options: %v", received)

	if received["interval"] != "7777" {
		t.Fatalf("interval = %q, want '7777'", received["interval"])
	}
	if received["custom"] != "hello_from_server" {
		t.Fatalf("custom = %q, want 'hello_from_server'", received["custom"])
	}

	// Suppress unused import warnings
	_ = net.Addr(nil)
}
