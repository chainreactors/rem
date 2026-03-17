//go:build tinygo

package runner

import (
	_ "github.com/chainreactors/rem/protocol/serve/portforward"
	_ "github.com/chainreactors/rem/protocol/serve/raw"
	_ "github.com/chainreactors/rem/protocol/serve/socks"
	_ "github.com/chainreactors/rem/protocol/tunnel/memory"
	_ "github.com/chainreactors/rem/protocol/tunnel/tcp"
	_ "github.com/chainreactors/rem/protocol/wrapper"
)
