package tunnel

import (
	"context"
	"crypto/tls"
	"math"
	"net"
	"net/url"

	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/wrapper"
	"github.com/chainreactors/rem/x/utils"
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

func WithTLS() TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		if do.listener != nil {
			tlsConfig, err := utils.NewServerTLSConfig("", "", "")
			if err != nil {
				return
			}
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: TLSPriority,
				ListenHook: func(ctx context.Context, listener net.Listener) (context.Context, net.Listener, error) {
					tlsListener := tls.NewListener(listener, tlsConfig)
					return ctx, tlsListener, nil
				},
			})
		} else {
			tlsConfig, err := utils.NewClientTLSConfig("", "", "", "")
			if err != nil {
				return
			}
			do.meta["tls"] = true
			do.meta["tlsConfig"] = tlsConfig
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: TLSPriority,
				DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
					conn := tls.Client(c, tlsConfig)
					return ctx, conn, nil
				},
			})
		}
	})
}

func WithTLSInTLS() TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		if do.listener != nil {
			tlsConfig, err := utils.NewServerTLSConfig("", "", "")
			if err != nil {
				return
			}
			do.meta["tlsintls"] = true
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: TLSPriority - 100,
				AcceptHook: func(ctx context.Context, c net.Conn) (context.Context, net.Conn, error) {
					tlsConn := tls.Server(c, tlsConfig)
					return ctx, tlsConn, nil
				},
			})
		} else {
			tlsConfig, err := utils.NewClientTLSConfig("", "", "", "")
			if err != nil {
				return
			}
			do.meta["tlsintls"] = true
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: TLSPriority - 100,
				DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
					conn := tls.Client(c, tlsConfig)
					return ctx, conn, nil
				},
			})
		}
	})
}

func WithProxyClient(proxyAddr []string) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.dialer = &core.WrappedDialer{
			TunnelDialer: do.dialer,
			Dialer: func(dst string) (net.Conn, error) {
				var proxies []*url.URL
				for _, proxyUrl := range proxyAddr {
					u, err := url.Parse(proxyUrl)
					if err != nil {
						return nil, err
					}
					proxies = append(proxies, u)
				}
				proxy, err := proxyclient.NewClientChain(proxies)
				if err != nil {
					return nil, err
				}
				u, err := core.NewURL(dst)
				if err != nil {
					return nil, err
				}
				conn, err := proxy.Dial(u.Scheme, u.Host)
				if err != nil {
					return nil, err
				}
				return conn, nil
			},
		}
	})
}

func WithCompression() TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		if do.listener == nil {
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: WrapperPriority + 5,
				DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
					return ctx, cio.WrapConn(c, wrapper.NewSnappyWrapper(c, c, nil)), nil
				},
			})
		} else {
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: WrapperPriority + -5,
				AcceptHook: func(ctx context.Context, c net.Conn) (context.Context, net.Conn, error) {
					return ctx, cio.WrapConn(c, wrapper.NewSnappyWrapper(c, c, nil)), nil
				},
			})
		}

	})
}

func WithDialWrappers(opts []*core.WrapperOption) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: WrapperPriority,
			DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
				wrap, err := wrapper.NewChainWrapper(c, opts)
				if err != nil {
					return ctx, c, err
				}
				return ctx, cio.WrapConn(c, wrap), nil
			},
		})
	})
}

func WithAcceptWrappers(opts []*core.WrapperOption) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: WrapperPriority,
			AcceptHook: func(ctx context.Context, c net.Conn) (context.Context, net.Conn, error) {
				wrap, err := wrapper.NewChainWrapper(c, opts)
				if err != nil {
					return ctx, c, err
				}
				return ctx, cio.WrapConn(c, wrap), nil
			},
		})
	})
}

func WithDialWrapper(opt *core.WrapperOption) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: WrapperPriority + 1,
			DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
				wrap, err := core.WrapperCreate(opt.Name, c, c, opt.Options)
				if err != nil {
					return ctx, c, err
				}
				return ctx, cio.WrapConn(c, wrap), nil
			},
		})
	})
}

func WithAcceptWrapper(opt *core.WrapperOption) TunnelOption {
	return newFuncTunnelOption(func(do *TunnelService) {
		do.afterHooks = append(do.afterHooks, core.AfterHook{
			Priority: WrapperPriority + 1,
			AcceptHook: func(ctx context.Context, c net.Conn) (context.Context, net.Conn, error) {
				wrap, err := core.WrapperCreate(opt.Name, c, c, opt.Options)
				if err != nil {
					return ctx, c, err
				}
				return ctx, cio.WrapConn(c, wrap), nil
			},
		})
	})
}

func WithWrappers(server bool, opts []*core.WrapperOption) TunnelOption {
	if server {
		return WithAcceptWrappers(opts)
	} else {
		return WithDialWrappers(opts)
	}
}
