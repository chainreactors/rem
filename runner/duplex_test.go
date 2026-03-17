//go:build !tinygo

package runner

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	_ "github.com/chainreactors/rem/protocol/serve/raw"
	_ "github.com/chainreactors/rem/protocol/serve/socks"
	"github.com/chainreactors/rem/protocol/tunnel"
	_ "github.com/chainreactors/rem/protocol/tunnel/tcp"
	_ "github.com/chainreactors/rem/protocol/tunnel/udp"
	_ "github.com/chainreactors/rem/protocol/wrapper"
	"github.com/chainreactors/rem/x/cryptor"
	"github.com/chainreactors/rem/x/utils"
	"golang.org/x/net/proxy"
)

func makeToken(key string) string {
	token, _ := cryptor.AesEncrypt([]byte(key), cryptor.PKCS7Padding([]byte(key), 16))
	return hex.EncodeToString(token)
}

// TestHelperDuplexServer is a subprocess that listens on TCP+UDP, accepts two
// connections, pairs them into a duplexConn, and runs a server agent.
// Env: REM_DUPLEX_SERVER=1, REM_TCP_PORT, REM_UDP_PORT
func TestHelperDuplexServer(t *testing.T) {
	if os.Getenv("REM_DUPLEX_SERVER") == "" {
		t.Skip("subprocess helper")
	}
	utils.Log.SetLevel(logs.DebugLevel)
	key := core.DefaultKey
	tok := makeToken(key)
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
	t.Logf("TCP listening on port %s", tcpPort)
	if _, err := udpTun.Listen(fmt.Sprintf("udp://0.0.0.0:%s", udpPort)); err != nil {
		t.Fatalf("Listen udp: %v", err)
	}
	t.Logf("UDP listening on port %s", udpPort)

	type acc struct {
		conn  net.Conn
		role  string
		login *message.Login
	}
	ch := make(chan acc, 2)
	acceptOne := func(tun *tunnel.TunnelService, label string) {
		for {
			conn, err := tun.Accept()
			if err != nil {
				t.Logf("[%s] Accept error: %v", label, err)
				return
			}
			msg, err := cio.ReadAndAssertMsg(conn, message.LoginMsg)
			if err != nil {
				t.Logf("[%s] bad conn (probe?): %v", label, err)
				conn.Close()
				continue
			}
			login := msg.(*message.Login)
			if tok != login.Token {
				cio.WriteMsg(conn, &message.Ack{Status: message.StatusFailed})
				conn.Close()
				continue
			}
			cio.WriteMsg(conn, &message.Ack{Status: message.StatusSuccess})
			ch <- acc{conn: conn, role: login.ChannelRole, login: login}
			return
		}
	}
	go acceptOne(tcpTun, "tcp")
	go acceptOne(udpTun, "udp")

	var upConn, downConn net.Conn
	var login *message.Login
	for i := 0; i < 2; i++ {
		a := <-ch
		if a.role == "up" {
			upConn = a.conn
			login = a.login
		} else {
			downConn = a.conn
		}
	}

	// Server: Read from upConn, Write to downConn
	dc := mergeHalfConn(upConn, downConn)

	ctrlMsg, err := cio.ReadAndAssertMsg(dc, message.ControlMsg)
	if err != nil {
		t.Fatalf("ReadAndAssertMsg control: %v", err)
	}
	cio.WriteMsg(dc, &message.Ack{Status: message.StatusSuccess})
	ctrl := ctrlMsg.(*message.Control)

	srv, err := agent.NewAgent(&agent.Config{
		Alias: login.Agent, Type: core.SERVER, Mod: ctrl.Mod,
		Redirect: ctrl.Destination, Controller: ctrl, Params: ctrl.Options,
		URLs: &core.URLs{
			ConsoleURL: login.ConsoleURL(),
			RemoteURL:  ctrl.LocalURL(),
			LocalURL:   ctrl.RemoteURL(),
		},
	})
	if err != nil {
		t.Fatalf("NewAgent server: %v", err)
	}
	srv.Conn = dc
	agent.Agents.Add(srv)
	srv.Handler() // blocks
}

// TestHelperDuplexClient is a subprocess that dials TCP+UDP, pairs them into
// a duplexConn, and runs a client agent with SOCKS5 inbound.
// Env: REM_DUPLEX_CLIENT=1, REM_TCP_ADDR, REM_UDP_ADDR, REM_SOCKS_PORT, REM_ALIAS
func TestHelperDuplexClient(t *testing.T) {
	if os.Getenv("REM_DUPLEX_CLIENT") == "" {
		t.Skip("subprocess helper")
	}
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
		Type: core.CLIENT, Mod: core.Proxy, Alias: alias,
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

	// Dial TCP (uplink)
	upConn, err := tcpTun.Dial(tcpAddr)
	if err != nil {
		t.Fatalf("Dial tcp: %v", err)
	}
	if _, err := cio.WriteAndAssertMsg(upConn, &message.Login{
		Agent: a.ID, Token: tokenStr, ChannelRole: "up",
		ConsoleProto: consoleURL.Scheme,
		ConsoleIP:    consoleURL.Hostname(), ConsolePort: consoleURL.IntPort(), Mod: a.Mod,
	}); err != nil {
		t.Fatalf("Login up: %v", err)
	}
	t.Log("TCP uplink login OK")

	// Dial UDP (downlink)
	downConn, err := udpTun.Dial(udpAddr)
	if err != nil {
		t.Fatalf("Dial udp: %v", err)
	}
	if _, err := cio.WriteAndAssertMsg(downConn, &message.Login{
		Agent: a.ID, Token: tokenStr, ChannelRole: "down",
		ConsoleProto: consoleURL.Scheme,
		ConsoleIP:    consoleURL.Hostname(), ConsolePort: consoleURL.IntPort(), Mod: a.Mod,
	}); err != nil {
		t.Fatalf("Login down: %v", err)
	}
	t.Log("UDP downlink login OK")

	// Client: Write to upConn, Read from downConn
	dc := mergeHalfConn(downConn, upConn)
	a.Conn = dc
	a.URLs.RemoteURL = remoteURL
	a.URLs.LocalURL = localURL
	if err := a.Dial(remoteURL, localURL); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	agent.Agents.Add(a)
	a.Handler() // blocks
}

// TestDuplexTunnel_TCPUpUDPDown launches server+client as subprocesses,
// then verifies HTTP through the duplex SOCKS5 proxy.
func TestDuplexTunnel_TCPUpUDPDown(t *testing.T) {
	tcpPort := freePort(t)
	udpPort := freeUDPPort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("duplex%d", tcpPort)

	// HTTP target
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello duplex"))
	}))
	defer ts.Close()

	// Server subprocess
	srvProc := exec.Command(os.Args[0], "-test.run=^TestHelperDuplexServer$", "-test.v")
	srvProc.Env = append(os.Environ(),
		"REM_DUPLEX_SERVER=1",
		fmt.Sprintf("REM_TCP_PORT=%d", tcpPort),
		fmt.Sprintf("REM_UDP_PORT=%d", udpPort),
	)
	srvProc.Stdout = os.Stdout
	srvProc.Stderr = os.Stderr
	if err := srvProc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srvProc.Process.Kill(); srvProc.Wait() })

	// Wait for TCP listener
	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", tcpPort), 10*time.Second)
	// UDP needs a small extra delay
	time.Sleep(1 * time.Second)

	// Client subprocess
	cliProc := exec.Command(os.Args[0], "-test.run=^TestHelperDuplexClient$", "-test.v")
	cliProc.Env = append(os.Environ(),
		"REM_DUPLEX_CLIENT=1",
		fmt.Sprintf("REM_TCP_ADDR=127.0.0.1:%d", tcpPort),
		fmt.Sprintf("REM_UDP_ADDR=127.0.0.1:%d", udpPort),
		fmt.Sprintf("REM_SOCKS_PORT=%d", socksPort),
		fmt.Sprintf("REM_ALIAS=%s", alias),
	)
	cliProc.Stdout = os.Stdout
	cliProc.Stderr = os.Stderr
	if err := cliProc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cliProc.Process.Kill(); cliProc.Wait() })

	// Wait for SOCKS5
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(500 * time.Millisecond)

	// Verify
	dialer, err := proxy.SOCKS5("tcp", socksAddr, socksAuth(), proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	httpCli := &http.Client{
		Transport: &http.Transport{Dial: dialer.Dial},
		Timeout:   10 * time.Second,
	}
	resp, err := httpCli.Get(ts.URL)
	if err != nil {
		t.Fatalf("HTTP GET through duplex SOCKS5: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello duplex" {
		t.Fatalf("expected %q, got %q", "hello duplex", body)
	}
	t.Log("duplex tunnel TCP-up + UDP-down SOCKS5: PASSED")
}
