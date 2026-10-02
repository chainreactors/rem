//go:build !tinygo

package runner

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/rem/harness/runner"
	"github.com/chainreactors/rem/protocol/core"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func init() {
	registerTunnelCases(
		runner.CaseSpec{
			Name:  "wireguard_transport",
			Check: wireGuardCapabilityCheck,
			Build: buildWireGuardFixture,
			Scenarios: []runner.Scenario{
				socksSmokeScenario("hello wireguard_transport"),
			},
		},
		runner.CaseSpec{
			Name:  "wireguard_multi_client",
			Check: wireGuardCapabilityCheck,
			Build: buildWireGuardMultiClientFixture,
			Scenarios: []runner.Scenario{
				wireGuardDualClientScenario(),
			},
		},
	)
}

func wireGuardCapabilityCheck(*testing.T) string {
	if hasRegisteredTunnel(core.GetRegisteredDialers(), core.WireGuardTunnel) &&
		hasRegisteredTunnel(core.GetRegisteredListeners(), core.WireGuardTunnel) {
		return ""
	}
	return "wireguard tunnel is not linked into this test binary; run under the Go 1.20/WireGuard build"
}

func buildWireGuardFixture(t *testing.T) runner.FixtureSpec {
	wgPort := freeUDPPort(t)
	socksPort := freePort(t)
	srvPriv, srvPub := genTestWGKeys(t)
	cliPriv, cliPub := genTestWGKeys(t)

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"wg_port":    fmt.Sprintf("%d", wgPort),
			"socks_port": fmt.Sprintf("%d", socksPort),
			"socks_addr": fmt.Sprintf("127.0.0.1:%d", socksPort),
			"srv_priv":   srvPriv,
			"srv_pub":    srvPub,
			"cli_priv":   cliPriv,
			"cli_pub":    cliPub,
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: fmt.Sprintf("--debug -s %s -i 127.0.0.1 --no-sub", buildTestWGServerURL(wgPort, srvPriv, []string{cliPub})),
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 2 * time.Second}},
			},
			{
				Name:    "client",
				Mode:    "console_run",
				Command: fmt.Sprintf("-c %s -l socks5://127.0.0.1:%d -a wg_%d --debug", buildTestWGClientURL(wgPort, cliPriv, srvPub), socksPort, atomic.AddUint32(&testCounter, 1)),
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr}}", Timeout: 30 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 500 * time.Millisecond},
				},
			},
		},
	}
}

func buildWireGuardMultiClientFixture(t *testing.T) runner.FixtureSpec {
	wgPort := freeUDPPort(t)
	socksPort1 := freePort(t)
	socksPort2 := freePort(t)
	id := atomic.AddUint32(&testCounter, 1)
	srvPriv, srvPub := genTestWGKeys(t)
	cli1Priv, cli1Pub := genTestWGKeys(t)
	cli2Priv, cli2Pub := genTestWGKeys(t)

	return runner.FixtureSpec{
		HelperTest: e2eHelperTestPattern,
		Vars: runner.Vars{
			"socks_port_1": fmt.Sprintf("%d", socksPort1),
			"socks_addr_1": fmt.Sprintf("127.0.0.1:%d", socksPort1),
			"socks_port_2": fmt.Sprintf("%d", socksPort2),
			"socks_addr_2": fmt.Sprintf("127.0.0.1:%d", socksPort2),
		},
		Processes: []runner.ProcessSpec{
			{
				Name:    "server",
				Mode:    "console_run",
				Command: fmt.Sprintf("--debug -s %s -i 127.0.0.1 --no-sub", buildTestWGServerURL(wgPort, srvPriv, []string{cli1Pub, cli2Pub})),
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyDelayOnly, Timeout: 2 * time.Second}},
			},
			{
				Name:    "client_1",
				Mode:    "console_run",
				Command: fmt.Sprintf("-c %s -l socks5://127.0.0.1:%d -a wgmc1_%d --debug", buildTestWGClientURL(wgPort, cli1Priv, srvPub), socksPort1, id),
				Wait:    []runner.ReadySpec{{Kind: runner.ReadyTCPListen, Target: "{{socks_addr_1}}", Timeout: 30 * time.Second}},
			},
			{
				Name:    "client_2",
				Mode:    "console_run",
				Command: fmt.Sprintf("-c %s -l socks5://127.0.0.1:%d -a wgmc2_%d --debug", buildTestWGClientURL(wgPort, cli2Priv, srvPub), socksPort2, id),
				Wait: []runner.ReadySpec{
					{Kind: runner.ReadyTCPListen, Target: "{{socks_addr_2}}", Timeout: 30 * time.Second},
					{Kind: runner.ReadyDelayOnly, Timeout: 500 * time.Millisecond},
				},
			},
		},
	}
}

func wireGuardDualClientScenario() runner.Scenario {
	return runner.Scenario{
		Name: "dual_client_http",
		Run: func(t *testing.T, fx *runner.Fixture) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("multi-client-ok"))
			}))
			defer ts.Close()

			for _, endpoint := range []string{"socks_addr_1", "socks_addr_2"} {
				verifyHTTP(t, fx.Endpoint(endpoint), ts.URL, "multi-client-ok")
			}

			const numPerClient = 3
			var wg sync.WaitGroup
			errs := make(chan error, numPerClient*2)
			for idx, endpoint := range []string{"socks_addr_1", "socks_addr_2"} {
				client := newSOCKS5Client(t, fx.Endpoint(endpoint), 15*time.Second)
				for req := 0; req < numPerClient; req++ {
					wg.Add(1)
					go func(clientIdx, reqIdx int, httpClient *http.Client) {
						defer wg.Done()
						resp, err := httpClient.Get(ts.URL)
						if err != nil {
							errs <- fmt.Errorf("client%d req%d: %w", clientIdx+1, reqIdx, err)
							return
						}
						defer resp.Body.Close()
						body, _ := io.ReadAll(resp.Body)
						if string(body) != "multi-client-ok" {
							errs <- fmt.Errorf("client%d req%d: expected %q got %q", clientIdx+1, reqIdx, "multi-client-ok", body)
						}
					}(idx, req, client)
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

func genTestWGKeys(t *testing.T) (privHex, pubHex string) {
	t.Helper()
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PublicKey()
	return hex.EncodeToString(priv[:]), hex.EncodeToString(pub[:])
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

func buildTestWGURL(host string, port int, q url.Values) string {
	u := &url.URL{
		Scheme: "wireguard",
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   "/",
	}
	u.RawQuery = q.Encode()
	return u.String()
}
