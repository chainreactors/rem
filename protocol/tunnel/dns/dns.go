package dns

import (
	"context"
	"net"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/kcp"
)

func init() {
	core.DialerRegister(core.DNSTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewDNSDialer(ctx), nil
	},
		"dot", "doh")

	core.ListenerRegister(core.DNSTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewDNSListener(ctx), nil
	},
		"dot", "doh")
}

// DNSDialer implements core.TunnelDialer
type DNSDialer struct {
	net.Conn
	meta core.Metas
}

// DNSListener implements core.TunnelListener
type DNSListener struct {
	listener *kcp.Listener
	meta     core.Metas
}

// NewDNSDialer creates a new DNS dialer
func NewDNSDialer(ctx context.Context) *DNSDialer {
	return &DNSDialer{
		meta: core.GetMetas(ctx),
	}
}

// NewDNSListener creates a new DNS listener
func NewDNSListener(ctx context.Context) *DNSListener {
	return &DNSListener{
		meta: core.GetMetas(ctx),
	}
}

// Dial implements core.TunnelDialer.Dial
func (d *DNSDialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	d.meta["url"] = u
	kcp.SetKCPMTULimit(d.meta["mtu"].(int))

	conn, err := kcp.DialWithOptions(core.DNSTunnel, u.RawString(), nil, 0, 0)
	if err != nil {
		return nil, err
	}
	return kcp.NewKCPConn(conn, kcp.DNSKCPConfig), nil
}

// Listen implements core.TunnelListener.Listen
func (l *DNSListener) Listen(dst string) (net.Listener, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	l.meta["url"] = u
	kcp.SetKCPMTULimit(l.meta["mtu"].(int))

	lsn, err := kcp.ListenWithOptions(core.DNSTunnel, u.RawString(), nil, 0, 0)
	if err != nil {
		return nil, err
	}
	l.listener = lsn
	l.listener.SetReadBuffer(core.MaxPacketSize)
	l.listener.SetWriteBuffer(core.MaxPacketSize)
	return lsn, nil
}

// Accept implements core.TunnelListener.Accept
func (l *DNSListener) Accept() (net.Conn, error) {
	conn, err := l.listener.AcceptKCP()
	if err != nil {
		return nil, err
	}
	return kcp.NewKCPConn(conn, kcp.DNSKCPConfig), nil
}

// Close implements core.TunnelListener.Close
func (l *DNSListener) Close() error {
	if l.listener != nil {
		return l.listener.Close()
	}
	return nil
}

// Addr implements core.TunnelListener.Addr
func (l *DNSListener) Addr() net.Addr {
	return l.meta.URL()
}
