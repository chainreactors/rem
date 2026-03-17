//go:build !tinygo

package tunnel

import (
	"context"
	"testing"

	_ "github.com/chainreactors/rem/protocol/tunnel/dns"
	_ "github.com/chainreactors/rem/protocol/tunnel/http"
	_ "github.com/chainreactors/rem/protocol/tunnel/http2"
	_ "github.com/chainreactors/rem/protocol/tunnel/icmp"
	_ "github.com/chainreactors/rem/protocol/tunnel/memory"
	_ "github.com/chainreactors/rem/protocol/tunnel/simplex"
	_ "github.com/chainreactors/rem/protocol/tunnel/streamhttp"
	_ "github.com/chainreactors/rem/protocol/tunnel/tcp"
	_ "github.com/chainreactors/rem/protocol/tunnel/udp"
	_ "github.com/chainreactors/rem/protocol/tunnel/unix"
	_ "github.com/chainreactors/rem/protocol/tunnel/websocket"

	"github.com/chainreactors/rem/protocol/core"
)

func TestRegisteredTunnelDialersConstruct(t *testing.T) {
	cases := []struct {
		rawURL     string
		tunnelName string
	}{
		{rawURL: "tcp+raw://127.0.0.1:34996/rem?wrapper=raw", tunnelName: core.TCPTunnel},
		{rawURL: "udp+raw://127.0.0.1:34996/rem?wrapper=raw", tunnelName: core.UDPTunnel},
		{rawURL: "http+raw://127.0.0.1:8080/rem?wrapper=raw", tunnelName: core.HTTPTunnel},
		{rawURL: "http2+raw://127.0.0.1:8443/rem?wrapper=raw", tunnelName: core.HTTP2Tunnel},
		{rawURL: "streamhttp+raw://127.0.0.1:8080/rem?wrapper=raw", tunnelName: core.StreamHTTPTunnel},
		{rawURL: "websocket+raw://127.0.0.1:8080/rem?wrapper=raw", tunnelName: core.WebSocketTunnel},
		{rawURL: "unix+raw://127.0.0.1:0/rem?pipe=rem-test&wrapper=raw", tunnelName: core.UNIXTunnel},
		{rawURL: "memory+socks://alice:secret@rem-pipe?wrapper=raw", tunnelName: core.MemoryTunnel},
		{rawURL: "dns+raw://127.0.0.1:5353/rem?domain=test.local&wrapper=raw", tunnelName: core.DNSTunnel},
		{rawURL: "simplex+sharepoint:///rem?tenant=test-tenant&client=test-client&secret=test-secret&site=test-site&wrapper=raw", tunnelName: core.SimplexTunnel},
		{rawURL: "icmp+raw://127.0.0.1:0/rem?wrapper=raw", tunnelName: core.ICMPTunnel},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.tunnelName, func(t *testing.T) {
			parsed := mustParseTunnelURL(t, tc.rawURL)
			if got := parsed.Tunnel; got != tc.tunnelName {
				t.Fatalf("unexpected tunnel parsed from %q: got %q want %q", tc.rawURL, got, tc.tunnelName)
			}
			if _, err := core.DialerCreate(tc.tunnelName, newTunnelRegistryContext(parsed)); err != nil {
				t.Fatalf("DialerCreate(%q) error: %v", tc.tunnelName, err)
			}
		})
	}
}

func TestRegisteredTunnelListenersConstruct(t *testing.T) {
	cases := []struct {
		rawURL     string
		tunnelName string
	}{
		{rawURL: "tcp+raw://127.0.0.1:34996/rem?wrapper=raw", tunnelName: core.TCPTunnel},
		{rawURL: "udp+raw://127.0.0.1:34996/rem?wrapper=raw", tunnelName: core.UDPTunnel},
		{rawURL: "http+raw://127.0.0.1:8080/rem?wrapper=raw", tunnelName: core.HTTPTunnel},
		{rawURL: "http2+raw://127.0.0.1:8443/rem?wrapper=raw", tunnelName: core.HTTP2Tunnel},
		{rawURL: "streamhttp+raw://127.0.0.1:8080/rem?wrapper=raw", tunnelName: core.StreamHTTPTunnel},
		{rawURL: "websocket+raw://127.0.0.1:8080/rem?wrapper=raw", tunnelName: core.WebSocketTunnel},
		{rawURL: "unix+raw://127.0.0.1:0/rem?pipe=rem-test&wrapper=raw", tunnelName: core.UNIXTunnel},
		{rawURL: "memory+socks://alice:secret@rem-pipe?wrapper=raw", tunnelName: core.MemoryTunnel},
		{rawURL: "dns+raw://127.0.0.1:5353/rem?domain=test.local&wrapper=raw", tunnelName: core.DNSTunnel},
		{rawURL: "simplex+sharepoint:///rem?tenant=test-tenant&client=test-client&secret=test-secret&site=test-site&wrapper=raw", tunnelName: core.SimplexTunnel},
		{rawURL: "icmp+raw://127.0.0.1:0/rem?wrapper=raw", tunnelName: core.ICMPTunnel},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.tunnelName, func(t *testing.T) {
			parsed := mustParseTunnelURL(t, tc.rawURL)
			if got := parsed.Tunnel; got != tc.tunnelName {
				t.Fatalf("unexpected tunnel parsed from %q: got %q want %q", tc.rawURL, got, tc.tunnelName)
			}
			if _, err := core.ListenerCreate(tc.tunnelName, newTunnelRegistryContext(parsed)); err != nil {
				t.Fatalf("ListenerCreate(%q) error: %v", tc.tunnelName, err)
			}
		})
	}
}

func mustParseTunnelURL(t *testing.T, raw string) *core.URL {
	t.Helper()

	parsed, err := core.NewURL(raw)
	if err != nil {
		t.Fatalf("NewURL(%q) error: %v", raw, err)
	}
	return parsed
}

func newTunnelRegistryContext(u *core.URL) context.Context {
	meta := core.Metas{}
	if u != nil {
		meta["url"] = u
	}
	return context.WithValue(context.Background(), "meta", meta)
}
