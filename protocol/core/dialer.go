package core

import (
	"context"
	"net"
)

const ContextDialerMetaKey = "contextDialer"

type ContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Dial(network, address string) (net.Conn, error)
}

func GetContextDialer(meta Metas) ContextDialer {
	if meta != nil {
		if dialer, ok := meta[ContextDialerMetaKey].(ContextDialer); ok && dialer != nil {
			return dialer
		}
	}
	return &NetDialer{}
}
