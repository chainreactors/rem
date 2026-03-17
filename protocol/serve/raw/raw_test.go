package raw

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/internal/servetest"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}

func TestNewRawInboundSupportsDomainDestinations(t *testing.T) {
	plugI, err := NewRawInbound(map[string]string{"dest": "localhost:8443"})
	if err != nil {
		t.Fatalf("NewRawInbound returned error: %v", err)
	}

	plug := plugI.(*RawPlugin)
	if plug.Dest.DestAddr.FQDN != "localhost" {
		t.Fatalf("FQDN mismatch: got %q", plug.Dest.DestAddr.FQDN)
	}
	if plug.Dest.DestAddr.Port != 8443 {
		t.Fatalf("port mismatch: got %d", plug.Dest.DestAddr.Port)
	}
}

func TestNewRawInboundSupportsIPv6Destinations(t *testing.T) {
	plugI, err := NewRawInbound(map[string]string{"dest": "[2001:db8::1]:9443"})
	if err != nil {
		t.Fatalf("NewRawInbound returned error: %v", err)
	}

	plug := plugI.(*RawPlugin)
	wantIP := net.ParseIP("2001:db8::1")
	if !plug.Dest.DestAddr.IP.Equal(wantIP) {
		t.Fatalf("IP mismatch: want %v, got %v", wantIP, plug.Dest.DestAddr.IP)
	}
	if plug.Dest.DestAddr.Port != 9443 {
		t.Fatalf("port mismatch: got %d", plug.Dest.DestAddr.Port)
	}
}

func TestNewRawInboundRequiresDest(t *testing.T) {
	_, err := NewRawInbound(map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "dest not found") {
		t.Fatalf("expected dest not found error, got %v", err)
	}
}

func TestNewRawInboundRejectsInvalidDest(t *testing.T) {
	_, err := NewRawInbound(map[string]string{"dest": "invalid-dest"})
	if err == nil || !strings.Contains(err.Error(), "invalid dest") {
		t.Fatalf("expected invalid dest error, got %v", err)
	}
}

func TestRelayReturnsBridgeWriteError(t *testing.T) {
	plugI, err := NewRawInbound(map[string]string{"dest": "localhost:8443"})
	if err != nil {
		t.Fatalf("NewRawInbound returned error: %v", err)
	}

	plug := plugI.(*RawPlugin)
	bridgeErr := errors.New("bridge write failed")
	bridge := servetest.NewReadWriteCloser(nil)
	bridge.WriteErr = bridgeErr
	_, err = plug.Relay(servetest.NewConn(nil), bridge)
	if !errors.Is(err, bridgeErr) {
		t.Fatalf("expected bridge write error, got %v", err)
	}
}

func TestRelayReturnsReplyReadError(t *testing.T) {
	plugI, err := NewRawInbound(map[string]string{"dest": "localhost:8443"})
	if err != nil {
		t.Fatalf("NewRawInbound returned error: %v", err)
	}

	plug := plugI.(*RawPlugin)
	bridge := servetest.NewReadWriteCloser(servetest.SOCKS5Reply(0x05))
	_, err = plug.Relay(servetest.NewConn(nil), bridge)
	if err == nil || !strings.Contains(err.Error(), "SOCKS5 request failed") {
		t.Fatalf("expected reply read error, got %v", err)
	}
}
