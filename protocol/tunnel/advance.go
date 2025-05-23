//go:build advance
// +build advance

package tunnel

func WithShadowTLS(server bool, addr, password string) TunnelOption {
	if server {
		return newFuncTunnelOption(func(do *TunnelService) {
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: TLSPriority,
				AcceptHook: func(ctx context.Context, c net.Conn) (context.Context, net.Conn, error) {
					tlsConn, err := shadowtls.NewTLSServer(c, shadowtls.NewShadowTLSConfig(addr, password))
					if err != nil {
						return ctx, c, err
					}
					return ctx, tlsConn, nil
				},
			})
		})
	} else {
		return newFuncTunnelOption(func(do *TunnelService) {
			do.meta["shadowtls"] = true
			do.afterHooks = append(do.afterHooks, core.AfterHook{
				Priority: TLSPriority,
				DialerHook: func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error) {
					conn, err := shadowtls.NewTLSClient(c, shadowtls.NewShadowTLSConfig(addr, password))
					if err != nil {
						return ctx, c, err
					}
					return ctx, conn, nil
				},
			})
		})
	}
}
