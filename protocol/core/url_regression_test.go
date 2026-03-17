package core

import (
	"context"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestURLCopyPreservesRawScheme(t *testing.T) {
	original := &URL{
		URL: &url.URL{
			Scheme:   "https",
			User:     url.UserPassword("user", "pass"),
			Host:     "example.com:443",
			Path:     "/api",
			RawQuery: "foo=bar",
			Fragment: "frag",
		},
		RawScheme: "wss",
		Tunnel:    WebSocketTunnel,
		Direction: DirUp,
	}

	copied := original.Copy()
	if copied.RawScheme != original.RawScheme {
		t.Fatalf("RawScheme mismatch: want %q, got %q", original.RawScheme, copied.RawScheme)
	}
	if copied.String() != original.String() {
		t.Fatalf("string mismatch: want %q, got %q", original.String(), copied.String())
	}
}

func TestWrappedDialerDelegatesToEmbeddedDialer(t *testing.T) {
	wantConn := &testConn{}
	embedded := &testTunnelDialer{
		dial: func(dst string) (net.Conn, error) {
			if dst != "example" {
				t.Fatalf("unexpected dial target: %s", dst)
			}
			return wantConn, nil
		},
	}

	wrapped := &WrappedDialer{TunnelDialer: embedded}
	gotConn, err := wrapped.Dial("example")
	if err != nil {
		t.Fatalf("Dial returned error: %v", err)
	}
	if gotConn != wantConn {
		t.Fatal("wrapped dialer did not return embedded connection")
	}
	if embedded.calls != 1 {
		t.Fatalf("embedded dialer call count: want 1, got %d", embedded.calls)
	}
}

func TestWrappedListenerDelegatesToEmbeddedListener(t *testing.T) {
	wantConn := &testConn{}
	embedded := &testTunnelListener{
		accept: func() (net.Conn, error) {
			return wantConn, nil
		},
	}

	wrapped := &WrappedListener{TunnelListener: embedded}
	gotConn, err := wrapped.Accept()
	if err != nil {
		t.Fatalf("Accept returned error: %v", err)
	}
	if gotConn != wantConn {
		t.Fatal("wrapped listener did not return embedded connection")
	}
	if embedded.acceptCalls != 1 {
		t.Fatalf("embedded listener accept count: want 1, got %d", embedded.acceptCalls)
	}
}

type testTunnelDialer struct {
	calls int
	dial  func(string) (net.Conn, error)
}

func (d *testTunnelDialer) Dial(dst string) (net.Conn, error) {
	d.calls++
	return d.dial(dst)
}

type testTunnelListener struct {
	acceptCalls int
	accept      func() (net.Conn, error)
}

func (l *testTunnelListener) Accept() (net.Conn, error) {
	l.acceptCalls++
	return l.accept()
}

func (l *testTunnelListener) Close() error {
	return nil
}

func (l *testTunnelListener) Addr() net.Addr {
	return testAddr("listener")
}

func (l *testTunnelListener) Listen(dst string) (net.Listener, error) {
	return l, nil
}

type testConn struct{}

func (c *testConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c *testConn) Write(p []byte) (int, error)        { return len(p), nil }
func (c *testConn) Close() error                       { return nil }
func (c *testConn) LocalAddr() net.Addr                { return testAddr("local") }
func (c *testConn) RemoteAddr() net.Addr               { return testAddr("remote") }
func (c *testConn) SetDeadline(_ time.Time) error      { return nil }
func (c *testConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *testConn) SetWriteDeadline(_ time.Time) error { return nil }

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }

func TestGetRegisteredDialersIncludesAliases(t *testing.T) {
	name := "test-alias-dialer"
	DialerRegister(name, func(ctx context.Context) (TunnelDialer, error) {
		return &testTunnelDialer{dial: func(string) (net.Conn, error) { return &testConn{}, nil }}, nil
	}, "alias-a", "alias-b")

	got := GetRegisteredDialers()
	found := false
	for _, entry := range got {
		if strings.Contains(entry, name) && strings.Contains(entry, "alias-a") && strings.Contains(entry, "alias-b") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("registered dialers missing aliases: %v", got)
	}
}
