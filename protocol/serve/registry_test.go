package serve_test

import (
	"testing"

	"github.com/chainreactors/logs"
	_ "github.com/chainreactors/rem/protocol/serve/externalc2"
	_ "github.com/chainreactors/rem/protocol/serve/http"
	_ "github.com/chainreactors/rem/protocol/serve/portforward"
	_ "github.com/chainreactors/rem/protocol/serve/raw"
	_ "github.com/chainreactors/rem/protocol/serve/shadowsocks"
	_ "github.com/chainreactors/rem/protocol/serve/socks"
	_ "github.com/chainreactors/rem/protocol/serve/trojan"

	"github.com/chainreactors/rem/internal/servetest"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
)

func TestInboundMatrix(t *testing.T) {
	ensureServeLogger()

	cases := []struct {
		rawURL string
	}{
		{rawURL: "raw://127.0.0.1:0?dest=localhost:8443"},
		{rawURL: "socks://alice:secret@127.0.0.1:0"},
		{rawURL: "http://127.0.0.1:0"},
		{rawURL: "ss://rem:secret@127.0.0.1:0?cipher=aes-256-gcm"},
		{rawURL: "trojan://rem:secret@127.0.0.1:0"},
		{rawURL: "forward://127.0.0.1:0?dest=localhost:8080"},
		{rawURL: "cs://127.0.0.1:0?dest=127.0.0.1:50050"},
	}

	for _, tc := range cases {
		tc := tc
		parsed := mustParseServeURL(t, tc.rawURL)
		t.Run(parsed.String(), func(t *testing.T) {
			inbound, err := core.InboundCreate(parsed.Scheme, parsed.Options())
			if err != nil {
				t.Fatalf("InboundCreate(%q) error: %v", parsed.String(), err)
			}
			if inbound == nil {
				t.Fatal("InboundCreate returned nil inbound")
			}
			_ = inbound.ToClash()
		})
	}
}

func TestOutboundMatrix(t *testing.T) {
	ensureServeLogger()

	dialer := &servetest.Dialer{Conn: servetest.NewConn(nil)}
	cases := []struct {
		rawURL       string
		expectedName string
	}{
		{rawURL: "raw://127.0.0.1:0", expectedName: core.Socks5Serve},
		{rawURL: "socks://alice:secret@127.0.0.1:0", expectedName: core.Socks5Serve},
		{rawURL: "http://127.0.0.1:0", expectedName: core.HTTPServe},
		{rawURL: "ss://rem:secret@127.0.0.1:0?cipher=aes-256-gcm", expectedName: core.ShadowSocksServe},
		{rawURL: "trojan://rem:secret@127.0.0.1:0", expectedName: core.TrojanServe},
		{rawURL: "forward://127.0.0.1:0?dest=localhost:8080", expectedName: core.PortForwardServe},
		{rawURL: "cs://127.0.0.1:0?dest=127.0.0.1:50050", expectedName: core.CobaltStrikeServe},
	}

	for _, tc := range cases {
		tc := tc
		parsed := mustParseServeURL(t, tc.rawURL)
		t.Run(parsed.String(), func(t *testing.T) {
			outbound, err := core.OutboundCreate(parsed.Scheme, parsed.Options(), dialer)
			if err != nil {
				t.Fatalf("OutboundCreate(%q) error: %v", parsed.String(), err)
			}
			if outbound == nil {
				t.Fatal("OutboundCreate returned nil outbound")
			}
			if got := outbound.Name(); got != tc.expectedName {
				t.Fatalf("unexpected outbound name: got %q want %q", got, tc.expectedName)
			}
		})
	}
}

func mustParseServeURL(t *testing.T, raw string) *core.URL {
	t.Helper()

	parsed, err := core.NewURL(raw)
	if err != nil {
		t.Fatalf("NewURL(%q) error: %v", raw, err)
	}
	return parsed
}

func ensureServeLogger() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}
