package netconntest

import (
	"net"
	"testing"

	"golang.org/x/net/nettest"
)

type MakePipe func() (c1, c2 net.Conn, stop func(), err error)

func TestConn(t *testing.T, makePipe MakePipe) {
	t.Helper()
	nettest.TestConn(t, func() (net.Conn, net.Conn, func(), error) {
		return makePipe()
	})
}
