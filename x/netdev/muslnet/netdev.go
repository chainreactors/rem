//go:build tinygo && !wasip1 && linux

package muslnet

import (
	"github.com/chainreactors/rem/x/netdev"
	"github.com/chainreactors/rem/x/netdev/muslnet/internal/musl"
)

// New returns a Linux native TinyGo netdev implementation based on musl.
func New() netdev.Netdever {
	return musl.NewNetdever()
}
