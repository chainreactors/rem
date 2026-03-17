package core

import (
	"context"
	"net"
)

// NewProxyDialer wraps a dial function into a ContextDialer.
func NewProxyDialer(dial func(ctx context.Context, network, address string) (net.Conn, error)) *ProxyDialer {
	return &ProxyDialer{dial: dial}
}

type ProxyDialer struct {
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func (client *ProxyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return client.dial(ctx, network, address)
}

func (client *ProxyDialer) Dial(network, address string) (net.Conn, error) {
	return client.dial(context.Background(), network, address)
}

// NetDialFunc, when non-nil, overrides net.Dial in NetDialer.
// TinyGo WASM builds set this to bypass net.DialTCP's 16-byte IPv4 rejection.
var NetDialFunc func(network, address string) (net.Conn, error)

// NetDialer is a simple dialer that uses net.Dial directly.
// In TinyGo WASM builds, NetDialFunc overrides this to use WasmNetdev.DialTCP.
type NetDialer struct{}

func (d *NetDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if NetDialFunc != nil {
		return NetDialFunc(network, address)
	}
	return net.Dial(network, address)
}

func (d *NetDialer) Dial(network, address string) (net.Conn, error) {
	if NetDialFunc != nil {
		return NetDialFunc(network, address)
	}
	return net.Dial(network, address)
}
