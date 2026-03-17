package runner

import (
	"github.com/chainreactors/rem/protocol/tunnel"
)

func appendAdvanceTunnelOptions(console *Console, _ *RunnerConfig, tunOpts []tunnel.TunnelOption) ([]tunnel.TunnelOption, error) {
	// Standard TLS is always available
	if console.ConsoleURL.GetQuery("tls") != "" {
		tunOpts = append(tunOpts, tunnel.WithTLS())
	}
	if console.ConsoleURL.GetQuery("tlsintls") != "" {
		tunOpts = append(tunOpts, tunnel.WithTLSInTLS())
	}
	return tunOpts, nil
}
