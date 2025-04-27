package memory

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sync"

	"github.com/chainreactors/proxyclient"
	socksproxy "github.com/chainreactors/proxyclient/socks"
	"github.com/chainreactors/rem/protocol/core"
)

var (
	globalBridge = NewMemoryBridge()
)

func init() {
	core.DialerRegister(core.MemoryTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return &MemoryDialer{meta: core.GetMetas(ctx)}, nil
	})

	core.ListenerRegister(core.MemoryTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return &MemoryListener{meta: core.GetMetas(ctx)}, nil
	})

	// 注册 memory 作为代理协议
	proxyclient.RegisterScheme("MEMORY", newMemoryProxyClient)
}

// 实现 DialFactory 函数
func newMemoryProxyClient(proxyURL *url.URL, upstreamDial proxyclient.Dial) (proxyclient.Dial, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		// 首先获取内存通道连接
		dialer := &MemoryDialer{meta: make(core.Metas)}

		conf := &socksproxy.SOCKSConf{
			Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.Dial(proxyURL.Host)
			},
		}

		client, err := socksproxy.NewClient(&url.URL{Scheme: "socks5"}, conf)
		if err != nil {
			return nil, err
		}

		return client.Dial(ctx, network, address)
	}, nil
}

type MemoryBridge struct {
	connMap sync.Map // map[string]chan net.Conn
}

func NewMemoryBridge() *MemoryBridge {
	return &MemoryBridge{}
}

func (b *MemoryBridge) CreateChannel(id string) chan net.Conn {
	ch := make(chan net.Conn, 1)
	b.connMap.Store(id, ch)
	return ch
}

func (b *MemoryBridge) GetChannel(id string) (chan net.Conn, error) {
	ch, ok := b.connMap.Load(id)
	if ok {
		return ch.(chan net.Conn), nil
	} else {
		return nil, fmt.Errorf("not found memory listener")
	}
}

type MemoryDialer struct {
	meta core.Metas
}

type MemoryListener struct {
	meta     core.Metas
	connChan chan net.Conn
}

// Dialer implementation
func (d *MemoryDialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	d.meta["url"] = u

	client, server := net.Pipe()
	ch, err := globalBridge.GetChannel(u.Hostname())
	if err != nil {
		return nil, err
	}
	ch <- server

	return client, nil
}

// Listener implementation
func (l *MemoryListener) Listen(dst string) (net.Listener, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	l.meta["url"] = u
	l.connChan = globalBridge.CreateChannel(u.Hostname())
	return l, nil
}

func (l *MemoryListener) Accept() (net.Conn, error) {
	conn := <-l.connChan
	return conn, nil
}

func (l *MemoryListener) Close() error {
	return nil
}

func (l *MemoryListener) Addr() net.Addr {
	return l.meta.URL()
}
