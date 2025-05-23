package icmp

import (
	"context"
	"net"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/kcp"
)

func init() {
	core.DialerRegister(core.ICMPTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewICMPDialer(ctx), nil
	})
	core.ListenerRegister(core.ICMPTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewICMPListener(ctx), nil
	})
}

type ICMPDialer struct {
	net.Conn
	meta core.Metas
}

type ICMPListener struct {
	listener *kcp.Listener
	meta     core.Metas
}

func NewICMPDialer(ctx context.Context) *ICMPDialer {
	return &ICMPDialer{
		meta: core.GetMetas(ctx),
	}
}

func NewICMPListener(ctx context.Context) *ICMPListener {
	return &ICMPListener{
		meta: core.GetMetas(ctx),
	}
}

func (c *ICMPListener) Addr() net.Addr {
	return c.meta.URL()
}

func (c *ICMPDialer) Dial(dst string) (net.Conn, error) {
	u, _ := core.NewURL(dst)
	c.meta["url"] = u
	//host, _, err := net.SplitHostPort(dst)
	//if err != nil {
	//	return nil, err
	//}
	conn, err := kcp.DialWithOptions(parseICMPNetwork(u.Hostname()), u.Host, nil, 0, 0)
	if err != nil {
		return nil, err
	}
	return kcp.NewKCPConn(conn, kcp.RadicalKCPConfig), nil
}

func (c *ICMPListener) Listen(dst string) (net.Listener, error) {
	u, _ := core.NewURL(dst)
	c.meta["url"] = u
	lsn, err := kcp.ListenWithOptions(parseICMPNetwork(u.Hostname()), u.Host, nil, 0, 0)
	if err != nil {
		return nil, err
	}
	c.listener = lsn
	c.listener.SetReadBuffer(core.MaxPacketSize)
	c.listener.SetWriteBuffer(core.MaxPacketSize)
	c.listener.SetDSCP(46)
	return lsn, nil
}

func (c *ICMPListener) Accept() (net.Conn, error) {
	conn, err := c.listener.AcceptKCP()
	if err != nil {
		return nil, err
	}
	return kcp.NewKCPConn(conn, kcp.RadicalKCPConfig), nil
}

func (c *ICMPListener) Close() error {
	if c.listener != nil {
		return c.listener.Close()
	}
	return nil
}

func parseICMPNetwork(ipStr string) string {
	ip := net.ParseIP(ipStr)

	if ip.To4() != nil {
		return "ip4:icmp"
	}
	return "ip6:ipv6-icmp"
}
