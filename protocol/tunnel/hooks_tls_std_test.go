//go:build !tinygo

package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sort"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/xtls"
)

type stubTunnelListener struct{}

func (stubTunnelListener) Accept() (net.Conn, error) {
	return nil, errors.New("stub listener")
}

func (stubTunnelListener) Close() error {
	return nil
}

func (stubTunnelListener) Addr() net.Addr {
	return &net.TCPAddr{}
}

func (stubTunnelListener) Listen(string) (net.Listener, error) {
	return nil, errors.New("stub listener")
}

func TestWithTLSInTLSClientMetadataAndPriority(t *testing.T) {
	tun := &TunnelService{meta: core.Metas{}}

	WithTLSInTLS().Apply(tun)

	if len(tun.afterHooks) != 1 {
		t.Fatalf("expected 1 after hook, got %d", len(tun.afterHooks))
	}
	if got, ok := tun.meta["tlsintls"].(bool); !ok || !got {
		t.Fatalf("expected tlsintls metadata to be true, got %#v", tun.meta["tlsintls"])
	}

	hook := tun.afterHooks[0]
	if hook.Priority != TLSPriority-100 {
		t.Fatalf("expected priority %d, got %d", TLSPriority-100, hook.Priority)
	}
	if hook.DialerHook == nil {
		t.Fatal("expected client-side tlsintls dialer hook")
	}
	if hook.AcceptHook != nil {
		t.Fatal("did not expect accept hook on client-side tlsintls option")
	}
	if hook.ListenHook != nil {
		t.Fatal("did not expect listen hook on client-side tlsintls option")
	}
}

func TestWithTLSInTLSServerMetadataAndPriority(t *testing.T) {
	tun := &TunnelService{
		meta:     core.Metas{},
		listener: stubTunnelListener{},
	}

	WithTLSInTLS().Apply(tun)

	if len(tun.afterHooks) != 1 {
		t.Fatalf("expected 1 after hook, got %d", len(tun.afterHooks))
	}
	if got, ok := tun.meta["tlsintls"].(bool); !ok || !got {
		t.Fatalf("expected tlsintls metadata to be true, got %#v", tun.meta["tlsintls"])
	}

	hook := tun.afterHooks[0]
	if hook.Priority != TLSPriority-100 {
		t.Fatalf("expected priority %d, got %d", TLSPriority-100, hook.Priority)
	}
	if hook.AcceptHook == nil {
		t.Fatal("expected server-side tlsintls accept hook")
	}
	if hook.DialerHook != nil {
		t.Fatal("did not expect dialer hook on server-side tlsintls option")
	}
	if hook.ListenHook != nil {
		t.Fatal("did not expect listen hook on server-side tlsintls option")
	}
}

func TestWithTLSAndTLSInTLSClientPriorityOrder(t *testing.T) {
	tun := &TunnelService{meta: core.Metas{}}

	WithTLSInTLS().Apply(tun)
	WithTLS().Apply(tun)

	sort.SliceStable(tun.afterHooks, func(i, j int) bool {
		return tun.afterHooks[i].Priority > tun.afterHooks[j].Priority
	})

	if len(tun.afterHooks) != 2 {
		t.Fatalf("expected 2 after hooks, got %d", len(tun.afterHooks))
	}
	if got, ok := tun.meta["tls"].(bool); !ok || !got {
		t.Fatalf("expected tls metadata to be true, got %#v", tun.meta["tls"])
	}
	if got, ok := tun.meta["tlsintls"].(bool); !ok || !got {
		t.Fatalf("expected tlsintls metadata to be true, got %#v", tun.meta["tlsintls"])
	}
	if _, ok := tun.meta["tlsConfig"].(*tls.Config); !ok {
		t.Fatalf("expected tlsConfig metadata to be a *tls.Config, got %#v", tun.meta["tlsConfig"])
	}
	if tun.afterHooks[0].Priority != TLSPriority {
		t.Fatalf("expected outer TLS hook priority %d, got %d", TLSPriority, tun.afterHooks[0].Priority)
	}
	if tun.afterHooks[1].Priority != TLSPriority-100 {
		t.Fatalf("expected inner TLSInTLS hook priority %d, got %d", TLSPriority-100, tun.afterHooks[1].Priority)
	}
}

func TestWithTLSInTLSEstablishesInnerTLSOverExistingConn(t *testing.T) {
	serverTun := &TunnelService{
		meta:     core.Metas{},
		listener: stubTunnelListener{},
	}
	clientTun := &TunnelService{meta: core.Metas{}}

	WithTLSInTLS().Apply(serverTun)
	WithTLSInTLS().Apply(clientTun)

	serverCfg, err := xtls.NewServerTLSConfig("", "", "")
	if err != nil {
		t.Fatalf("new server tls config: %v", err)
	}
	clientCfg, err := xtls.NewClientTLSConfig("", "", "", "")
	if err != nil {
		t.Fatalf("new client tls config: %v", err)
	}

	rawServer, rawClient := net.Pipe()
	defer rawServer.Close()
	defer rawClient.Close()

	outerServer := tls.Server(rawServer, serverCfg)
	outerClient := tls.Client(rawClient, clientCfg)

	handshakeDeadline := time.Now().Add(5 * time.Second)
	if err := outerServer.SetDeadline(handshakeDeadline); err != nil {
		t.Fatalf("set outer server deadline: %v", err)
	}
	if err := outerClient.SetDeadline(handshakeDeadline); err != nil {
		t.Fatalf("set outer client deadline: %v", err)
	}

	outerErrs := make(chan error, 2)
	go func() { outerErrs <- outerServer.Handshake() }()
	go func() { outerErrs <- outerClient.Handshake() }()
	for i := 0; i < 2; i++ {
		if err := <-outerErrs; err != nil {
			t.Fatalf("outer tls handshake failed: %v", err)
		}
	}

	serverHook := serverTun.afterHooks[0]
	clientHook := clientTun.afterHooks[0]
	if serverHook.AcceptHook == nil || clientHook.DialerHook == nil {
		t.Fatal("expected tlsintls hooks to be installed on both client and server")
	}

	_, innerServerConn, err := serverHook.AcceptHook(context.Background(), outerServer)
	if err != nil {
		t.Fatalf("apply server tlsintls hook: %v", err)
	}
	_, innerClientConn, err := clientHook.DialerHook(context.Background(), outerClient, "pipe")
	if err != nil {
		t.Fatalf("apply client tlsintls hook: %v", err)
	}

	innerServer, ok := innerServerConn.(*tls.Conn)
	if !ok {
		t.Fatalf("expected server inner conn to be *tls.Conn, got %T", innerServerConn)
	}
	innerClient, ok := innerClientConn.(*tls.Conn)
	if !ok {
		t.Fatalf("expected client inner conn to be *tls.Conn, got %T", innerClientConn)
	}

	defer innerServer.Close()
	defer innerClient.Close()

	innerDeadline := time.Now().Add(5 * time.Second)
	if err := innerServer.SetDeadline(innerDeadline); err != nil {
		t.Fatalf("set inner server deadline: %v", err)
	}
	if err := innerClient.SetDeadline(innerDeadline); err != nil {
		t.Fatalf("set inner client deadline: %v", err)
	}

	innerErrs := make(chan error, 2)
	go func() { innerErrs <- innerServer.Handshake() }()
	go func() { innerErrs <- innerClient.Handshake() }()
	for i := 0; i < 2; i++ {
		if err := <-innerErrs; err != nil {
			t.Fatalf("inner tlsintls handshake failed: %v", err)
		}
	}

	serverDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 4)
		if _, err := io.ReadFull(innerServer, buf); err != nil {
			serverDone <- err
			return
		}
		if string(buf) != "ping" {
			serverDone <- errors.New("unexpected client payload")
			return
		}
		_, err := innerServer.Write([]byte("pong"))
		serverDone <- err
	}()

	if _, err := innerClient.Write([]byte("ping")); err != nil {
		t.Fatalf("write inner payload: %v", err)
	}

	resp := make([]byte, 4)
	if _, err := io.ReadFull(innerClient, resp); err != nil {
		t.Fatalf("read inner payload: %v", err)
	}
	if string(resp) != "pong" {
		t.Fatalf("expected pong response, got %q", string(resp))
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server inner exchange failed: %v", err)
	}
}
