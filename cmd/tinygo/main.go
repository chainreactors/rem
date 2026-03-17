//go:build tinygo

package main

import (
	"github.com/chainreactors/rem/cmd/cmd"
	_ "github.com/chainreactors/rem/runner"
	"github.com/chainreactors/rem/x/netdev"
	"github.com/chainreactors/rem/x/netdev/native"
)

func main() {
	netdev.UseNetdev(native.New())
	cmd.RUN()
}
