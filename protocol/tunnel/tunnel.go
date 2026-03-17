package tunnel

import (
	"context"
	"fmt"
	"net"
	"sort"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
)

var (
	ctxKey = "meta"
)

func NewTunnel(ctx context.Context, proto string, isLns bool, opts ...TunnelOption) (*TunnelService, error) {
	options := map[string]interface{}{
		"mtu": core.MaxPacketSize,
	}

	tun := &TunnelService{
		meta:            options,
		wrapperPriority: DefaultHookPriority,
	}

	tun.ctx = context.WithValue(ctx, ctxKey, tun.meta)
	var err error
	if isLns {
		tun.listener, err = core.ListenerCreate(proto, tun.ctx)
		if err != nil {
			return nil, err
		}
		opts = append([]TunnelOption{WithListener(tun.listener)}, opts...)
	} else {
		tun.dialer, err = core.DialerCreate(proto, tun.ctx)
		if err != nil {
			return nil, err
		}
		opts = append([]TunnelOption{WithDialer(tun.dialer)}, opts...)
	}

	switch proto {
	case core.WebSocketTunnel:
		opts = append(opts, WithMeta(map[string]interface{}{
			"path": utils.RandomString(8),
		}))
	case core.UNIXTunnel:
		opts = append(opts, WithMeta(map[string]interface{}{
			"pipe": utils.RandomString(8),
		}))
	}

	for _, opt := range opts {
		opt.Apply(tun)
	}

	sort.SliceStable(tun.afterHooks, func(i, j int) bool {
		return tun.afterHooks[i].Priority > tun.afterHooks[j].Priority
	})

	return tun, nil
}

type TunnelService struct {
	dialer   core.TunnelDialer
	listener core.TunnelListener

	meta            core.Metas
	wrapperPriority uint
	ctx             context.Context
	hooks
}

func WithDialer(d core.TunnelDialer) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.dialer = d
	})
}

func WithListener(l core.TunnelListener) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.listener = l
	})
}

func (op *TunnelService) isListener() bool {
	if op.listener != nil {
		return true
	}
	return false
}

func (op *TunnelService) Dial(addr string) (c net.Conn, err error) {
	ctx := op.ctx
	for _, v := range op.beforeHooks {
		if v.DialHook != nil {
			ctx = v.DialHook(ctx, addr)
		}
	}

	if op.dialer != nil {
		c, err = op.dialer.Dial(addr)
	} else {
		return nil, fmt.Errorf("no dialer available")
	}

	if err != nil {
		return nil, err
	}

	lastSuccConn := c
	for _, v := range op.afterHooks {
		if v.DialerHook == nil {
			continue
		}
		ctx, c, err = v.DialerHook(ctx, c, addr)
		if err != nil {
			lastSuccConn.Close()
			return nil, err
		}
		lastSuccConn = c
	}
	return
}

func (op *TunnelService) Accept() (c net.Conn, err error) {
	ctx := op.ctx

	for _, v := range op.beforeHooks {
		if v.AcceptHook != nil {
			ctx = v.AcceptHook(ctx)
		}
	}

	if op.listener != nil {
		c, err = op.listener.Accept()
	} else {
		return nil, fmt.Errorf("no listener available")
	}

	if err != nil {
		return nil, err
	}

	lastSuccConn := c
	for _, v := range op.afterHooks {
		if v.AcceptHook == nil {
			continue
		}
		ctx, c, err = v.AcceptHook(ctx, c)
		if err != nil {
			lastSuccConn.Close()
			return nil, err
		}
		lastSuccConn = c
	}
	return
}

func (op *TunnelService) Listen(dst string) (net.Listener, error) {
	if op.listener == nil {
		return nil, fmt.Errorf("listener not set")
	}

	ctx := context.WithValue(context.Background(), ctxKey, op.meta)

	for _, v := range op.beforeHooks {
		if v.ListenHook != nil {
			ctx = v.ListenHook(ctx, dst)
		}
	}

	lsn, err := op.listener.Listen(dst)
	if err != nil {
		return nil, err
	}

	for _, v := range op.afterHooks {
		if v.ListenHook == nil {
			continue
		}
		ctx, lsn, err = v.ListenHook(ctx, lsn)
		if err != nil {
			op.listener.Close()
			return nil, err
		}
		op.listener = &core.WrappedListener{
			TunnelListener: op.listener,
			Lns:            lsn,
		}
	}

	return op.listener, nil
}

func (op *TunnelService) Close() error {
	if op.listener != nil {
		return op.listener.Close()
	}
	return nil
}

func (op *TunnelService) Addr() net.Addr {
	if op.listener != nil {
		return op.listener.Addr()
	}
	return nil
}

// Meta returns a value from the tunnel's metadata map.
func (op *TunnelService) Meta(key string) interface{} {
	if op.meta != nil {
		return op.meta[key]
	}
	return nil
}
