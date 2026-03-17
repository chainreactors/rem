package unix

import (
	"context"
	"net"

	"github.com/chainreactors/rem/protocol/core"
)

func init() {
	core.DialerRegister(core.UNIXTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewUnixDialer(ctx), nil
	}, "smb", "pipe", "unix", "sock")
	core.ListenerRegister(core.UNIXTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewUnixListener(ctx), nil
	}, "smb", "pipe", "unix", "sock")
}

// UnixDialer 实现了 Dial 接口
type UnixDialer struct {
	net.Conn
	meta core.Metas
}

// UnixListener 实现了 net.Listener 接口
type UnixListener struct {
	listener net.Listener
	meta     core.Metas
}

func (c *UnixListener) Addr() net.Addr {
	return c.meta.URL()
}

func (c *UnixListener) Accept() (net.Conn, error) {
	return c.listener.Accept()
}

func (c *UnixListener) Close() error {
	return c.listener.Close()
}
