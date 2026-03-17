//go:build !tinygo

package memory

import (
	"context"
	"net"
	"net/url"

	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/socks5"
)

func init() {
	// Register memory as a proxy scheme
	proxyclient.RegisterScheme("MEMORY", NewMemoryProxyClient)
}

func NewMemoryProxyClient(proxyURL *url.URL, upstreamDial proxyclient.Dial) (proxyclient.Dial, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		dialer := &MemoryDialer{meta: make(core.Metas)}
		conn, err := dialer.Dial(proxyURL.Host)
		if err != nil {
			return nil, err
		}
		// Perform SOCKS5 client handshake over the memory pipe
		if err := socks5.ClientConnect(conn, address, "", ""); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}, nil
}
