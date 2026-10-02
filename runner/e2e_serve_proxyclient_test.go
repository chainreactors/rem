//go:build !tinygo && proxyclient_shadowsocks

package runner

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/rem/harness/runner"
	_ "github.com/chainreactors/rem/protocol/serve/shadowsocks"
	_ "github.com/chainreactors/rem/protocol/serve/trojan"
	ss "github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
)

const proxyclientTestPassword = "testpass"

func init() {
	registerServeCases(
		runner.CaseSpec{
			Name:  "proxyclient_ss_outbound",
			Build: buildSSOutboundFixture,
			Scenarios: []runner.Scenario{
				serveHTTPSmokeScenario("hello ss outbound"),
			},
		},
		runner.CaseSpec{
			Name:  "proxyclient_ss_forward",
			Build: buildSSForwardFixture,
			Scenarios: []runner.Scenario{
				serveHTTPSmokeScenario("hello ss forward"),
			},
		},
		runner.CaseSpec{
			Name:  "serve_shadowsocks",
			Build: buildSSServeFixture,
			Scenarios: []runner.Scenario{
				ssServeEchoScenario("echo_smoke", []byte("hello ss serve")),
				ssServeLargePayloadScenario(128 * 1024),
				ssServeConcurrentScenario(5),
			},
		},
		runner.CaseSpec{
			Name:  "serve_trojan",
			Build: buildTrojanServeFixture,
			Scenarios: []runner.Scenario{
				trojanServeEchoScenario("echo_smoke", []byte("hello trojan serve")),
				trojanServeLargePayloadScenario(128 * 1024),
				trojanServeConcurrentScenario(5),
			},
		},
	)
}

func buildSSOutboundFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	alias := fmt.Sprintf("ss_out_%d", atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port":   fmt.Sprintf("%d", serverPort),
			"server_addr":   serverAddr,
			"socks_port":    fmt.Sprintf("%d", socksPort),
			"socks_addr":    fmt.Sprintf("127.0.0.1:%d", socksPort),
			"ss_proxy_addr": startSSProxyServer(t, proxyclientTestPassword, "aes-256-gcm"),
			"alias":         alias,
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: "--debug -s tcp://0.0.0.0:{{server_port}}/?wrapper=raw -i 127.0.0.1 --no-sub -x ss://aes-256-gcm:" + proxyclientTestPassword + "@{{ss_proxy_addr}}",
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

func buildSSForwardFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	socksPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	alias := fmt.Sprintf("ss_fwd_%d", atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port":   fmt.Sprintf("%d", serverPort),
			"server_addr":   serverAddr,
			"socks_port":    fmt.Sprintf("%d", socksPort),
			"socks_addr":    fmt.Sprintf("127.0.0.1:%d", socksPort),
			"ss_proxy_addr": startSSProxyServer(t, proxyclientTestPassword, "aes-256-gcm"),
			"alias":         alias,
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
				Command: "-c tcp://{{server_addr}}/?wrapper=raw -l socks5://127.0.0.1:{{socks_port}} -a {{alias}} --debug -f ss://aes-256-gcm:" + proxyclientTestPassword + "@{{ss_proxy_addr}}",
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 15 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 500 * time.Millisecond},
				},
			},
		},
	}
}

func buildSSServeFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	ssPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	alias := fmt.Sprintf("ss_srv_%d", atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"ss_port":     fmt.Sprintf("%d", ssPort),
			"ss_addr":     fmt.Sprintf("127.0.0.1:%d", ssPort),
			"echo_addr":   startEchoServer(t),
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
				Command: "-c tcp://{{server_addr}}/?wrapper=raw -l ss://0.0.0.0:{{ss_port}}/?password=" + proxyclientTestPassword + " -a {{alias}} --debug",
				// A raw TCP readiness probe is not protocol-neutral here: it creates
				// an invalid Shadowsocks bridge. Let the real protocol connection be
				// the first accepted connection instead.
				Wait: []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 2 * time.Second}},
			},
		},
	}
}

func buildTrojanServeFixture(t *testing.T) runner.FixtureSpec {
	serverPort := freePort(t)
	trojanPort := freePort(t)
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)
	alias := fmt.Sprintf("trojan_srv_%d", atomic.AddUint32(&testCounter, 1))

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"server_port": fmt.Sprintf("%d", serverPort),
			"server_addr": serverAddr,
			"trojan_port": fmt.Sprintf("%d", trojanPort),
			"trojan_addr": fmt.Sprintf("127.0.0.1:%d", trojanPort),
			"echo_addr":   startEchoServer(t),
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
				Command: "-c tcp://{{server_addr}}/?wrapper=raw -l trojan://0.0.0.0:{{trojan_port}}/?password=" + proxyclientTestPassword + " -a {{alias}} --debug",
				// A raw TCP readiness probe consumes a Trojan connection and can race
				// with bridge cleanup. Delay startup without touching the protocol port.
				Wait: []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 2 * time.Second}},
			},
		},
	}
}

func ssServeEchoScenario(name string, payload []byte) runner.Scenario {
	return runner.Scenario{
		Name: name,
		Run: func(t *testing.T, fx *runner.Fixture) {
			ssEchoRoundtrip(t, fx.Endpoint("ss_addr"), fx.Endpoint("echo_addr"), proxyclientTestPassword, payload)
		},
	}
}

func ssServeLargePayloadScenario(size int) runner.Scenario {
	return runner.Scenario{
		Name: "large_payload",
		Run: func(t *testing.T, fx *runner.Fixture) {
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i % 251)
			}
			ssEchoRoundtrip(t, fx.Endpoint("ss_addr"), fx.Endpoint("echo_addr"), proxyclientTestPassword, payload)
		},
	}
}

func ssServeConcurrentScenario(n int) runner.Scenario {
	return runner.Scenario{
		Name: "concurrent_conns",
		Run: func(t *testing.T, fx *runner.Fixture) {
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					msg := []byte(fmt.Sprintf("ss-conn-%d-hello", idx))
					conn := dialSS(t, fx.Endpoint("ss_addr"), fx.Endpoint("echo_addr"), proxyclientTestPassword)
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

func trojanServeEchoScenario(name string, payload []byte) runner.Scenario {
	return runner.Scenario{
		Name: name,
		Run: func(t *testing.T, fx *runner.Fixture) {
			trojanEchoRoundtrip(t, fx.Endpoint("trojan_addr"), fx.Endpoint("echo_addr"), proxyclientTestPassword, payload)
		},
	}
}

func trojanServeLargePayloadScenario(size int) runner.Scenario {
	return runner.Scenario{
		Name: "large_payload",
		Run: func(t *testing.T, fx *runner.Fixture) {
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i % 251)
			}
			trojanEchoRoundtrip(t, fx.Endpoint("trojan_addr"), fx.Endpoint("echo_addr"), proxyclientTestPassword, payload)
		},
	}
}

func trojanServeConcurrentScenario(n int) runner.Scenario {
	return runner.Scenario{
		Name: "concurrent_conns",
		Run: func(t *testing.T, fx *runner.Fixture) {
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					msg := []byte(fmt.Sprintf("trojan-conn-%d-hello", idx))
					conn := dialTrojan(t, fx.Endpoint("trojan_addr"), fx.Endpoint("echo_addr"), proxyclientTestPassword)
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
	t.Cleanup(func() { _ = ln.Close() })

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

				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(rc, sc); done <- struct{}{} }()
				go func() { _, _ = io.Copy(sc, rc); done <- struct{}{} }()
				<-done
			}(conn)
		}
	}()

	return ln.Addr().String()
}

func dialSS(t *testing.T, ssAddr, targetAddr, password string) net.Conn {
	t.Helper()

	ciph, err := ss.PickCipher("aes-256-gcm", nil, password)
	if err != nil {
		t.Fatalf("PickCipher: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	var conn net.Conn
	var lastErr error
	for time.Now().Before(deadline) {
		conn, lastErr = net.DialTimeout("tcp", ssAddr, time.Second)
		if lastErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial SS %s: %v", ssAddr, lastErr)
	}

	sc := ciph.StreamConn(conn)
	tgt := socks.ParseAddr(targetAddr)
	if tgt == nil {
		_ = conn.Close()
		t.Fatalf("ParseAddr(%s) returned nil", targetAddr)
	}
	if _, err := sc.Write(tgt); err != nil {
		_ = conn.Close()
		t.Fatalf("write SS target addr: %v", err)
	}
	return sc
}

func ssEchoRoundtrip(t *testing.T, ssAddr, echoAddr, password string, msg []byte) {
	t.Helper()
	conn := dialSS(t, ssAddr, echoAddr, password)
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
		t.Fatalf("echo mismatch: expected %q got %q", msg, buf)
	}
}

func dialTrojan(t *testing.T, trojanAddr, targetAddr, password string) net.Conn {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	var tlsConn *tls.Conn
	var lastErr error
	for time.Now().Before(deadline) {
		tlsConn, lastErr = tls.DialWithDialer(
			&net.Dialer{Timeout: time.Second},
			"tcp",
			trojanAddr,
			&tls.Config{InsecureSkipVerify: true},
		)
		if lastErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if tlsConn == nil {
		t.Fatalf("TLS dial %s: %v", trojanAddr, lastErr)
	}

	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		_ = tlsConn.Close()
		t.Fatalf("SplitHostPort(%s): %v", targetAddr, err)
	}
	token := sha224Hex(password)
	var header []byte
	header = append(header, []byte(token)...)
	header = append(header, 0x0D, 0x0A, 0x01)

	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil {
		header = append(header, 0x01)
		header = append(header, ip4...)
	} else if ip != nil {
		header = append(header, 0x04)
		header = append(header, ip.To16()...)
	} else {
		header = append(header, 0x03, byte(len(host)))
		header = append(header, []byte(host)...)
	}

	var portBuf [2]byte
	portNum := 0
	_, _ = fmt.Sscanf(portStr, "%d", &portNum)
	binary.BigEndian.PutUint16(portBuf[:], uint16(portNum))
	header = append(header, portBuf[:]...)
	header = append(header, 0x0D, 0x0A)

	if _, err := tlsConn.Write(header); err != nil {
		_ = tlsConn.Close()
		t.Fatalf("write trojan header: %v", err)
	}
	return tlsConn
}

func trojanEchoRoundtrip(t *testing.T, trojanAddr, echoAddr, password string, msg []byte) {
	t.Helper()
	conn := dialTrojan(t, trojanAddr, echoAddr, password)
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
		t.Fatalf("echo mismatch: expected %q got %q", msg, buf)
	}
}

func sha224Hex(s string) string {
	h := sha256.New224()
	_, _ = h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
