package http

import (
	"context"
	"net"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/kcp"
)

func init() {
	core.DialerRegister(core.HTTPTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewHTTPDialer(ctx), nil
	},
		"https", "cdn")

	core.ListenerRegister(core.HTTPTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewHTTPListener(ctx), nil
	},
		"https", "cdn")
}

type HTTPDialer struct {
	net.Conn
	meta core.Metas
}

type HTTPListener struct {
	listener *kcp.Listener
	meta     core.Metas
}

func NewHTTPDialer(ctx context.Context) *HTTPDialer {
	return &HTTPDialer{
		meta: ctx.Value("meta").(core.Metas),
	}
}

func NewHTTPListener(ctx context.Context) *HTTPListener {
	return &HTTPListener{
		meta: ctx.Value("meta").(core.Metas),
	}
}

func (c *HTTPListener) Addr() net.Addr {
	return c.meta.URL()
}

func (c *HTTPDialer) Dial(dst string) (net.Conn, error) {
	u, _ := core.NewURL(dst)
	c.meta["url"] = u
	kcp.SetKCPMTULimit(c.meta["mtu"].(int))
	conn, err := kcp.DialWithOptions(core.HTTPTunnel, u.RawString(), nil, 0, 0)
	if err != nil {
		return nil, err
	}
	return kcp.NewKCPConn(conn, kcp.HTTPKCPConfig), nil
}

func (c *HTTPListener) Listen(dst string) (net.Listener, error) {
	u, _ := core.NewURL(dst)
	c.meta["url"] = u
	kcp.SetKCPMTULimit(c.meta["mtu"].(int))
	lsn, err := kcp.ListenWithOptions(core.HTTPTunnel, u.String(), nil, 0, 0)
	if err != nil {
		return nil, err
	}
	c.listener = lsn
	c.listener.SetReadBuffer(core.MaxPacketSize)
	c.listener.SetWriteBuffer(core.MaxPacketSize)
	return lsn, nil
}

func (c *HTTPListener) Accept() (net.Conn, error) {
	conn, err := c.listener.AcceptKCP()
	if err != nil {
		return nil, err
	}
	return kcp.NewKCPConn(conn, kcp.HTTPKCPConfig), nil
}

func (c *HTTPListener) Close() error {
	if c.listener != nil {
		return c.listener.Close()
	}
	return nil
}
