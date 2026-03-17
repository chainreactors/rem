package memory

import (
	"context"
	"fmt"
	"net"
	"sync"

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

func NewMemoryDialer(ctx context.Context) *MemoryDialer {
	return &MemoryDialer{meta: core.GetMetas(ctx)}
}

func NewMemoryListener(ctx context.Context) *MemoryListener {
	return &MemoryListener{meta: core.GetMetas(ctx)}
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

// TryAccept attempts a non-blocking accept. Returns (conn, nil, true) if a
// connection was available, or (nil, nil, false) if none was pending.
// Used by the TinyGo WASM polling loop where goroutine-based Accept blocks forever.
func (l *MemoryListener) TryAccept() (net.Conn, error, bool) {
	select {
	case conn := <-l.connChan:
		return conn, nil, true
	default:
		return nil, nil, false
	}
}

func (l *MemoryListener) Close() error {
	return nil
}

func (l *MemoryListener) Addr() net.Addr {
	return l.meta.URL()
}
