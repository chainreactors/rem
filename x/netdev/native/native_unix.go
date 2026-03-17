//go:build tinygo && !wasip1 && darwin

package native

import (
	"github.com/chainreactors/rem/x/netdev"
	"github.com/chainreactors/rem/x/netdev/unix"
)

// New creates a native TinyGo netdev for macOS.
func New() netdev.Netdever {
	return unix.New()
}
