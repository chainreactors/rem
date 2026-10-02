//go:build !tinygo

package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/harness/runner"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	_ "github.com/chainreactors/rem/protocol/serve/http"
	_ "github.com/chainreactors/rem/protocol/serve/portforward"
	_ "github.com/chainreactors/rem/protocol/serve/raw"
	_ "github.com/chainreactors/rem/protocol/serve/socks"
	"github.com/chainreactors/rem/protocol/tunnel"
	_ "github.com/chainreactors/rem/protocol/tunnel/http"
	_ "github.com/chainreactors/rem/protocol/tunnel/http2"
	_ "github.com/chainreactors/rem/protocol/tunnel/icmp"
	_ "github.com/chainreactors/rem/protocol/tunnel/memory"
	_ "github.com/chainreactors/rem/protocol/tunnel/streamhttp"
	_ "github.com/chainreactors/rem/protocol/tunnel/tcp"
	_ "github.com/chainreactors/rem/protocol/tunnel/udp"
	_ "github.com/chainreactors/rem/protocol/tunnel/unix"
	_ "github.com/chainreactors/rem/protocol/tunnel/websocket"
	_ "github.com/chainreactors/rem/protocol/wrapper"
	"github.com/chainreactors/rem/x/cryptor"
	xsocks5 "github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
	"github.com/kballard/go-shellquote"
	"golang.org/x/net/proxy"
)

// ═══════════════════════════════════════════════════════════════════════════
// Helpers & shared infrastructure
// ═══════════════════════════════════════════════════════════════════════════

const (
	e2eHelperTestPattern = "^TestE2EHelperProcess$"
	e2eSocksUser         = "remno1"
	e2eSocksPassword     = "0onmer"
)

var testCounter uint32

func init() {
	utils.Log = logs.NewLogger(logs.DebugLevel)
}

func TestE2EHelperProcess(t *testing.T) {
	mode := os.Getenv("REM_E2E_MODE")
	if mode == "" {
		t.Skip("subprocess helper")
	}

	switch mode {
	case "console_run":
		runConsoleHelper(t)
	case "runner_run":
		runRunnerHelper(t)
	case "keepalive_console":
		agent.SetKeepaliveConfig(2*time.Second, 3)
		runConsoleHelper(t)
	case "reconfigure_server":
		runReconfigureServerHelper(t)
	case "reconfigure_client_report":
		runReconfigureClientReportHelper(t)
	case "connhub_client_report":
		runConnHubClientReportHelper(t)
	case "memory_bridge_client":
		runMemoryBridgeClientHelper(t)
	case "duplex_server":
		runDuplexServerHelper(t)
	case "duplex_client":
		runDuplexClientHelper(t)
	default:
		t.Fatalf("unknown REM_E2E_MODE %q", mode)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("REM_HELPER") == "" {
		t.Skip("subprocess helper")
	}
	runConsoleHelper(t)
}

func runConsoleHelper(t *testing.T) {
	cmd := os.Getenv("REM_HELPER_CMD")
	if cmd == "" {
		t.Fatal("REM_HELPER_CMD not set")
	}
	console, err := NewConsoleWithCMD(cmd)
	if err != nil {
		t.Fatalf("NewConsoleWithCMD: %v", err)
	}
	if err := console.Run(); err != nil {
		t.Fatalf("console.Run: %v", err)
	}
}

func runRunnerHelper(t *testing.T) {
	cmdline := os.Getenv("REM_HELPER_CMD")
	if cmdline == "" {
		t.Fatal("REM_HELPER_CMD not set")
	}
	args, err := shellquote.Split(cmdline)
	if err != nil {
		t.Fatalf("shellquote.Split: %v", err)
	}
	var opt Options
	if err := opt.ParseArgs(args); err != nil {
		t.Fatalf("Options.ParseArgs: %v", err)
	}
	r, err := opt.Prepare()
	if err != nil {
		t.Fatalf("Options.Prepare: %v", err)
	}
	if err := r.Run(); err != nil {
		t.Fatalf("Runner.Run: %v", err)
	}
}

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
		a.options = map[string]string{}
	}
	a.options[key] = value
}

func (a *mockTransportAddr) snapshot() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make(map[string]string, len(a.options))
	for key, value := range a.options {
		cp[key] = value
	}
	return cp
}

func runReconfigureServerHelper(t *testing.T) {
	cmd := os.Getenv("REM_HELPER_CMD")
	expectedAlias := os.Getenv("REM_EXPECTED_ALIAS")
	console, err := NewConsoleWithCMD(cmd)
	if err != nil {
		t.Fatalf("NewConsoleWithCMD: %v", err)
	}
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- console.Run()
	}()

	var target *agent.Agent
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-runErrCh:
			t.Fatalf("console.Run exited before agent connected: %v", err)
		default:
		}
		if a, ok := agent.Agents.Get(expectedAlias); ok && a.Init {
			target = a
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if target == nil {
		t.Fatalf("client agent %q never connected", expectedAlias)
	}

	if err := target.Send(&message.Reconfigure{
		Options: map[string]string{
			"interval": "7777",
			"custom":   "hello_from_server",
		},
	}); err != nil {
		t.Fatalf("Send Reconfigure: %v", err)
	}

	select {}
}

func runReconfigureClientReportHelper(t *testing.T) {
	cmd := os.Getenv("REM_HELPER_CMD")
	reportPath := os.Getenv("REM_REPORT_PATH")
	console, err := NewConsoleWithCMD(cmd)
	if err != nil {
		t.Fatalf("NewConsoleWithCMD: %v", err)
	}
	a, err := console.Dial(console.ConsoleURL)
	if err != nil {
		t.Fatalf("console.Dial: %v", err)
	}

	mockAddr := &mockTransportAddr{}
	a.TransportAddr = mockAddr

	done := make(chan error, 1)
	go func() {
		done <- a.Handler()
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if mockAddr.snapshot()["interval"] != "" {
			raw, err := json.Marshal(mockAddr.snapshot())
			if err != nil {
				t.Fatalf("marshal report: %v", err)
			}
			if err := os.WriteFile(reportPath, raw, 0644); err != nil {
				t.Fatalf("write report: %v", err)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handler exited before reconfigure: %v", err)
		}
	default:
	}
	t.Fatal("Reconfigure not received within timeout")
}

type connHubPerfReport struct {
	LoadBalance string                  `json:"load_balance"`
	Requests    int                     `json:"requests"`
	DurationMS  int64                   `json:"duration_ms"`
	Stats       []agent.ConnHubConnStat `json:"stats"`
}

func runConnHubClientReportHelper(t *testing.T) {
	tcpAddr := os.Getenv("REM_TCP_ADDR")
	udpAddr := os.Getenv("REM_UDP_ADDR")
	wsAddr := os.Getenv("REM_WS_ADDR")
	socksPort := os.Getenv("REM_SOCKS_PORT")
	alias := os.Getenv("REM_ALIAS")
	targetURL := os.Getenv("REM_TARGET_URL")
	reportPath := os.Getenv("REM_REPORT_PATH")
	loadBalance := os.Getenv("REM_LB")
	if loadBalance == "" {
		loadBalance = "round-robin"
	}

	reqCount := 60
	if raw := os.Getenv("REM_REQ_COUNT"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			reqCount = parsed
		}
	}

	var opt Options
	if err := opt.ParseArgs([]string{
		"-c", fmt.Sprintf("tcp://%s/?wrapper=raw", tcpAddr),
		"-c", fmt.Sprintf("udp://%s/?wrapper=raw", udpAddr),
		"-c", fmt.Sprintf("ws://%s/hub?wrapper=raw", wsAddr),
		"-m", "proxy",
		"-l", fmt.Sprintf("socks5://127.0.0.1:%s", socksPort),
		"-a", alias,
		"--lb", loadBalance,
	}); err != nil {
		t.Fatalf("Options.ParseArgs: %v", err)
	}
	r, err := opt.Prepare()
	if err != nil {
		t.Fatalf("Options.Prepare: %v", err)
	}

	fullURLs, upURLs, downURLs, err := splitConsoleChannels(r.ConsoleURLs)
	if err != nil {
		t.Fatalf("splitConsoleChannels: %v", err)
	}
	if len(fullURLs) == 0 {
		t.Fatal("helper expects at least one full channel")
	}

	console, err := NewConsole(r, r.NewURLs(fullURLs[0]))
	if err != nil {
		t.Fatalf("NewConsole: %v", err)
	}
	a, err := console.Dial(fullURLs[0])
	if err != nil {
		t.Fatalf("console.Dial: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- a.Handler()
	}()
	defer func() {
		a.Close(nil)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}()

	deadline := time.Now().Add(15 * time.Second)
	for !a.Init && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !a.Init {
		t.Fatal("agent not initialized")
	}

	r.attachChannelConns(a, fullURLs, upURLs, downURLs, 0)

	socksAddr := fmt.Sprintf("127.0.0.1:%s", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(300 * time.Millisecond)

	client := newSOCKS5Client(t, socksAddr, 15*time.Second)
	client.Transport.(*http.Transport).DisableKeepAlives = true

	start := time.Now()
	var wg sync.WaitGroup
	errCh := make(chan error, reqCount)
	sem := make(chan struct{}, 10)
	for i := 0; i < reqCount; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(index int) {
			defer wg.Done()
			defer func() { <-sem }()
			for attempt := 1; attempt <= 3; attempt++ {
				resp, err := client.Get(targetURL)
				if err != nil {
					if attempt == 3 {
						errCh <- fmt.Errorf("request %d failed: %w", index, err)
						return
					}
					time.Sleep(100 * time.Millisecond)
					continue
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					if attempt == 3 {
						errCh <- fmt.Errorf("request %d read failed: %w", index, err)
						return
					}
					time.Sleep(100 * time.Millisecond)
					continue
				}
				if string(body) != "hub-ok" {
					errCh <- fmt.Errorf("request %d unexpected body: %q", index, body)
				}
				return
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	raw, err := json.MarshalIndent(connHubPerfReport{
		LoadBalance: loadBalance,
		Requests:    reqCount,
		DurationMS:  time.Since(start).Milliseconds(),
		Stats:       a.ConnHubStats(),
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(reportPath, raw, 0644); err != nil {
		t.Fatalf("write report: %v", err)
	}
}

func runMemoryBridgeClientHelper(t *testing.T) {
	serverAddr := os.Getenv("REM_SERVER_ADDR")
	echoAddr := os.Getenv("REM_ECHO_ADDR")
	if serverAddr == "" || echoAddr == "" {
		t.Fatal("REM_SERVER_ADDR and REM_ECHO_ADDR must be set")
	}

	memoryPipe := "memory"
	console, err := NewConsoleWithCMD(fmt.Sprintf("-c tcp://%s/?wrapper=raw -l memory+socks5://:@%s --debug", serverAddr, memoryPipe))
	if err != nil {
		t.Fatalf("NewConsoleWithCMD: %v", err)
	}
	a, err := console.Dial(console.ConsoleURL)
	if err != nil {
		t.Fatalf("console.Dial: %v", err)
	}
	go func() {
		if err := a.Handler(); err != nil {
			t.Logf("Handler error: %v", err)
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !a.Init && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !a.Init {
		t.Fatal("agent did not initialize within timeout")
	}

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

	msg := []byte("hello memory bridge e2e")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if tc, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = tc.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: expected %q, got %q", msg, buf)
	}
}

func makeDuplexToken(key string) string {
	token, _ := cryptor.AesEncrypt([]byte(key), cryptor.PKCS7Padding([]byte(key), 16))
	return hex.EncodeToString(token)
}

func runDuplexServerHelper(t *testing.T) {
	utils.Log.SetLevel(logs.DebugLevel)
	key := core.DefaultKey
	tok := makeDuplexToken(key)
	tcpPort := os.Getenv("REM_TCP_PORT")
	udpPort := os.Getenv("REM_UDP_PORT")

	tcpTun, err := tunnel.NewTunnel(context.Background(), "tcp", true)
	if err != nil {
		t.Fatalf("NewTunnel tcp: %v", err)
	}
	udpTun, err := tunnel.NewTunnel(context.Background(), "udp", true)
	if err != nil {
		t.Fatalf("NewTunnel udp: %v", err)
	}
	if _, err := tcpTun.Listen(fmt.Sprintf("tcp://0.0.0.0:%s", tcpPort)); err != nil {
		t.Fatalf("Listen tcp: %v", err)
	}
	if _, err := udpTun.Listen(fmt.Sprintf("udp://0.0.0.0:%s", udpPort)); err != nil {
		t.Fatalf("Listen udp: %v", err)
	}

	type accepted struct {
		conn  net.Conn
		role  string
		login *message.Login
	}
	ch := make(chan accepted, 2)
	acceptOne := func(tun *tunnel.TunnelService) {
		for {
			conn, err := tun.Accept()
			if err != nil {
				return
			}
			msg, err := cio.ReadAndAssertMsg(conn, message.LoginMsg)
			if err != nil {
				_ = conn.Close()
				continue
			}
			login := msg.(*message.Login)
			if tok != login.Token {
				_ = cio.WriteMsg(conn, &message.Ack{Status: message.StatusFailed})
				_ = conn.Close()
				continue
			}
			_ = cio.WriteMsg(conn, &message.Ack{Status: message.StatusSuccess})
			ch <- accepted{conn: conn, role: login.ChannelRole, login: login}
			return
		}
	}
	go acceptOne(tcpTun)
	go acceptOne(udpTun)

	var upConn, downConn net.Conn
	var login *message.Login
	for i := 0; i < 2; i++ {
		item := <-ch
		if item.role == "up" {
			upConn = item.conn
			login = item.login
			continue
		}
		downConn = item.conn
	}

	dc := mergeHalfConn(upConn, downConn)
	ctrlMsg, err := cio.ReadAndAssertMsg(dc, message.ControlMsg)
	if err != nil {
		t.Fatalf("ReadAndAssertMsg control: %v", err)
	}
	_ = cio.WriteMsg(dc, &message.Ack{Status: message.StatusSuccess})
	ctrl := ctrlMsg.(*message.Control)

	srv, err := agent.NewAgent(&agent.Config{
		Alias: login.Agent, Type: core.SERVER, InboundSide: ctrl.InboundSide,
		Redirect: ctrl.Destination, Controller: ctrl, Params: ctrl.Options,
		URLs: &core.URLs{
			ConsoleURL: login.ConsoleURL(),
			LocalURL:   ctrl.LocalURL(),
			RemoteURL:  ctrl.RemoteURL(),
		},
	})
	if err != nil {
		t.Fatalf("NewAgent server: %v", err)
	}
	srv.Conn = dc
	agent.Agents.Add(srv)
	srv.Handler()
}

func runDuplexClientHelper(t *testing.T) {
	utils.Log.SetLevel(logs.DebugLevel)
	key := core.DefaultKey
	tcpAddr := os.Getenv("REM_TCP_ADDR")
	udpAddr := os.Getenv("REM_UDP_ADDR")
	socksPort := os.Getenv("REM_SOCKS_PORT")
	alias := os.Getenv("REM_ALIAS")

	consoleURL, err := core.NewConsoleURL(fmt.Sprintf("tcp://%s", tcpAddr))
	if err != nil {
		t.Fatalf("NewConsoleURL: %v", err)
	}
	localURL, err := core.NewURL(fmt.Sprintf("socks5://127.0.0.1:%s", socksPort))
	if err != nil {
		t.Fatalf("NewURL local: %v", err)
	}
	remoteURL, err := core.NewURL("raw://")
	if err != nil {
		t.Fatalf("NewURL remote: %v", err)
	}

	a, err := agent.NewAgent(&agent.Config{
		Type: core.CLIENT, InboundSide: core.SideLocal, Alias: alias,
		AuthKey: []byte(key), URLs: &core.URLs{ConsoleURL: consoleURL},
		Params: map[string]string{},
	})
	if err != nil {
		t.Fatalf("NewAgent client: %v", err)
	}

	tok, err := cryptor.AesEncrypt([]byte(key), cryptor.PKCS7Padding([]byte(key), 16))
	if err != nil {
		t.Fatalf("AesEncrypt: %v", err)
	}
	tokenStr := hex.EncodeToString(tok)

	tcpTun, err := tunnel.NewTunnel(context.Background(), "tcp", false)
	if err != nil {
		t.Fatalf("NewTunnel tcp: %v", err)
	}
	udpTun, err := tunnel.NewTunnel(context.Background(), "udp", false)
	if err != nil {
		t.Fatalf("NewTunnel udp: %v", err)
	}

	upConn, err := tcpTun.Dial(tcpAddr)
	if err != nil {
		t.Fatalf("Dial tcp: %v", err)
	}
	if _, err := cio.WriteAndAssertMsg(upConn, &message.Login{
		Agent: a.ID, Token: tokenStr, ChannelRole: "up",
		ConsoleProto: consoleURL.Scheme,
		ConsoleIP:    consoleURL.Hostname(), ConsolePort: consoleURL.IntPort(),
	}); err != nil {
		t.Fatalf("Login up: %v", err)
	}

	downConn, err := udpTun.Dial(udpAddr)
	if err != nil {
		t.Fatalf("Dial udp: %v", err)
	}
	if _, err := cio.WriteAndAssertMsg(downConn, &message.Login{
		Agent: a.ID, Token: tokenStr, ChannelRole: "down",
		ConsoleProto: consoleURL.Scheme,
		ConsoleIP:    consoleURL.Hostname(), ConsolePort: consoleURL.IntPort(),
	}); err != nil {
		t.Fatalf("Login down: %v", err)
	}

	dc := mergeHalfConn(downConn, upConn)
	a.Conn = dc
	a.URLs.RemoteURL = remoteURL
	a.URLs.LocalURL = localURL
	if err := a.Dial(remoteURL, localURL); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	agent.Agents.Add(a)
	a.Handler()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.LocalAddr().(*net.UDPAddr).Port
}

func waitForTCP(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	runner.WaitForTCP(t, addr, timeout)
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	runner.WaitForFile(t, path, timeout)
}

func socksAuth() *proxy.Auth {
	return &proxy.Auth{User: e2eSocksUser, Password: e2eSocksPassword}
}

func newSOCKS5Client(t *testing.T, socksAddr string, timeout time.Duration) *http.Client {
	t.Helper()
	return runner.NewSOCKS5Client(t, socksAddr, e2eSocksUser, e2eSocksPassword, timeout)
}

func newHTTPProxyClient(t *testing.T, proxyAddr string, tlsConfig *tls.Config, timeout time.Duration) *http.Client {
	t.Helper()
	return runner.NewHTTPProxyClient(t, proxyAddr, e2eSocksUser, e2eSocksPassword, tlsConfig, timeout)
}

func httpGetEventually(t *testing.T, httpClient *http.Client, targetURL string, timeout time.Duration) (*http.Response, []byte) {
	t.Helper()
	return runner.HTTPGetEventually(t, httpClient, targetURL, timeout)
}

func verifyHTTP(t *testing.T, socksAddr, targetURL, want string) {
	t.Helper()
	resp, body := httpGetEventually(t, newSOCKS5Client(t, socksAddr, 15*time.Second), targetURL, 15*time.Second)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s via %s: unexpected status %d", targetURL, socksAddr, resp.StatusCode)
	}
	if string(body) != want {
		t.Fatalf("expected %q, got %q", want, string(body))
	}
}

func verifyLargeTransfer(t *testing.T, socksAddr string, dataSize int) {
	t.Helper()
	data := make([]byte, dataSize)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", dataSize))
		_, _ = w.Write(data)
	}))
	defer ts.Close()

	// Large proxy transfers can be materially slower on Windows when the full
	// runner matrix is active, especially for UDP-backed tunnels. Keep the
	// payload and integrity checks strict, but allow a wider end-to-end budget
	// so the test measures correctness rather than scheduler noise.
	client := newSOCKS5Client(t, socksAddr, 60*time.Second)
	resp, err := client.Get(ts.URL)
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

func verifyConcurrent(t *testing.T, socksAddr, targetURL, want string, n int) {
	t.Helper()
	client := newSOCKS5Client(t, socksAddr, 15*time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := client.Get(targetURL)
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

func waitForSOCKSHTTPReady(t *testing.T, socksAddr string, timeout time.Duration) {
	t.Helper()
	runner.WaitForSOCKSHTTP(t, socksAddr, e2eSocksUser, e2eSocksPassword, timeout)
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
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
	return ln.Addr().String()
}

func startLocalhostEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
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

func echoRoundtrip(t *testing.T, addr string, msg []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

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

func startHTTPTarget(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func startHelper(t *testing.T, cmdline string) {
	t.Helper()
	proc := exec.Command(os.Args[0], "-test.run="+e2eHelperTestPattern, "-test.v")
	proc.Env = append(os.Environ(),
		"REM_E2E_MODE=console_run",
		fmt.Sprintf("REM_HELPER_CMD=%s", cmdline),
	)
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	if err := proc.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = proc.Process.Kill()
		_ = proc.Wait()
	})
}

func assertSOCKS5Get(t *testing.T, socksAddr, targetURL string, timeout time.Duration) {
	t.Helper()
	client := newSOCKS5Client(t, socksAddr, timeout)
	resp, body := httpGetEventually(t, client, targetURL, timeout)
	if resp.StatusCode != 200 {
		t.Fatalf("unexpected status: %d, body: %s", resp.StatusCode, string(body))
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

func statsByLabel(stats []agent.ConnHubConnStat) map[string]uint64 {
	selectedByLabel := map[string]uint64{}
	for _, stat := range stats {
		selectedByLabel[stat.Label] += stat.Selected
	}
	return selectedByLabel
}

func reportPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// ── Tunnel ──────────────────────────────────────────────────────────────────

var tunnelMatrixCases []runner.CaseSpec

func init() {
	registerTunnelCases(
		makeProxyTunnelCase("tcp_raw", buildTCPRawFixture, 1<<20, 10),
		makeProxyTunnelCase("udp_raw", buildUDPRawFixture, 1<<20, 10),
		makeProxyTunnelCase("websocket_raw", buildWebSocketRawFixture, 1<<20, 10),
		makeProxyTunnelCase("tls_wrapper", buildTLSWrapperFixture, 1<<20, 10),
		makeProxyTunnelCase("tlsintls_wrapper", buildTLSInTLSWrapperFixture, 1<<20, 10),
		makeProxyTunnelCase("http_transport", buildHTTPTransportFixture, 512*1024, 5),
		makeProxyTunnelCase("http2_transport", buildHTTP2TransportFixture, 512*1024, 5),
		makeSmokeTunnelCase("http2s_transport", buildHTTP2STransportFixture),
		makeSmokeTunnelCase("http2_forward_proxy", buildHTTP2ForwardProxyFixture),
		makeSmokeTunnelCase("http2_tlsintls", buildHTTP2TLSInTLSFixture),
		makeProxyTunnelCase("streamhttp_transport", buildStreamHTTPTransportFixture, 512*1024, 5),
		makeSmokeTunnelCase("streamhttps_transport", buildStreamHTTPSTransportFixture),
		makeSmokeTunnelCase("streamhttp_forward_proxy", buildStreamHTTPForwardProxyFixture),
		makeSmokeTunnelCase("streamhttp_tlsintls", buildStreamHTTPTLSInTLSFixture),
		makeUnixTunnelCase(),
		makeMemoryTunnelCase(),
		makeTCPStabilityCase(),
	)
}

func TestE2E_TunnelMatrix(t *testing.T) {
	cases := append([]runner.CaseSpec(nil), tunnelMatrixCases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	runner.RunTunnelSuite(t, cases)
}

func registerTunnelCases(cases ...runner.CaseSpec) {
	tunnelMatrixCases = append(tunnelMatrixCases, cases...)
}

func makeProxyTunnelCase(name string, build func(*testing.T) runner.FixtureSpec, largeTransfer, concurrent int) runner.CaseSpec {
	body := "hello " + name
	scenarios := []runner.Scenario{
		socksSmokeScenario(body),
		largeTransferScenario(largeTransfer),
	}
	if concurrent > 0 {
		scenarios = append(scenarios, concurrentScenario(body, concurrent))
	}
	return runner.CaseSpec{
		Name:      name,
		Build:     build,
		Scenarios: scenarios,
	}
}

func makeSmokeTunnelCase(name string, build func(*testing.T) runner.FixtureSpec) runner.CaseSpec {
	return runner.CaseSpec{
		Name:      name,
		Build:     build,
		Scenarios: []runner.Scenario{socksSmokeScenario("hello " + name)},
	}
}

func makeUnixTunnelCase() runner.CaseSpec {
	return runner.CaseSpec{
		Name: "unix_transport",
		Check: func(*testing.T) string {
			if runtime.GOOS == "windows" {
				return "unix sockets not supported on Windows"
			}
			return ""
		},
		Build: buildUnixTransportFixture,
		Scenarios: []runner.Scenario{
			socksSmokeScenario("hello unix_transport"),
			largeTransferScenario(512 * 1024),
		},
	}
}

func makeMemoryTunnelCase() runner.CaseSpec {
	return runner.CaseSpec{
		Name:  "memory_transport",
		Build: buildMemoryTransportFixture,
		Scenarios: []runner.Scenario{
			{
				Name: "memory_bridge_echo",
				Run: func(t *testing.T, fx *runner.Fixture) {
					if err := fx.Process("memory_client").Wait(); err != nil {
						t.Fatalf("memory bridge helper failed: %v", err)
					}
				},
			},
		},
	}
}

func makeTCPStabilityCase() runner.CaseSpec {
	return runner.CaseSpec{
		Name: "tcp_stability",
		Check: func(*testing.T) string {
			if os.Getenv("REM_STABILITY") == "" {
				return "set REM_STABILITY=1 to run 10-minute stability test"
			}
			return ""
		},
		Build: buildTCPRawFixture,
		Scenarios: []runner.Scenario{
			{
				Name: "stability",
				Run: func(t *testing.T, fx *runner.Fixture) {
					const (
						testDuration  = 10 * time.Minute
						probeInterval = 10 * time.Second
						httpTimeout   = 15 * time.Second
						targetURL     = "http://www.baidu.com"
					)

					socksAddr := fx.Endpoint("socks_addr")
					deadline := time.Now().Add(testDuration)
					total, success, fail := 0, 0, 0
					maxConsecutiveFails := 0
					consecutiveFails := 0
					start := time.Now()

					for time.Now().Before(deadline) {
						total++
						client := newSOCKS5Client(t, socksAddr, httpTimeout)
						resp, err := client.Get(targetURL)
						if err == nil {
							body, _ := io.ReadAll(resp.Body)
							resp.Body.Close()
							if resp.StatusCode == 200 && len(body) > 0 {
								success++
								consecutiveFails = 0
							} else {
								fail++
								consecutiveFails++
							}
						} else {
							fail++
							consecutiveFails++
						}
						client.CloseIdleConnections()

						if consecutiveFails > maxConsecutiveFails {
							maxConsecutiveFails = consecutiveFails
						}
						if consecutiveFails >= 5 {
							t.Fatalf("5 consecutive failures at probe %d", total)
						}
						time.Sleep(probeInterval)
					}

					rate := float64(success) / float64(total) * 100
					t.Logf("tcp stability duration=%v probes=%d ok=%d fail=%d max_consecutive=%d rate=%.1f%%",
						time.Since(start).Truncate(time.Second), total, success, fail, maxConsecutiveFails, rate)
					if rate < 95 {
						t.Fatalf("success rate %.1f%% below 95%% threshold", rate)
					}
				},
			},
		},
	}
}

func socksSmokeScenario(body string) runner.Scenario {
	return runner.Scenario{
		Name: "smoke_proxy",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer ts.Close()

			verifyHTTP(t, fx.Endpoint("socks_addr"), ts.URL, body)
		},
	}
}

func largeTransferScenario(size int) runner.Scenario {
	return runner.Scenario{
		Name: "large_transfer",
		Run: func(t *testing.T, fx *runner.Fixture) {
			verifyLargeTransfer(t, fx.Endpoint("socks_addr"), size)
		},
	}
}

func concurrentScenario(body string, n int) runner.Scenario {
	return runner.Scenario{
		Name: "concurrent_http",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer ts.Close()

			verifyConcurrent(t, fx.Endpoint("socks_addr"), ts.URL, body, n)
		},
	}
}

func buildTCPRawFixture(t *testing.T) runner.FixtureSpec {
	return buildProxyConsoleFixture(t, "tcp",
		func(port int) string { return fmt.Sprintf("tcp://0.0.0.0:%d/?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("tcp://%s/?wrapper=raw", addr) },
		[]runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
		[]runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 20 * time.Second}, {Kind: runner.ReadyDelayOnly, Timeout: 500 * time.Millisecond}},
	)
}

func buildUDPRawFixture(t *testing.T) runner.FixtureSpec {
	return buildProxyConsoleFixture(t, "udp",
		func(port int) string { return fmt.Sprintf("udp://0.0.0.0:%d/?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("udp://%s/?wrapper=raw", addr) },
		[]runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 2 * time.Second}},
		[]runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 20 * time.Second}, {Kind: runner.ReadyDelayOnly, Timeout: 300 * time.Millisecond}},
	)
}

func buildWebSocketRawFixture(t *testing.T) runner.FixtureSpec {
	return buildProxyConsoleFixture(t, "websocket",
		func(port int) string { return fmt.Sprintf("ws://0.0.0.0:%d/rem/?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("ws://%s/rem/?wrapper=raw", addr) },
		[]runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
		[]runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 20 * time.Second}, {Kind: runner.ReadyDelayOnly, Timeout: 300 * time.Millisecond}},
	)
}

func buildTLSWrapperFixture(t *testing.T) runner.FixtureSpec {
	return buildProxyConsoleFixture(t, "tls",
		func(port int) string { return fmt.Sprintf("tcp://0.0.0.0:%d/?wrapper=raw&tls=1", port) },
		func(addr string) string { return fmt.Sprintf("tcp://%s/?wrapper=raw&tls=1", addr) },
		[]runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
		[]runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 20 * time.Second}, {Kind: runner.ReadyDelayOnly, Timeout: 300 * time.Millisecond}},
	)
}

func buildTLSInTLSWrapperFixture(t *testing.T) runner.FixtureSpec {
	return buildProxyConsoleFixture(t, "tlsintls",
		func(port int) string { return fmt.Sprintf("tcp://0.0.0.0:%d/?wrapper=raw&tls=1&tlsintls=1", port) },
		func(addr string) string { return fmt.Sprintf("tcp://%s/?wrapper=raw&tls=1&tlsintls=1", addr) },
		[]runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
		[]runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 20 * time.Second}, {Kind: runner.ReadyDelayOnly, Timeout: 300 * time.Millisecond}},
	)
}

func buildHTTPTransportFixture(t *testing.T) runner.FixtureSpec {
	return buildHTTPStyleFixture(t, "http_transport",
		func(port int) string { return fmt.Sprintf("http://0.0.0.0:%d/rem?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("http://%s/rem?wrapper=raw", addr) },
		"",
	)
}

func buildHTTP2TransportFixture(t *testing.T) runner.FixtureSpec {
	return buildHTTPStyleFixture(t, "http2_transport",
		func(port int) string { return fmt.Sprintf("http2://0.0.0.0:%d/rem?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("http2://%s/rem?wrapper=raw", addr) },
		"",
	)
}

func buildHTTP2STransportFixture(t *testing.T) runner.FixtureSpec {
	return buildHTTPStyleFixture(t, "http2s_transport",
		func(port int) string { return fmt.Sprintf("http2s://0.0.0.0:%d/rem?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("http2s://%s/rem?wrapper=raw", addr) },
		"",
	)
}

func buildHTTP2ForwardProxyFixture(t *testing.T) runner.FixtureSpec {
	return buildHTTPStyleFixture(t, "http2_forward_proxy",
		func(port int) string { return fmt.Sprintf("http2://0.0.0.0:%d/rem?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("http2://%s/rem?wrapper=raw", addr) },
		"-f {{forward_proxy}}",
	)
}

func buildHTTP2TLSInTLSFixture(t *testing.T) runner.FixtureSpec {
	return buildHTTPStyleFixture(t, "http2_tlsintls",
		func(port int) string { return fmt.Sprintf("http2://0.0.0.0:%d/rem?wrapper=raw&tlsintls=1", port) },
		func(addr string) string { return fmt.Sprintf("http2://%s/rem?wrapper=raw&tlsintls=1", addr) },
		"",
	)
}

func buildStreamHTTPTransportFixture(t *testing.T) runner.FixtureSpec {
	return buildHTTPStyleFixture(t, "streamhttp_transport",
		func(port int) string { return fmt.Sprintf("streamhttp://0.0.0.0:%d/rem?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("streamhttp://%s/rem?wrapper=raw", addr) },
		"",
	)
}

func buildStreamHTTPSTransportFixture(t *testing.T) runner.FixtureSpec {
	return buildHTTPStyleFixture(t, "streamhttps_transport",
		func(port int) string { return fmt.Sprintf("streamhttps://0.0.0.0:%d/rem?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("streamhttps://%s/rem?wrapper=raw", addr) },
		"",
	)
}

func buildStreamHTTPForwardProxyFixture(t *testing.T) runner.FixtureSpec {
	return buildHTTPStyleFixture(t, "streamhttp_forward_proxy",
		func(port int) string { return fmt.Sprintf("streamhttp://0.0.0.0:%d/rem?wrapper=raw", port) },
		func(addr string) string { return fmt.Sprintf("streamhttp://%s/rem?wrapper=raw", addr) },
		"-f {{forward_proxy}}",
	)
}

func buildStreamHTTPTLSInTLSFixture(t *testing.T) runner.FixtureSpec {
	return buildHTTPStyleFixture(t, "streamhttp_tlsintls",
		func(port int) string { return fmt.Sprintf("streamhttp://0.0.0.0:%d/rem?wrapper=raw&tlsintls=1", port) },
		func(addr string) string { return fmt.Sprintf("streamhttp://%s/rem?wrapper=raw&tlsintls=1", addr) },
		"",
	)
}

func buildUnixTransportFixture(t *testing.T) runner.FixtureSpec {
	socketPath := filepath.Join(t.TempDir(), fmt.Sprintf("rem-unix-%d.sock", atomic.AddUint32(&testCounter, 1)))
	socksPort := freePort(t)
	alias := fmt.Sprintf("unix_%d", atomic.AddUint32(&testCounter, 1))
	vars := runner.Vars{
		"socket_path": socketPath,
		"socks_port":  fmt.Sprintf("%d", socksPort),
		"socks_addr":  fmt.Sprintf("127.0.0.1:%d", socksPort),
		"alias":       alias,
	}
	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars:       vars,
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: "--debug -s unix://{{socket_path}}?wrapper=raw -i 127.0.0.1 --no-sub",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyUnixSocket, Target: "{{socket_path}}", Timeout: 10 * time.Second}},
			},
			{
				Name:    "client",
				Mode:    "console_run",
				Command: "-c unix://{{socket_path}}?wrapper=raw -l socks5://127.0.0.1:{{socks_port}} -a {{alias}} --debug",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 20 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 300 * time.Millisecond},
				},
			},
		},
	}
}

func buildMemoryTransportFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"echo_addr":   startEchoServer(t),
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
			},
			{
				Name:        "memory_client",
				Mode:        "memory_bridge_client",
				TestTimeout: 60 * time.Second,
				Env: runner.Vars{
					"REM_SERVER_ADDR": "{{server_addr}}",
					"REM_ECHO_ADDR":   "{{echo_addr}}",
				},
			},
		},
	}
}

func buildProxyConsoleFixture(
	t *testing.T,
	aliasPrefix string,
	serverURL func(port int) string,
	clientURL func(addr string) string,
	serverWait []runner.ReadySpec,
	clientWait []runner.ReadySpec,
) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	alias := fmt.Sprintf("%s_%d", aliasPrefix, atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"socks_port":  fmt.Sprintf("%d", socksPort),
			"socks_addr":  fmt.Sprintf("127.0.0.1:%d", socksPort),
			"alias":       alias,
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: fmt.Sprintf("--debug -s %s -i 127.0.0.1 --no-sub", serverURL(serverPort)),
				Wait:    serverWait,
			},
			{
				Name:    "client",
				Mode:    "console_run",
				Command: fmt.Sprintf("-c %s -l socks5://127.0.0.1:%d -a %s --debug", clientURL(serverAddr), socksPort, alias),
				Wait:    clientWait,
			},
		},
	}
}

func buildHTTPStyleFixture(
	t *testing.T,
	aliasPrefix string,
	serverURL func(port int) string,
	clientURL func(addr string) string,
	clientExtra string,
) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	alias := fmt.Sprintf("%s_%d", aliasPrefix, atomic.AddUint32(&testCounter, 1))
	vars := runner.Vars{
		"server_port": fmt.Sprintf("%d", serverPort),
		"server_addr": serverAddr,
		"socks_port":  fmt.Sprintf("%d", socksPort),
		"socks_addr":  fmt.Sprintf("127.0.0.1:%d", socksPort),
		"alias":       alias,
	}
	if strings.Contains(clientExtra, "{{forward_proxy}}") {
		vars["forward_proxy"] = startForwardSOCKS5Proxy(t)
	}

	command := fmt.Sprintf("-c %s -l socks5://127.0.0.1:%d -a %s --debug", clientURL(serverAddr), socksPort, alias)
	if clientExtra != "" {
		command += " " + clientExtra
	}

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars:       vars,
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: fmt.Sprintf("--debug -s %s -i 127.0.0.1 --no-sub", serverURL(serverPort)),
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
			},
			{
				Name:    "client",
				Mode:    "console_run",
				Command: command,
				Wait:    []runner.ReadySpec{{Kind: runner.ReadySocksHTTP, Target: "{{socks_addr}}", Timeout: 20 * time.Second, User: e2eSocksUser, Password: e2eSocksPassword}},
			},
		},
	}
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

// ---------------------------------------------------------------------------
// ICMP transport
// ---------------------------------------------------------------------------

func init() {
	registerTunnelCases(runner.CaseSpec{
		Name: "icmp_transport",
		Check: func(*testing.T) string {
			if os.Getenv("REM_TEST_ICMP") == "" {
				return "set REM_TEST_ICMP=1 to run ICMP tests (requires raw socket privileges)"
			}
			return ""
		},
		Build: buildICMPTransportFixture,
		Scenarios: []runner.Scenario{
			socksSmokeScenario("hello icmp_transport"),
			largeTransferScenario(256 * 1024),
		},
	})
}

func buildICMPTransportFixture(t *testing.T) runner.FixtureSpec {
	socksPort := freePort(t)
	alias := fmt.Sprintf("icmp_%d", atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"socks_port": fmt.Sprintf("%d", socksPort),
			"socks_addr": fmt.Sprintf("127.0.0.1:%d", socksPort),
			"alias":      alias,
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: "--debug -s icmp://127.0.0.1/?wrapper=raw -i 127.0.0.1 --no-sub",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 2 * time.Second}},
			},
			{
				Name:    "client",
				Mode:    "console_run",
				Command: "-c icmp://127.0.0.1/?wrapper=raw -l socks5://127.0.0.1:{{socks_port}} -a {{alias}} --debug",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 20 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 500 * time.Millisecond},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Stress tests
// ---------------------------------------------------------------------------

func init() {
	registerTunnelCases(
		makeStressTunnelCase("tcp_stress", buildTCPRawFixture),
		makeStressTunnelCase("udp_stress", buildUDPRawFixture),
		makeStressTunnelCase("websocket_stress", buildWebSocketRawFixture),
	)
}

func makeStressTunnelCase(name string, build func(*testing.T) runner.FixtureSpec) runner.CaseSpec {
	return runner.CaseSpec{
		Name: name,
		Check: func(*testing.T) string {
			if os.Getenv("REM_STRESS") == "" {
				return "set REM_STRESS=1 to run stress cases"
			}
			return ""
		},
		Build: build,
		Scenarios: []runner.Scenario{
			largeTransferScenario(256 * 1024 * 1024),
			concurrentScenario("stress-high-concurrency", 100),
		},
	}
}

// ── Serve ───────────────────────────────────────────────────────────────────

var serveMatrixCases []runner.CaseSpec

func init() {
	registerServeCases(
		runner.CaseSpec{
			Name:  "socks5_proxy",
			Build: buildSOCKS5ServeFixture,
			Scenarios: []runner.Scenario{
				serveHTTPSmokeScenario("hello socks5_proxy"),
				serveLargeTransferScenario(1 << 20),
				serveConcurrentHTTPScenario("hello socks5_proxy", 10),
			},
		},
		runner.CaseSpec{
			Name:  "http_proxy",
			Build: buildHTTPProxyServeFixture,
			Scenarios: []runner.Scenario{
				httpsConnectSmokeScenario("hello http_proxy"),
				httpsConnectConcurrentScenario("hello http_proxy", 5),
			},
		},
		runner.CaseSpec{
			Name:  "portforward",
			Build: buildPortForwardFixture,
			Scenarios: []runner.Scenario{
				echoScenario("echo_smoke", "listen_addr", []byte("hello portforward")),
				tcpPayloadScenario("large_payload", "listen_addr", 128*1024),
				concurrentConnScenario("concurrent_conns", "listen_addr", "conn", 5),
			},
		},
		runner.CaseSpec{
			Name:  "portforward_domain_dest",
			Build: buildPortForwardDomainFixture,
			Scenarios: []runner.Scenario{
				echoScenario("domain_dest", "listen_addr", []byte("hello portforward domain")),
			},
		},
		runner.CaseSpec{
			Name:  "raw_serve",
			Build: buildRawServeFixture,
			Scenarios: []runner.Scenario{
				echoScenario("direct_tcp_smoke", "listen_addr", []byte("hello raw serve")),
				tcpPayloadScenario("large_payload", "listen_addr", 256*1024),
				concurrentConnScenario("concurrent_conns", "listen_addr", "raw", 5),
			},
		},
		runner.CaseSpec{
			Name:  "raw_domain_dest",
			Build: buildRawDomainFixture,
			Scenarios: []runner.Scenario{
				echoScenario("domain_dest", "listen_addr", []byte("hello raw domain")),
			},
		},
	)
}

func TestE2E_ServeMatrix(t *testing.T) {
	cases := append([]runner.CaseSpec(nil), serveMatrixCases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	runner.RunServeSuite(t, cases)
}

func registerServeCases(cases ...runner.CaseSpec) {
	serveMatrixCases = append(serveMatrixCases, cases...)
}

func serveHTTPSmokeScenario(body string) runner.Scenario {
	return runner.Scenario{
		Name: "http_smoke",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer ts.Close()
			verifyHTTP(t, fx.Endpoint("socks_addr"), ts.URL, body)
		},
	}
}

func serveLargeTransferScenario(size int) runner.Scenario {
	return runner.Scenario{
		Name: "large_transfer",
		Run: func(t *testing.T, fx *runner.Fixture) {
			verifyLargeTransfer(t, fx.Endpoint("socks_addr"), size)
		},
	}
}

func serveConcurrentHTTPScenario(body string, n int) runner.Scenario {
	return runner.Scenario{
		Name: "concurrent_requests",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer ts.Close()
			verifyConcurrent(t, fx.Endpoint("socks_addr"), ts.URL, body, n)
		},
	}
}

func httpsConnectSmokeScenario(body string) runner.Scenario {
	return runner.Scenario{
		Name: "https_connect",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer ts.Close()
			client := newHTTPProxyClient(t, fx.Endpoint("proxy_addr"), ts.Client().Transport.(*http.Transport).TLSClientConfig, 15*time.Second)
			resp, err := client.Get(ts.URL)
			if err != nil {
				t.Fatalf("HTTPS through HTTP proxy: %v", err)
			}
			defer resp.Body.Close()
			payload, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read proxy response: %v", err)
			}
			if string(payload) != body {
				t.Fatalf("expected %q, got %q", body, string(payload))
			}
		},
	}
}

func httpsConnectConcurrentScenario(body string, n int) runner.Scenario {
	return runner.Scenario{
		Name: "https_connect_concurrent",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer ts.Close()

			client := newHTTPProxyClient(t, fx.Endpoint("proxy_addr"), ts.Client().Transport.(*http.Transport).TLSClientConfig, 15*time.Second)
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					resp, err := client.Get(ts.URL)
					if err != nil {
						errs <- fmt.Errorf("request %d: %w", idx, err)
						return
					}
					defer resp.Body.Close()
					payload, err := io.ReadAll(resp.Body)
					if err != nil {
						errs <- fmt.Errorf("request %d read: %w", idx, err)
						return
					}
					if string(payload) != body {
						errs <- fmt.Errorf("request %d: expected %q got %q", idx, body, payload)
					}
				}(i)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}
		},
	}
}

func echoScenario(name, endpoint string, payload []byte) runner.Scenario {
	return runner.Scenario{
		Name: name,
		Run: func(t *testing.T, fx *runner.Fixture) {
			echoRoundtrip(t, fx.Endpoint(endpoint), payload)
		},
	}
}

func tcpPayloadScenario(name, endpoint string, size int) runner.Scenario {
	return runner.Scenario{
		Name: name,
		Run: func(t *testing.T, fx *runner.Fixture) {
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i % 251)
			}
			echoRoundtrip(t, fx.Endpoint(endpoint), payload)
		},
	}
}

func concurrentConnScenario(name, endpoint, prefix string, n int) runner.Scenario {
	return runner.Scenario{
		Name: name,
		Run: func(t *testing.T, fx *runner.Fixture) {
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					msg := []byte(fmt.Sprintf("%s-%d-hello", prefix, idx))
					conn, err := net.DialTimeout("tcp", fx.Endpoint(endpoint), 5*time.Second)
					if err != nil {
						errs <- fmt.Errorf("conn %d dial: %w", idx, err)
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
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
						errs <- fmt.Errorf("conn %d: expected %q got %q", idx, msg, buf)
					}
				}(i)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}
		},
	}
}

func buildSOCKS5ServeFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	alias := fmt.Sprintf("socks5_%d", atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"socks_port":  fmt.Sprintf("%d", socksPort),
			"socks_addr":  fmt.Sprintf("127.0.0.1:%d", socksPort),
			"alias":       alias,
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
			},
			{
				Name:    "client",
				Mode:    "console_run",
				Command: "-c tcp://{{server_addr}}/?wrapper=raw -l socks5://127.0.0.1:{{socks_port}} -a {{alias}} --debug",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 500 * time.Millisecond},
				},
			},
		},
	}
}

func buildHTTPProxyServeFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	proxyPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	alias := fmt.Sprintf("http_proxy_%d", atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"proxy_port":  fmt.Sprintf("%d", proxyPort),
			"proxy_addr":  fmt.Sprintf("127.0.0.1:%d", proxyPort),
			"alias":       alias,
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
			},
			{
				Name:    "client",
				Mode:    "console_run",
				Command: "-c tcp://{{server_addr}}/?wrapper=raw -l http://127.0.0.1:{{proxy_port}} -a {{alias}} --debug",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyHTTPProxy, Target: "{{proxy_addr}}", Timeout: 20 * time.Second, User: e2eSocksUser, Password: e2eSocksPassword}},
			},
		},
	}
}

func buildPortForwardFixture(t *testing.T) runner.FixtureSpec {
	return buildDirectServeFixture(t, "portforward", "forward", startEchoServer(t))
}

func buildPortForwardDomainFixture(t *testing.T) runner.FixtureSpec {
	echoAddr := startLocalhostEchoServer(t)
	return buildDirectServeFixture(t, "portforward_domain", "forward", asLocalhostAddr(t, echoAddr))
}

func buildRawServeFixture(t *testing.T) runner.FixtureSpec {
	return buildDirectServeFixture(t, "raw", "raw", startEchoServer(t))
}

func buildRawDomainFixture(t *testing.T) runner.FixtureSpec {
	echoAddr := startLocalhostEchoServer(t)
	return buildDirectServeFixture(t, "raw_domain", "raw", asLocalhostAddr(t, echoAddr))
}

func buildDirectServeFixture(t *testing.T, aliasPrefix, scheme, dest string) runner.FixtureSpec {
	serverPort := freePort(t)
	listenPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", listenPort)
	alias := fmt.Sprintf("%s_%d", aliasPrefix, atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"listen_port": fmt.Sprintf("%d", listenPort),
			"listen_addr": listenAddr,
			"dest":        dest,
			"alias":       alias,
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
			},
			{
				Name:    "client",
				Mode:    "console_run",
				Command: fmt.Sprintf("-c tcp://{{server_addr}}/?wrapper=raw -l %s://0.0.0.0:{{listen_port}} -r {{dest}} -a {{alias}} --debug", scheme),
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{listen_addr}}", Timeout: 20 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 300 * time.Millisecond},
				},
			},
		},
	}
}

// ── Console ─────────────────────────────────────────────────────────────────

var remConsoleMatrixCases []runner.CaseSpec

func init() {
	registerConsoleCases(
		runner.CaseSpec{
			Name:      "bridge_alias",
			Build:     buildBridgeAliasFixture,
			Scenarios: []runner.Scenario{consoleSOCKSSmokeScenario("socks_addr")},
		},
		runner.CaseSpec{
			Name:      "cascade_3hop",
			Build:     buildCascade3HopFixture,
			Scenarios: []runner.Scenario{consoleSOCKSSmokeScenario("socks_addr")},
		},
		runner.CaseSpec{
			Name:      "relay_2hop",
			Build:     buildRelay2HopFixture,
			Scenarios: []runner.Scenario{consoleSOCKSSmokeScenario("socks_addr")},
		},
		runner.CaseSpec{
			Name:      "relay_3hop",
			Build:     buildRelay3HopFixture,
			Scenarios: []runner.Scenario{consoleSOCKSSmokeScenario("socks_addr")},
		},
		runner.CaseSpec{
			Name:      "relay_topology",
			Build:     buildRelayTopologyFixture,
			Scenarios: []runner.Scenario{topologyScenario()},
		},
		runner.CaseSpec{
			Name:      "multiserve_socks_http",
			Build:     buildMultiServeSOCKSHTTPFixture,
			Scenarios: []runner.Scenario{multiServeSOCKSHTTPScenario()},
		},
		runner.CaseSpec{
			Name:      "multiserve_dual_socks5",
			Build:     buildMultiServeDualSOCKSFixture,
			Scenarios: []runner.Scenario{multiServeDualSOCKSScenario(), multiServeDualSOCKSConcurrentScenario()},
		},
		runner.CaseSpec{
			Name:      "multiserve_2pair",
			Build:     buildMultiServe2PairFixture,
			Scenarios: []runner.Scenario{multiServeNPairScenario(2)},
		},
		runner.CaseSpec{
			Name:      "multiserve_3pair",
			Build:     buildMultiServe3PairFixture,
			Scenarios: []runner.Scenario{multiServeNPairScenario(3)},
		},
		runner.CaseSpec{
			Name:      "connhub_round_robin",
			Build:     buildConnHubFixture("round-robin"),
			Scenarios: []runner.Scenario{connHubScenario("round-robin")},
		},
		runner.CaseSpec{
			Name:      "connhub_random",
			Build:     buildConnHubFixture("random"),
			Scenarios: []runner.Scenario{connHubScenario("random")},
		},
		runner.CaseSpec{
			Name:      "connhub_fallback",
			Build:     buildConnHubFixture("fallback"),
			Scenarios: []runner.Scenario{connHubScenario("fallback")},
		},
		runner.CaseSpec{
			Name:      "keepalive_server_restart",
			Build:     buildKeepaliveFixture,
			Scenarios: []runner.Scenario{keepaliveRecoveryScenario()},
		},
		runner.CaseSpec{
			Name:      "reconfigure_push",
			Build:     buildReconfigureFixture,
			Scenarios: []runner.Scenario{reconfigureScenario()},
		},
	)
}

func TestE2E_RemConsoleMatrix(t *testing.T) {
	cases := append([]runner.CaseSpec(nil), remConsoleMatrixCases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	runner.RunConsoleSuite(t, cases)
}

func registerConsoleCases(cases ...runner.CaseSpec) {
	remConsoleMatrixCases = append(remConsoleMatrixCases, cases...)
}

func consoleSOCKSSmokeScenario(endpoint string) runner.Scenario {
	return runner.Scenario{
		Name: "socks_http_smoke",
		Run: func(t *testing.T, fx *runner.Fixture) {
			targetURL := startHTTPTarget(t)
			assertSOCKS5Get(t, fx.Endpoint(endpoint), targetURL, 15*time.Second)
		},
	}
}

func topologyScenario() runner.Scenario {
	return runner.Scenario{
		Name: "topology_smoke",
		Run: func(t *testing.T, fx *runner.Fixture) {
			resp, err := http.Get(fx.Endpoint("topology_url"))
			if err != nil {
				t.Fatalf("GET /topology: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Fatalf("topology HTTP %d: %s", resp.StatusCode, string(body))
			}

			var graph agent.TopologyGraph
			if err := json.Unmarshal(body, &graph); err != nil {
				t.Fatalf("unmarshal topology: %v", err)
			}
			if len(graph.Nodes) < 3 {
				t.Fatalf("expected >= 3 nodes, got %d", len(graph.Nodes))
			}

			relayAlias := fx.Var("relay_alias")
			leafAlias := fx.Var("leaf_alias")
			var foundLeaf bool
			tunnelEdges := 0
			for _, edge := range graph.Edges {
				if edge.Layer == "tunnel" {
					tunnelEdges++
				}
			}
			for _, node := range graph.Nodes {
				if node.ID != leafAlias {
					continue
				}
				foundLeaf = true
				if node.Via != relayAlias {
					t.Fatalf("leaf via = %q, want %q", node.Via, relayAlias)
				}
				if len(node.Services) == 0 {
					t.Fatal("leaf missing services")
				}
			}
			if !foundLeaf {
				t.Fatalf("leaf %q not found in topology", leafAlias)
			}
			if tunnelEdges < 2 {
				t.Fatalf("expected >= 2 tunnel edges, got %d", tunnelEdges)
			}
		},
	}
}

func multiServeSOCKSHTTPScenario() runner.Scenario {
	return runner.Scenario{
		Name: "dual_endpoint_smoke",
		Run: func(t *testing.T, fx *runner.Fixture) {
			tsPlain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("multi-serve-ok"))
			}))
			defer tsPlain.Close()

			tsTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("multi-serve-https"))
			}))
			defer tsTLS.Close()

			verifyHTTP(t, fx.Endpoint("socks_addr"), tsPlain.URL, "multi-serve-ok")

			client := newHTTPProxyClient(t, fx.Endpoint("proxy_addr"), tsTLS.Client().Transport.(*http.Transport).TLSClientConfig, 15*time.Second)
			resp, err := client.Get(tsTLS.URL)
			if err != nil {
				t.Fatalf("HTTP proxy GET: %v", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read proxy body: %v", err)
			}
			if string(body) != "multi-serve-https" {
				t.Fatalf("expected %q, got %q", "multi-serve-https", string(body))
			}
		},
	}
}

func multiServeDualSOCKSScenario() runner.Scenario {
	return runner.Scenario{
		Name: "dual_endpoint_smoke",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("dual-socks5-ok"))
			}))
			defer ts.Close()

			verifyHTTP(t, fx.Endpoint("socks_addr_1"), ts.URL, "dual-socks5-ok")
			verifyHTTP(t, fx.Endpoint("socks_addr_2"), ts.URL, "dual-socks5-ok")
		},
	}
}

func multiServeDualSOCKSConcurrentScenario() runner.Scenario {
	return runner.Scenario{
		Name: "dual_endpoint_concurrent",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("concurrent-dual"))
			}))
			defer ts.Close()

			const reqsPerServe = 5
			var wg sync.WaitGroup
			errs := make(chan error, reqsPerServe*2)
			for _, endpoint := range []string{"socks_addr_1", "socks_addr_2"} {
				addr := fx.Endpoint(endpoint)
				for i := 0; i < reqsPerServe; i++ {
					wg.Add(1)
					go func(idx int) {
						defer wg.Done()
						client := newSOCKS5Client(t, addr, 10*time.Second)
						resp, err := client.Get(ts.URL)
						if err != nil {
							errs <- fmt.Errorf("%s[%d]: %w", endpoint, idx, err)
							return
						}
						defer resp.Body.Close()
						body, _ := io.ReadAll(resp.Body)
						if string(body) != "concurrent-dual" {
							errs <- fmt.Errorf("%s[%d]: expected %q got %q", endpoint, idx, "concurrent-dual", body)
						}
					}(i)
				}
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}
		},
	}
}

func multiServeNPairScenario(n int) runner.Scenario {
	return runner.Scenario{
		Name: "topology_smoke",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("npair-ok"))
			}))
			defer ts.Close()
			for i := 1; i <= n; i++ {
				verifyHTTP(t, fx.Endpoint(fmt.Sprintf("socks_addr_%d", i)), ts.URL, "npair-ok")
			}
		},
	}
}

func connHubScenario(lb string) runner.Scenario {
	return runner.Scenario{
		Name: "load_distribution",
		Run: func(t *testing.T, fx *runner.Fixture) {
			if err := fx.Process("client").Wait(); err != nil {
				t.Fatalf("ConnHub client failed: %v", err)
			}

			raw, err := os.ReadFile(fx.Endpoint("report_path"))
			if err != nil {
				t.Fatalf("read report: %v", err)
			}
			var report connHubPerfReport
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatalf("unmarshal report: %v", err)
			}
			if report.Requests == 0 || len(report.Stats) == 0 {
				t.Fatalf("empty report: %+v", report)
			}

			selectedByLabel := statsByLabel(report.Stats)
			if selectedByLabel["tcp"] == 0 {
				t.Fatalf("tcp selected count is zero: %+v", selectedByLabel)
			}
			switch lb {
			case "round-robin", "random":
				if selectedByLabel["udp-1"] == 0 || selectedByLabel["ws-2"] == 0 {
					t.Fatalf("%s requires all channels to be used: %+v", lb, selectedByLabel)
				}
			case "fallback":
				if selectedByLabel["tcp"] < selectedByLabel["udp-1"] || selectedByLabel["tcp"] < selectedByLabel["ws-2"] {
					t.Fatalf("fallback should prioritize tcp: %+v", selectedByLabel)
				}
			}
		},
	}
}

func keepaliveRecoveryScenario() runner.Scenario {
	return runner.Scenario{
		Name: "server_restart_recover",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("keepalive-ok"))
			}))
			defer ts.Close()

			socksAddr := fx.Endpoint("socks_addr")
			verifyHTTP(t, socksAddr, ts.URL, "keepalive-ok")

			fx.Kill("server")
			time.Sleep(10 * time.Second)
			fx.Restart("server")

			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				client := newSOCKS5Client(t, socksAddr, 5*time.Second)
				resp, err := client.Get(ts.URL)
				if err == nil {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if string(body) == "keepalive-ok" {
						return
					}
				}
				time.Sleep(1 * time.Second)
			}
			t.Fatal("client never recovered after server restart")
		},
	}
}

func reconfigureScenario() runner.Scenario {
	return runner.Scenario{
		Name: "config_update_observed",
		Run: func(t *testing.T, fx *runner.Fixture) {
			if err := fx.Process("client").Wait(); err != nil {
				t.Fatalf("reconfigure client failed: %v", err)
			}
			raw, err := os.ReadFile(fx.Endpoint("report_path"))
			if err != nil {
				t.Fatalf("read report: %v", err)
			}
			var received map[string]string
			if err := json.Unmarshal(raw, &received); err != nil {
				t.Fatalf("unmarshal report: %v", err)
			}
			if received["interval"] != "7777" {
				t.Fatalf("interval=%q want 7777", received["interval"])
			}
			if received["custom"] != "hello_from_server" {
				t.Fatalf("custom=%q want hello_from_server", received["custom"])
			}
		},
	}
}

func buildBridgeAliasFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	alias := fmt.Sprintf("bridge_alias_%d", atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"socks_port":  fmt.Sprintf("%d", socksPort),
			"socks_addr":  socksAddr,
			"alias":       alias,
		},
		Processes: []runner.ProcessSpec{
			serverConsoleProcess(serverAddr, "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub"),
			{
				Name:    "exit_node",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -a {{alias}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_port}}",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
				},
			},
			{
				Name:    "dest_node",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -d {{alias}}",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 3 * time.Second}},
			},
		},
	}
}

func buildCascade3HopFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	id := atomic.AddUint32(&testCounter, 1)
	hop3 := fmt.Sprintf("hop3_%d", id)
	hop2 := fmt.Sprintf("hop2_%d", id)

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"socks_port":  fmt.Sprintf("%d", socksPort),
			"socks_addr":  socksAddr,
			"hop2":        hop2,
			"hop3":        hop3,
		},
		Processes: []runner.ProcessSpec{
			serverConsoleProcess(serverAddr, "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub"),
			{
				Name:    "hop3",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -a {{hop3}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_port}}",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
				},
			},
			{
				Name:    "hop2",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -a {{hop2}} -d {{hop3}}",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 3 * time.Second}},
			},
			{
				Name:    "hop1",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -d {{hop2}}",
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 3 * time.Second}},
			},
		},
	}
}

func buildRelay2HopFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	relayPort := freePort(t)
	socksPort := freePort(t)
	id := atomic.AddUint32(&testCounter, 1)
	relayAlias := fmt.Sprintf("relay_%d", id)
	leafAlias := fmt.Sprintf("leaf_%d", id)

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": fmt.Sprintf("127.0.0.1:%d", serverPort),
			"relay_port":  fmt.Sprintf("%d", relayPort),
			"relay_addr":  fmt.Sprintf("127.0.0.1:%d", relayPort),
			"socks_port":  fmt.Sprintf("%d", socksPort),
			"socks_addr":  fmt.Sprintf("127.0.0.1:%d", socksPort),
			"relay_alias": relayAlias,
			"leaf_alias":  leafAlias,
		},
		Processes: []runner.ProcessSpec{
			serverConsoleProcess(fmt.Sprintf("127.0.0.1:%d", serverPort), "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub"),
			{
				Name:    "relay",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -s tcp://0.0.0.0:{{relay_port}} -a {{relay_alias}} -i 127.0.0.1",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{relay_addr}}", Timeout: 10 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
				},
			},
			{
				Name:    "leaf",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{relay_addr}}/?wrapper=raw&via={{relay_alias}} -a {{leaf_alias}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_port}}",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
				},
			},
		},
	}
}

func buildRelay3HopFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	relayCPort := freePort(t)
	relayBPort := freePort(t)
	socksPort := freePort(t)
	id := atomic.AddUint32(&testCounter, 1)
	relayC := fmt.Sprintf("relay_c_%d", id)
	relayB := fmt.Sprintf("relay_b_%d", id)
	leafA := fmt.Sprintf("leaf_a_%d", id)

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port":  fmt.Sprintf("%d", serverPort),
			"server_addr":  fmt.Sprintf("127.0.0.1:%d", serverPort),
			"relay_c_port": fmt.Sprintf("%d", relayCPort),
			"relay_c_addr": fmt.Sprintf("127.0.0.1:%d", relayCPort),
			"relay_b_port": fmt.Sprintf("%d", relayBPort),
			"relay_b_addr": fmt.Sprintf("127.0.0.1:%d", relayBPort),
			"socks_port":   fmt.Sprintf("%d", socksPort),
			"socks_addr":   fmt.Sprintf("127.0.0.1:%d", socksPort),
			"relay_c":      relayC,
			"relay_b":      relayB,
			"leaf_a":       leafA,
		},
		Processes: []runner.ProcessSpec{
			serverConsoleProcess(fmt.Sprintf("127.0.0.1:%d", serverPort), "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub"),
			{
				Name:    "relay_c",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -s tcp://0.0.0.0:{{relay_c_port}} -a {{relay_c}} -i 127.0.0.1",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{relay_c_addr}}", Timeout: 10 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
				},
			},
			{
				Name:    "relay_b",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{relay_c_addr}}/?wrapper=raw&via={{relay_c}} -s tcp://0.0.0.0:{{relay_b_port}} -a {{relay_b}} -i 127.0.0.1",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{relay_b_addr}}", Timeout: 10 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
				},
			},
			{
				Name:    "leaf_a",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{relay_b_addr}}/?wrapper=raw&via={{relay_b}} -a {{leaf_a}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_port}}",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
				},
			},
		},
	}
}

func buildRelayTopologyFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	relayPort := freePort(t)
	socksPort := freePort(t)
	subPort := freePort(t)
	id := atomic.AddUint32(&testCounter, 1)
	relayAlias := fmt.Sprintf("relay_topo_%d", id)
	leafAlias := fmt.Sprintf("leaf_topo_%d", id)

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port":  fmt.Sprintf("%d", serverPort),
			"server_addr":  fmt.Sprintf("127.0.0.1:%d", serverPort),
			"relay_port":   fmt.Sprintf("%d", relayPort),
			"relay_addr":   fmt.Sprintf("127.0.0.1:%d", relayPort),
			"socks_port":   fmt.Sprintf("%d", socksPort),
			"socks_addr":   fmt.Sprintf("127.0.0.1:%d", socksPort),
			"sub_port":     fmt.Sprintf("%d", subPort),
			"topology_url": fmt.Sprintf("http://127.0.0.1:%d/topology", subPort),
			"relay_alias":  relayAlias,
			"leaf_alias":   leafAlias,
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --sub http://0.0.0.0:{{sub_port}}/sub",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second},
					{Kind: runner.ReadyTCPListen, Target: "127.0.0.1:{{sub_port}}", Timeout: 10 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 2 * time.Second},
				},
			},
			{
				Name:    "relay",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -s tcp://0.0.0.0:{{relay_port}} -a {{relay_alias}} -i 127.0.0.1",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{relay_addr}}", Timeout: 10 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 2 * time.Second},
				},
			},
			{
				Name:    "leaf",
				Mode:    "console_run",
				Command: "--debug -c tcp://nonenonenonenone:@{{relay_addr}}/?wrapper=raw&via={{relay_alias}} -a {{leaf_alias}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_port}}",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 2 * time.Second},
				},
			},
		},
	}
}

func buildMultiServeSOCKSHTTPFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort := freePort(t)
	httpPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"socks_port":  fmt.Sprintf("%d", socksPort),
			"socks_addr":  fmt.Sprintf("127.0.0.1:%d", socksPort),
			"http_port":   fmt.Sprintf("%d", httpPort),
			"proxy_addr":  fmt.Sprintf("127.0.0.1:%d", httpPort),
			"alias":       fmt.Sprintf("multiserve_http_%d", atomic.AddUint32(&testCounter, 1)),
		},
		Processes: []runner.ProcessSpec{
			serverConsoleProcess(serverAddr, "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub"),
			{
				Name:    "client",
				Mode:    "console_run",
				Command: "-c tcp://{{server_addr}}/?wrapper=raw -l socks5://127.0.0.1:{{socks_port}} -l http://127.0.0.1:{{http_port}} -a {{alias}} --debug",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyHTTPProxy, Target: "{{proxy_addr}}", Timeout: 15 * time.Second, User: e2eSocksUser, Password: e2eSocksPassword},
				},
			},
		},
	}
}

func buildMultiServeDualSOCKSFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort1 := freePort(t)
	socksPort2 := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port":  fmt.Sprintf("%d", serverPort),
			"server_addr":  serverAddr,
			"socks_port_1": fmt.Sprintf("%d", socksPort1),
			"socks_addr_1": fmt.Sprintf("127.0.0.1:%d", socksPort1),
			"socks_port_2": fmt.Sprintf("%d", socksPort2),
			"socks_addr_2": fmt.Sprintf("127.0.0.1:%d", socksPort2),
			"alias":        fmt.Sprintf("multiserve_socks_%d", atomic.AddUint32(&testCounter, 1)),
		},
		Processes: []runner.ProcessSpec{
			serverConsoleProcess(serverAddr, "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub"),
			{
				Name:    "client",
				Mode:    "console_run",
				Command: "-c tcp://{{server_addr}}/?wrapper=raw -l socks5://127.0.0.1:{{socks_port_1}} -l socks5://127.0.0.1:{{socks_port_2}} -a {{alias}} --debug",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr_1}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr_2}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 500 * time.Millisecond},
				},
			},
		},
	}
}

func buildMultiServe2PairFixture(t *testing.T) runner.FixtureSpec {
	return buildMultiServeNSOCKSFixture(t, 2)
}

func buildMultiServe3PairFixture(t *testing.T) runner.FixtureSpec {
	return buildMultiServeNSOCKSFixture(t, 3)
}

func buildMultiServeNSOCKSFixture(t *testing.T, n int) runner.FixtureSpec {
	serverPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	vars := runner.Vars{
		"server_port": fmt.Sprintf("%d", serverPort),
		"server_addr": serverAddr,
		"alias":       fmt.Sprintf("multiserve_n_%d", atomic.AddUint32(&testCounter, 1)),
	}
	command := "-c tcp://{{server_addr}}/?wrapper=raw"
	wait := []runner.ReadySpec{}
	for i := 1; i <= n; i++ {
		port := freePort(t)
		vars[fmt.Sprintf("socks_port_%d", i)] = fmt.Sprintf("%d", port)
		vars[fmt.Sprintf("socks_addr_%d", i)] = fmt.Sprintf("127.0.0.1:%d", port)
		command += fmt.Sprintf(" -l socks5://127.0.0.1:{{socks_port_%d}}", i)
		wait = append(wait, runner.ReadySpec{Kind: runner.ReadyTCPListen, Target: fmt.Sprintf("{{socks_addr_%d}}", i), Timeout: 15 * time.Second})
	}
	command += " -a {{alias}} --debug"
	wait = append(wait, runner.ReadySpec{Kind: runner.ReadyDelayOnly, Timeout: 500 * time.Millisecond})

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars:       vars,
		Processes: []runner.ProcessSpec{
			serverConsoleProcess(serverAddr, "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub"),
			{
				Name:    "client",
				Mode:    "console_run",
				Command: command,
				Wait:    wait,
			},
		},
	}
}

func buildConnHubFixture(lb string) func(*testing.T) runner.FixtureSpec {
	return func(t *testing.T) runner.FixtureSpec {
		tcpPort := freePort(t)
		udpPort := freeUDPPort(t)
		wsPort := freePort(t)
		id := atomic.AddUint32(&testCounter, 1)
		report := reportPath(t, "connhub_report.json")
		serverVars := runner.Vars{
			"tcp_port":    fmt.Sprintf("%d", tcpPort),
			"udp_port":    fmt.Sprintf("%d", udpPort),
			"ws_port":     fmt.Sprintf("%d", wsPort),
			"tcp_addr":    fmt.Sprintf("127.0.0.1:%d", tcpPort),
			"udp_addr":    fmt.Sprintf("127.0.0.1:%d", udpPort),
			"ws_addr":     fmt.Sprintf("127.0.0.1:%d", wsPort),
			"socks_port":  fmt.Sprintf("%d", freePort(t)),
			"alias":       fmt.Sprintf("hub_%d", id),
			"report_path": report,
			"lb":          lb,
		}
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("hub-ok"))
		}))
		t.Cleanup(target.Close)
		serverVars["target_url"] = target.URL

		return runner.FixtureSpec{
			HelperTest: e2eHelperTestPattern,
			Vars:       serverVars,
			Processes: []runner.ProcessSpec{
				{
					Name:    "server",
					Mode:    "runner_run",
					Command: "-s tcp://0.0.0.0:{{tcp_port}}/?wrapper=raw -s udp://0.0.0.0:{{udp_port}}/?wrapper=raw -s ws://0.0.0.0:{{ws_port}}/hub?wrapper=raw -i 127.0.0.1 --no-sub",
					Wait: []runner.ReadySpec{
						{Kind: runner.ReadyTCPListen, Target: "{{tcp_addr}}", Timeout: 10 * time.Second},
						{Kind: runner.ReadyDelayOnly, Timeout: 1500 * time.Millisecond},
					},
				},
				{
					Name:        "client",
					Mode:        "connhub_client_report",
					TestTimeout: 120 * time.Second,
					Env: runner.Vars{
						"REM_TCP_ADDR":    "{{tcp_addr}}",
						"REM_UDP_ADDR":    "{{udp_addr}}",
						"REM_WS_ADDR":     "{{ws_addr}}",
						"REM_SOCKS_PORT":  "{{socks_port}}",
						"REM_ALIAS":       "{{alias}}",
						"REM_TARGET_URL":  "{{target_url}}",
						"REM_REPORT_PATH": "{{report_path}}",
						"REM_LB":          "{{lb}}",
						"REM_REQ_COUNT":   "45",
					},
					Wait: []runner.ReadySpec{{Kind: runner.ReadyReportFile, Target: "{{report_path}}", Timeout: 120 * time.Second}},
				},
			},
		}
	}
}

func buildKeepaliveFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"socks_port":  fmt.Sprintf("%d", socksPort),
			"socks_addr":  fmt.Sprintf("127.0.0.1:%d", socksPort),
			"alias":       fmt.Sprintf("keepalive_%d", atomic.AddUint32(&testCounter, 1)),
		},
		Processes: []runner.ProcessSpec{
			{
				Name:        "server",
				Mode:        "keepalive_console",
				TestTimeout: 120 * time.Second,
				Command:     "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub",
				Wait:        []runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
			},
			{
				Name:        "client",
				Mode:        "keepalive_console",
				TestTimeout: 120 * time.Second,
				Command:     "-c tcp://{{server_addr}}/?wrapper=raw -l socks5://127.0.0.1:{{socks_port}} -a {{alias}} --debug",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 500 * time.Millisecond},
				},
			},
		},
	}
}

func buildReconfigureFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	alias := fmt.Sprintf("reconfigure_%d", atomic.AddUint32(&testCounter, 1))
	report := reportPath(t, "reconfigure_report.json")

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"alias":       alias,
			"report_path": report,
		},
		Processes: []runner.ProcessSpec{
			{
				Name:        "server",
				Mode:        "reconfigure_server",
				TestTimeout: 60 * time.Second,
				Command:     "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub",
				Env:         runner.Vars{"REM_EXPECTED_ALIAS": "{{alias}}"},
				Wait:        []runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second}},
			},
			{
				Name:        "client",
				Mode:        "reconfigure_client_report",
				TestTimeout: 60 * time.Second,
				Command:     "-c tcp://{{server_addr}}/?wrapper=raw -l socks5://127.0.0.1:0 -a {{alias}} --debug",
				Env:         runner.Vars{"REM_REPORT_PATH": "{{report_path}}"},
				Wait:        []runner.ReadySpec{{Kind: runner.ReadyReportFile, Target: "{{report_path}}", Timeout: 30 * time.Second}},
			},
		},
	}
}

func serverConsoleProcess(serverAddr, command string) runner.ProcessSpec {
	return runner.ProcessSpec{
		Name:    "server",
		Mode:    "console_run",
		Command: command,
		Wait:    []runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: serverAddr, Timeout: 10 * time.Second}},
	}
}

// ---------------------------------------------------------------------------
// Topology: 10 nodes, 30 serves
// ---------------------------------------------------------------------------

func init() {
	registerConsoleCases(runner.CaseSpec{
		Name:  "relay_10nodes_30serves",
		Build: buildRelay10NodesFixture,
		Scenarios: []runner.Scenario{
			relay10NodesScenario(),
		},
	})
}

func buildRelay10NodesFixture(t *testing.T) runner.FixtureSpec {
	id := atomic.AddUint32(&testCounter, 1)
	sfx := fmt.Sprintf("-%d", id)

	serverPort := freePort(t)
	subPort := freePort(t)
	relayAPort := freePort(t)
	relayBPort := freePort(t)
	relayDPort := freePort(t)
	relayHPort := freePort(t)

	socksA := freePort(t)
	socksB := freePort(t)
	socksC := freePort(t)
	socksD := freePort(t)
	socksE := freePort(t)
	socksF := freePort(t)
	socksG := freePort(t)
	socksH := freePort(t)
	socksI := freePort(t)
	socksJ := freePort(t)

	httpA := freePort(t)
	pfwdA := freePort(t)
	pfwdB := freePort(t)
	pfwdD1 := freePort(t)
	pfwdD2 := freePort(t)

	vars := runner.Vars{
		"suffix":        sfx,
		"server_port":   fmt.Sprintf("%d", serverPort),
		"server_addr":   fmt.Sprintf("127.0.0.1:%d", serverPort),
		"sub_port":      fmt.Sprintf("%d", subPort),
		"topology_url":  fmt.Sprintf("http://127.0.0.1:%d/topology", subPort),
		"relay_a_port":  fmt.Sprintf("%d", relayAPort),
		"relay_a_addr":  fmt.Sprintf("127.0.0.1:%d", relayAPort),
		"relay_b_port":  fmt.Sprintf("%d", relayBPort),
		"relay_b_addr":  fmt.Sprintf("127.0.0.1:%d", relayBPort),
		"relay_d_port":  fmt.Sprintf("%d", relayDPort),
		"relay_d_addr":  fmt.Sprintf("127.0.0.1:%d", relayDPort),
		"relay_h_port":  fmt.Sprintf("%d", relayHPort),
		"relay_h_addr":  fmt.Sprintf("127.0.0.1:%d", relayHPort),
		"socks_a_port":  fmt.Sprintf("%d", socksA),
		"socks_a_addr":  fmt.Sprintf("127.0.0.1:%d", socksA),
		"socks_b_port":  fmt.Sprintf("%d", socksB),
		"socks_b_addr":  fmt.Sprintf("127.0.0.1:%d", socksB),
		"socks_c_port":  fmt.Sprintf("%d", socksC),
		"socks_c_addr":  fmt.Sprintf("127.0.0.1:%d", socksC),
		"socks_d_port":  fmt.Sprintf("%d", socksD),
		"socks_d_addr":  fmt.Sprintf("127.0.0.1:%d", socksD),
		"socks_e_port":  fmt.Sprintf("%d", socksE),
		"socks_e_addr":  fmt.Sprintf("127.0.0.1:%d", socksE),
		"socks_f_port":  fmt.Sprintf("%d", socksF),
		"socks_f_addr":  fmt.Sprintf("127.0.0.1:%d", socksF),
		"socks_g_port":  fmt.Sprintf("%d", socksG),
		"socks_g_addr":  fmt.Sprintf("127.0.0.1:%d", socksG),
		"socks_h_port":  fmt.Sprintf("%d", socksH),
		"socks_h_addr":  fmt.Sprintf("127.0.0.1:%d", socksH),
		"socks_i_port":  fmt.Sprintf("%d", socksI),
		"socks_i_addr":  fmt.Sprintf("127.0.0.1:%d", socksI),
		"socks_j_port":  fmt.Sprintf("%d", socksJ),
		"socks_j_addr":  fmt.Sprintf("127.0.0.1:%d", socksJ),
		"http_a_port":   fmt.Sprintf("%d", httpA),
		"pfwd_a_port":   fmt.Sprintf("%d", pfwdA),
		"pfwd_b_port":   fmt.Sprintf("%d", pfwdB),
		"pfwd_d1_port":  fmt.Sprintf("%d", pfwdD1),
		"pfwd_d2_port":  fmt.Sprintf("%d", pfwdD2),
		"relay_a_alias": "relay-A" + sfx,
		"relay_b_alias": "relay-B" + sfx,
		"relay_d_alias": "relay-D" + sfx,
		"relay_h_alias": "relay-H" + sfx,
		"node_e_alias":  "node-E" + sfx,
		"node_i_alias":  "node-I" + sfx,
	}

	processes := []runner.ProcessSpec{
		{
			Name:    "server",
			Mode:    "console_run",
			Command: "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --sub http://0.0.0.0:{{sub_port}}/sub",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{server_addr}}", Timeout: 10 * time.Second},
				{Kind: runner.ReadyTCPListen, Target: "127.0.0.1:{{sub_port}}", Timeout: 10 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "relay_a",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -s tcp://0.0.0.0:{{relay_a_port}} -a {{relay_a_alias}} -i 127.0.0.1 -r socks5://remno1:0onmer@0.0.0.0:{{socks_a_port}} -r http://0.0.0.0:{{http_a_port}} -r raw://0.0.0.0:{{pfwd_a_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{relay_a_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyTCPListen, Target: "{{socks_a_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "relay_d",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -s tcp://0.0.0.0:{{relay_d_port}} -a {{relay_d_alias}} -i 127.0.0.1 -r socks5://remno1:0onmer@0.0.0.0:{{socks_d_port}} -r raw://0.0.0.0:{{pfwd_d1_port}} -r raw://0.0.0.0:{{pfwd_d2_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{relay_d_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyTCPListen, Target: "{{socks_d_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "node_j",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{server_addr}}/?wrapper=raw -a node-J{{suffix}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_j_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{socks_j_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "relay_b",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{relay_a_addr}}/?wrapper=raw&via={{relay_a_alias}} -s tcp://0.0.0.0:{{relay_b_port}} -a {{relay_b_alias}} -i 127.0.0.1 -r socks5://remno1:0onmer@0.0.0.0:{{socks_b_port}} -r raw://0.0.0.0:{{pfwd_b_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{relay_b_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyTCPListen, Target: "{{socks_b_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "node_c",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{relay_a_addr}}/?wrapper=raw&via={{relay_a_alias}} -a node-C{{suffix}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_c_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{socks_c_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "node_g",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{relay_d_addr}}/?wrapper=raw&via={{relay_d_alias}} -a node-G{{suffix}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_g_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{socks_g_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "relay_h",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{relay_d_addr}}/?wrapper=raw&via={{relay_d_alias}} -s tcp://0.0.0.0:{{relay_h_port}} -a {{relay_h_alias}} -i 127.0.0.1 -r socks5://remno1:0onmer@0.0.0.0:{{socks_h_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{relay_h_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyTCPListen, Target: "{{socks_h_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "node_e",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{relay_b_addr}}/?wrapper=raw&via={{relay_b_alias}} -a {{node_e_alias}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_e_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{socks_e_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "node_f",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{relay_b_addr}}/?wrapper=raw&via={{relay_b_alias}} -a node-F{{suffix}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_f_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{socks_f_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "node_i",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{relay_h_addr}}/?wrapper=raw&via={{relay_h_alias}} -a {{node_i_alias}} -r socks5://remno1:0onmer@0.0.0.0:{{socks_i_port}}",
			Wait: []runner.ReadySpec{
				{Kind: runner.ReadyTCPListen, Target: "{{socks_i_addr}}", Timeout: 15 * time.Second},
				{Kind: runner.ReadyDelayOnly, Timeout: 1 * time.Second},
			},
		},
		{
			Name:    "route_e_to_g",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{relay_b_addr}}/?wrapper=raw&via={{relay_b_alias}} -d node-G{{suffix}} -n",
			Wait:    []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 1500 * time.Millisecond}},
		},
		{
			Name:    "route_i_to_e",
			Mode:    "console_run",
			Command: "--debug -c tcp://nonenonenonenone:@{{relay_h_addr}}/?wrapper=raw&via={{relay_h_alias}} -d {{node_e_alias}} -n",
			Wait:    []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 3 * time.Second}},
		},
	}

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars:       vars,
		Processes:  processes,
	}
}

func relay10NodesScenario() runner.Scenario {
	return runner.Scenario{
		Name: "deep_topology_smoke",
		Run: func(t *testing.T, fx *runner.Fixture) {
			targetURL := startHTTPTarget(t)
			assertSOCKS5Get(t, fx.Endpoint("socks_i_addr"), targetURL, 15*time.Second)
			assertSOCKS5Get(t, fx.Endpoint("socks_e_addr"), targetURL, 15*time.Second)

			resp, err := http.Get(fx.Endpoint("topology_url"))
			if err != nil {
				t.Fatalf("GET /topology: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Fatalf("topology HTTP %d: %s", resp.StatusCode, string(body))
			}

			var graph agent.TopologyGraph
			if err := json.Unmarshal(body, &graph); err != nil {
				t.Fatalf("unmarshal topology: %v", err)
			}
			if len(graph.Nodes) < 10 {
				t.Fatalf("expected >= 10 nodes, got %d", len(graph.Nodes))
			}

			var tunnelEdges, serveEdges int
			nodes := make(map[string]agent.TopologyNode, len(graph.Nodes))
			for _, node := range graph.Nodes {
				nodes[node.ID] = node
			}
			for _, edge := range graph.Edges {
				switch edge.Layer {
				case "tunnel":
					tunnelEdges++
				case "serve":
					serveEdges++
				}
			}
			if tunnelEdges < 10 {
				t.Fatalf("expected >= 10 tunnel edges, got %d", tunnelEdges)
			}
			if serveEdges < 2 {
				t.Fatalf("expected >= 2 serve edges, got %d", serveEdges)
			}

			for _, nodeID := range []string{
				fx.Var("relay_a_alias"),
				fx.Var("relay_b_alias"),
				fx.Var("relay_d_alias"),
				fx.Var("relay_h_alias"),
				fx.Var("node_e_alias"),
				fx.Var("node_i_alias"),
			} {
				node, ok := nodes[nodeID]
				if !ok {
					t.Fatalf("topology missing node %q", nodeID)
				}
				if nodeID == fx.Var("node_e_alias") || nodeID == fx.Var("node_i_alias") {
					if len(node.Services) == 0 {
						t.Fatalf("node %q should expose at least one service", nodeID)
					}
				}
			}
		},
	}
}
