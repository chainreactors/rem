//go:build tinygo && !wasip1 && windows

package native

import (
	"github.com/chainreactors/rem/x/netdev"
	"github.com/chainreactors/rem/x/netdev/windows"
)

// New creates a native TinyGo netdev for Windows.
func New() netdev.Netdever {
	return windows.New()
}
