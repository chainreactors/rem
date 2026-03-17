package core

import "sync"

const (
	ErrCmdParseFailed  = 1
	ErrArgsParseFailed = 2
	ErrPrepareFailed   = 3
	ErrNoConsoleURL    = 4
	ErrCreateConsole   = 5
	ErrDialFailed      = 6
)

var Conns sync.Map
