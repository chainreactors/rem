package simplex

import (
	"context"
	"net"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/arq"
	"github.com/chainreactors/rem/x/simplex"
)

func init() {
	core.DialerRegister(core.SimplexTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewSimplexDialer(ctx), nil
	},
		"oss")
	core.ListenerRegister(core.SimplexTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewSimplexListener(ctx), nil
	},
		"oss")
}

type SimplexDialer struct {
	meta core.Metas
}

type SimplexListener struct {
	listener *arq.ARQListener
	server   *simplex.SimplexServer
	meta     core.Metas
}

func NewSimplexDialer(ctx context.Context) *SimplexDialer {
	return &SimplexDialer{
		meta: core.GetMetas(ctx),
	}
}

func NewSimplexListener(ctx context.Context) *SimplexListener {
	return &SimplexListener{
		meta: core.GetMetas(ctx),
	}
}

func (d *SimplexDialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}

	addr, err := simplex.ResolveSimplexAddr(u.RawScheme, u.RawString())
	if err != nil {
		return nil, err
	}
	client, err := simplex.NewSimplexClient(addr)
	if err != nil {
		return nil, err
	}
	d.meta["url"], err = core.NewURL(core.SimplexTunnel + "+" + addr.String())
	if err != nil {
		return nil, err
	}
	d.meta["simplex_addr"] = addr
	return arq.NewARQSessionWithConfig(client, addr, addr.ARQConfig()), nil
}

func (l *SimplexListener) Accept() (net.Conn, error) {
	return l.listener.Accept()
}

func (l *SimplexListener) Listen(dst string) (net.Listener, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}

	// 创建simplex服务端
	server, err := simplex.NewSimplexServer(u.RawScheme, u.RawString())
	if err != nil {
		return nil, err
	}
	l.server = server
	addr := server.Addr()

	u, err = core.NewURL(core.SimplexTunnel + "+" + addr.String())
	if err != nil {
		server.Close()
		return nil, err
	}
	l.meta["url"] = u
	l.meta["simplex_addr"] = addr

	// 创建ARQ监听器，使用simplex服务端作为底层传输
	listener, err := arq.ServeConnWithConfig(server, addr.ARQConfig(), false)
	if err != nil {
		server.Close()
		return nil, err
	}
	l.listener = listener

	return listener, nil
}

func (l *SimplexListener) Close() error {
	if l.listener != nil {
		l.listener.Close()
	}
	if l.server != nil {
		l.server.Close()
	}
	return nil
}

func (l *SimplexListener) Addr() net.Addr {
	return l.meta.URL()
}
