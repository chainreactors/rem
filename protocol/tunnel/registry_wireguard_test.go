//go:build !tinygo && !go1.21

package tunnel

import (
	"testing"

	_ "github.com/chainreactors/rem/protocol/tunnel/wireguard"

	"github.com/chainreactors/rem/protocol/core"
)

func TestRegisteredWireGuardTunnelConstructs(t *testing.T) {
	parsed := mustParseTunnelURL(t, "wireguard+raw://127.0.0.1:51820/rem?private_key=test-private&peer_public_key=test-peer&wrapper=raw")
	if got := parsed.Tunnel; got != core.WireGuardTunnel {
		t.Fatalf("unexpected tunnel parsed from wireguard url: got %q want %q", got, core.WireGuardTunnel)
	}

	if _, err := core.DialerCreate(core.WireGuardTunnel, newTunnelRegistryContext(parsed)); err != nil {
		t.Fatalf("DialerCreate(%q) error: %v", core.WireGuardTunnel, err)
	}
	if _, err := core.ListenerCreate(core.WireGuardTunnel, newTunnelRegistryContext(parsed)); err != nil {
		t.Fatalf("ListenerCreate(%q) error: %v", core.WireGuardTunnel, err)
	}
}
