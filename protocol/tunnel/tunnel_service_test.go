//go:build !tinygo

package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/core"
)

func TestTunnelDialAppliesAfterHooksByPriorityAndClosesLastSuccessfulConnOnFailure(t *testing.T) {
	proto := fmt.Sprintf("test-dial-%d", time.Now().UnixNano())
	baseConn := &recordingConn{id: "base"}
	wrappedConn := &recordingConn{id: "wrapped"}
	order := make([]int, 0, 2)

	core.DialerRegister(proto, func(ctx context.Context) (core.TunnelDialer, error) {
		return &testDialer{conn: baseConn}, nil
	})

	tun, err := NewTunnel(context.Background(), proto, false,
		newFuncTunnelOption(func(ts *TunnelService) {
			ts.afterHooks = append(ts.afterHooks,
				core.AfterHook{
					Priority: 5,
					DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
						order = append(order, 5)
						if c != wrappedConn {
							t.Fatalf("low-priority hook saw unexpected conn: %#v", c)
						}
						return ctx, c, errors.New("boom")
					},
				},
				core.AfterHook{
					Priority: 50,
					DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
						order = append(order, 50)
						if c != baseConn {
							t.Fatalf("high-priority hook saw unexpected conn: %#v", c)
						}
						return ctx, wrappedConn, nil
					},
				},
			)
		}),
	)
	if err != nil {
		t.Fatalf("NewTunnel returned error: %v", err)
	}

	_, err = tun.Dial("ignored")
	if err == nil || err.Error() != "boom" {
		t.Fatalf("Dial error: want boom, got %v", err)
	}
	if !reflect.DeepEqual(order, []int{50, 5}) {
		t.Fatalf("hook order mismatch: got %v", order)
	}
	if !wrappedConn.closed {
		t.Fatal("last successful wrapped connection was not closed")
	}
	if baseConn.closed {
		t.Fatal("base connection should remain owned by wrapper path")
	}
}

type testDialer struct {
	conn net.Conn
}

func (d *testDialer) Dial(dst string) (net.Conn, error) {
	return d.conn, nil
}

type recordingConn struct {
	id     string
	closed bool
}

func (c *recordingConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c *recordingConn) Write(p []byte) (int, error)        { return len(p), nil }
func (c *recordingConn) Close() error                       { c.closed = true; return nil }
func (c *recordingConn) LocalAddr() net.Addr                { return tunnelTestAddr(c.id + "-local") }
func (c *recordingConn) RemoteAddr() net.Addr               { return tunnelTestAddr(c.id + "-remote") }
func (c *recordingConn) SetDeadline(_ time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(_ time.Time) error { return nil }

type tunnelTestAddr string

func (a tunnelTestAddr) Network() string { return "test" }
func (a tunnelTestAddr) String() string  { return string(a) }
