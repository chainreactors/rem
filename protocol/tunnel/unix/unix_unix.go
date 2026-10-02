//go:build !windows
// +build !windows

package unix

import (
	"context"
	"net"
	"os"

	"github.com/chainreactors/rem/protocol/core"
)

func NewUnixDialer(ctx context.Context) *UnixDialer {
	return &UnixDialer{
		meta: core.GetMetas(ctx),
	}
}

func (c *UnixDialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	c.meta["url"] = u
	return net.Dial("unix", u.Path)
}

func NewUnixListener(ctx context.Context) *UnixListener {
	return &UnixListener{
		meta: core.GetMetas(ctx),
	}
}

func (c *UnixListener) Listen(dst string) (net.Listener, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	c.meta["url"] = u

	// Remove existing socket file if it exists
	os.Remove(u.Path)

	listener, err := net.Listen("unix", u.Path)
	if err != nil {
		return nil, err
	}
	c.listener = listener
	return listener, nil
}
