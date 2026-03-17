package tcp

import (
	"context"
	"net"

	"github.com/chainreactors/rem/protocol/core"
)

func init() {
	core.DialerRegister(core.TCPTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewTCPDialer(ctx), nil
	})

	core.ListenerRegister(core.TCPTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewTCPListener(ctx), nil
	})
}

type TCPDialer struct {
	net.Conn
	meta core.Metas
}

func NewTCPDialer(ctx context.Context) *TCPDialer {
	return &TCPDialer{
		meta: core.GetMetas(ctx),
	}
}

func (c *TCPDialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	c.meta["url"] = u
	return core.GetContextDialer(c.meta).DialContext(context.Background(), "tcp", u.Host)
}

type TCPListener struct {
	listener net.Listener
	meta     core.Metas
}

func NewTCPListener(ctx context.Context) *TCPListener {
	return &TCPListener{
		meta: core.GetMetas(ctx),
	}
}

func (c *TCPListener) Listen(dst string) (net.Listener, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	c.meta["url"] = u

	listener, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, err
	}
	c.listener = listener
	return listener, nil
}

func (c *TCPListener) Accept() (net.Conn, error) {
	return c.listener.Accept()
}

func (c *TCPListener) Close() error {
	if c.listener != nil {
		return c.listener.Close()
	}
	return nil
}

func (c *TCPListener) Addr() net.Addr {
	return c.meta.URL()
}
