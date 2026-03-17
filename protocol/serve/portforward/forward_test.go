package portforward

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/internal/servetest"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}

func TestNewForwardInboundSupportsDomainDestinations(t *testing.T) {
	plugI, err := NewForwardInbound(map[string]string{"dest": "localhost:1080"})
	if err != nil {
		t.Fatalf("NewForwardInbound returned error: %v", err)
	}

	plug := plugI.(*ForwardPlugin)
	if plug.Dest.DestAddr.FQDN != "localhost" {
		t.Fatalf("FQDN mismatch: got %q", plug.Dest.DestAddr.FQDN)
	}
	if plug.Dest.DestAddr.Port != 1080 {
		t.Fatalf("port mismatch: got %d", plug.Dest.DestAddr.Port)
	}
}

func TestNewForwardOutboundSupportsIPv6Destinations(t *testing.T) {
	plugI, err := NewForwardOutbound(
		map[string]string{"dest": "[2001:db8::2]:1081"},
		core.NewProxyDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewForwardOutbound returned error: %v", err)
	}

	plug := plugI.(*ForwardPlugin)
	wantIP := net.ParseIP("2001:db8::2")
	if !plug.Dest.DestAddr.IP.Equal(wantIP) {
		t.Fatalf("IP mismatch: want %v, got %v", wantIP, plug.Dest.DestAddr.IP)
	}
	if plug.Dest.DestAddr.Port != 1081 {
		t.Fatalf("port mismatch: got %d", plug.Dest.DestAddr.Port)
	}
}

func TestNewForwardInboundRequiresDest(t *testing.T) {
	_, err := NewForwardInbound(map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "dest not found") {
		t.Fatalf("expected dest not found error, got %v", err)
	}
}

func TestNewForwardOutboundRejectsInvalidDest(t *testing.T) {
	_, err := NewForwardOutbound(map[string]string{"dest": "invalid-dest"}, core.NewProxyDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, nil
	}))
	if err == nil || !strings.Contains(err.Error(), "invalid dest") {
		t.Fatalf("expected invalid dest error, got %v", err)
	}
}

func TestHandleRequiresDialer(t *testing.T) {
	plug := &ForwardPlugin{Dest: mustForwardRequest(t, "localhost:8080")}
	_, err := plug.Handle(servetest.NewReadWriteCloser(nil), servetest.NewConn(nil))
	if err == nil || !strings.Contains(err.Error(), "dialer not configured") {
		t.Fatalf("expected dialer error, got %v", err)
	}
}

func TestHandleUsesDialableAddressForDomainDest(t *testing.T) {
	var gotNetwork, gotAddress string
	plug := &ForwardPlugin{
		Dest: mustForwardRequest(t, "localhost:8080"),
		dial: core.NewProxyDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
			gotNetwork = network
			gotAddress = address
			peer, remote := net.Pipe()
			t.Cleanup(func() {
				_ = peer.Close()
				_ = remote.Close()
			})
			return peer, nil
		}),
	}

	conn, err := plug.Handle(servetest.NewReadWriteCloser(nil), servetest.NewConn(nil))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	_ = conn.Close()

	if gotNetwork != "tcp" || gotAddress != "localhost:8080" {
		t.Fatalf("unexpected dial target: %s %s", gotNetwork, gotAddress)
	}
}

func TestHandlePropagatesDialError(t *testing.T) {
	dialErr := errors.New("dial failed")
	plug := &ForwardPlugin{
		Dest: mustForwardRequest(t, "127.0.0.1:8080"),
		dial: core.NewProxyDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, dialErr
		}),
	}

	_, err := plug.Handle(servetest.NewReadWriteCloser(nil), servetest.NewConn(nil))
	if !errors.Is(err, dialErr) {
		t.Fatalf("expected dial error, got %v", err)
	}
}

func TestRelayReturnsBridgeWriteError(t *testing.T) {
	plug := &ForwardPlugin{Dest: mustForwardRequest(t, "localhost:8080")}
	bridgeErr := errors.New("bridge write failed")
	bridge := servetest.NewReadWriteCloser(nil)
	bridge.WriteErr = bridgeErr
	_, err := plug.Relay(servetest.NewConn(nil), bridge)
	if !errors.Is(err, bridgeErr) {
		t.Fatalf("expected bridge write error, got %v", err)
	}
}

func TestRelayReturnsReplyReadError(t *testing.T) {
	plug := &ForwardPlugin{Dest: mustForwardRequest(t, "localhost:8080")}
	bridge := servetest.NewReadWriteCloser(servetest.SOCKS5Reply(0x05))
	_, err := plug.Relay(servetest.NewConn(nil), bridge)
	if err == nil || !strings.Contains(err.Error(), "SOCKS5 request failed") {
		t.Fatalf("expected reply read error, got %v", err)
	}
}

func mustForwardRequest(t *testing.T, dest string) *socks5.RelayRequest {
	t.Helper()
	req, err := newRelayRequest(dest)
	if err != nil {
		t.Fatalf("newRelayRequest(%q) returned error: %v", dest, err)
	}
	return req
}
