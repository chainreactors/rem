//go:build tinygo && !wasip1 && linux

package native

import (
	"github.com/chainreactors/rem/x/netdev"
	"github.com/chainreactors/rem/x/netdev/muslnet"
)

// New creates a native TinyGo netdev for Linux.
func New() netdev.Netdever {
	return muslnet.New()
}
