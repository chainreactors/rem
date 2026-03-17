//go:build tinygo

package tunnel

import (
	"context"
	"errors"
	"math"
	"net"

	"github.com/chainreactors/rem/protocol/core"
)

type TunnelOption interface {
	Apply(*TunnelService)
}

type hooks struct {
	afterHooks  []core.AfterHook
	beforeHooks []core.BeforeHook
}

const (
	DefaultHookPriority = 10
	WrapperPriority     = DefaultHookPriority + 10
	TLSPriority         = math.MaxInt32 - 10
)

func newFuncTunnelOption(f func(options *TunnelService)) *funcTunnelOption {
	return &funcTunnelOption{
		f: f,
	}
}

type funcTunnelOption struct {
	f func(*TunnelService)
}

func (fdo *funcTunnelOption) Apply(do *TunnelService) {
	fdo.f(do)
}

func WithMeta(options map[string]interface{}) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		if do.meta != nil {
			for k, v := range options {
				do.meta[k] = v
			}
		} else {
			do.meta = options
		}
	})
}

var errTinyGoTLSUnsupported = errors.New("tinygo: tls/tlsintls tunnel hooks are not supported")
var errTinyGoWrapperUnsupported = errors.New("tinygo: wrapper/compression tunnel hooks are not supported")
var errTinyGoProxyUnsupported = errors.New("tinygo: proxy chain tunnel hook is not supported")

func WithTLS() TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		if do.listener != nil {
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: TLSPriority,
				ListenHook: func(ctx context.Context, listener net.Listener) (context.Context, net.Listener, error) {
					return ctx, listener, errTinyGoTLSUnsupported
				},
			})
			return
		}

		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: TLSPriority,
			DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
				return ctx, c, errTinyGoTLSUnsupported
			},
		})
	})
}

func WithTLSInTLS() TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		if do.listener != nil {
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: TLSPriority - 100,
				AcceptHook: func(ctx context.Context, c net.Conn) (context.Context, net.Conn, error) {
					return ctx, c, errTinyGoTLSUnsupported
				},
			})
			return
		}

		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: TLSPriority - 100,
			DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
				return ctx, c, errTinyGoTLSUnsupported
			},
		})
	})
}

func WithDialWrappers(opts []*core.WrapperOption) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: WrapperPriority,
			DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
				return ctx, c, errTinyGoWrapperUnsupported
			},
		})
	})
}

func WithAcceptWrappers(opts []*core.WrapperOption) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: WrapperPriority,
			AcceptHook: func(ctx context.Context, c net.Conn) (context.Context, net.Conn, error) {
				return ctx, c, errTinyGoWrapperUnsupported
			},
		})
	})
}

func WithDialWrapper(opt *core.WrapperOption) TunnelOption {
	return WithDialWrappers(nil)
}

func WithAcceptWrapper(opt *core.WrapperOption) TunnelOption {
	return WithAcceptWrappers(nil)
}

func WithWrappers(server bool, opts []*core.WrapperOption) TunnelOption {
	if server {
		return WithAcceptWrappers(opts)
	}
	return WithDialWrappers(opts)
}

func WithCompression() TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		if do.listener == nil {
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: WrapperPriority + 5,
				DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
					return ctx, c, errTinyGoWrapperUnsupported
				},
			})
			return
		}

		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: WrapperPriority - 5,
			AcceptHook: func(ctx context.Context, c net.Conn) (context.Context, net.Conn, error) {
				return ctx, c, errTinyGoWrapperUnsupported
			},
		})
	})
}

func WithProxyClient(proxyAddr []string) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.dialer = &core.WrappedDialer{
			TunnelDialer: do.dialer,
			Dialer: func(dst string) (net.Conn, error) {
				return nil, errTinyGoProxyUnsupported
			},
		}

		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: WrapperPriority + 20,
			DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
				return ctx, c, errTinyGoProxyUnsupported
			},
		})
	})
}
