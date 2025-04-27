package core

import (
	"context"
	"github.com/chainreactors/proxyclient"
	"net"
)

type ContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Dial(network, address string) (net.Conn, error)
}

func NewProxyDialer(client proxyclient.Dial) *ProxyDialer {
	return &ProxyDialer{dial: client}
}

type ProxyDialer struct {
	dial proxyclient.Dial
}

func (client *ProxyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return client.dial.DialContext(ctx, network, address)
}

func (client *ProxyDialer) Dial(network, address string) (net.Conn, error) {
	return client.dial.Dial(network, address)
}
