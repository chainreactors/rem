package simplex

import (
	"context"
	"net"
	"strings"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/arq"
	"github.com/chainreactors/rem/x/simplex"
)

// isHighLatencyTransport 判断是否是高延迟传输（如轮询基础的 API）
func isHighLatencyTransport(scheme string) bool {
	scheme = strings.ToLower(scheme)
	// SharePoint/OneDrive/OSS 等轮询基础传输通常 RTT > 6s
	return scheme == "sharepoint" || scheme == "onedrive" || scheme == "teams"
}

func init() {
	core.DialerRegister(core.SimplexTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewSimplexDialer(ctx), nil
	},
		"sharepoint", "oss", "onedrive")
	core.ListenerRegister(core.SimplexTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewSimplexListener(ctx), nil
	},
		"sharepoint", "oss", "onedrive")
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
	d.meta["url"], err = core.NewURL(addr.String())
	if err != nil {
		return nil, err
	}
	d.meta["simplex_addr"] = addr
	client.SetIsControlPacket(arq.SimpleARQChecker)

	mtu := addr.MaxBodySize()

	// 为高延迟传输（如 SharePoint 3s 轮询）配置更大的 RTO
	cfg := arq.ARQConfig{
		MTU:     mtu,
		Timeout: 0,
	}
	if isHighLatencyTransport(addr.Scheme) {
		cfg.RTO = 15000 // 15 秒，适配轮询周期 + 网络延迟
	}

	return arq.NewARQSessionWithConfig(client, addr, cfg), nil
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
	server.SetIsControlPacket(arq.SimpleARQChecker)
	l.server = server
	addr := server.Addr()

	u, err = core.NewURL(addr.String())
	l.meta["url"] = u
	l.meta["simplex_addr"] = addr

	// 获取MTU配置
	mtu := server.Addr().MaxBodySize()

	// 为高延迟传输（如 SharePoint 3s 轮询）配置更大的 RTO
	cfg := arq.ARQConfig{
		MTU:     mtu,
		Timeout: 0,
	}
	if isHighLatencyTransport(addr.Scheme) {
		cfg.RTO = 15000 // 15 秒，适配轮询周期 + 网络延迟
	}

	// 创建ARQ监听器，使用simplex服务端作为底层传输
	listener, err := arq.ServeConnWithConfig(server, cfg, false)
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
