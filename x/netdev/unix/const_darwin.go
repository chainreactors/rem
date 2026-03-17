//go:build tinygo && !wasip1 && darwin

package unix

const (
	errAgain      = 35
	errWouldBlock = 35
	errInProgress = 36
	errIntr       = 4
	pollOut       = 0x0004
)
