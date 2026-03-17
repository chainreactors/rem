//go:build !tinygo

package main

import "github.com/chainreactors/rem/protocol/core"

const (
	ErrCmdParseFailed  = core.ErrCmdParseFailed
	ErrArgsParseFailed = core.ErrArgsParseFailed
	ErrPrepareFailed   = core.ErrPrepareFailed
	ErrNoConsoleURL    = core.ErrNoConsoleURL
	ErrCreateConsole   = core.ErrCreateConsole
	ErrDialFailed      = core.ErrDialFailed
)

var conns = &core.Conns

func main() {}
