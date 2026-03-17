package cio

import (
	"net"
	"testing"

	"github.com/chainreactors/rem/internal/netconntest"
)

func TestWrapConn_NetConn(t *testing.T) {
	netconntest.TestConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		raw1, raw2 := net.Pipe()
		c1 = WrapConn(raw1, WrapReadWriteCloser(raw1, raw1, raw1.Close))
		c2 = WrapConn(raw2, WrapReadWriteCloser(raw2, raw2, raw2.Close))
		stop = func() {
			_ = c1.Close()
			_ = c2.Close()
		}
		return c1, c2, stop, nil
	})
}

func TestWrapConnWithUnderlyingClose_NetConn(t *testing.T) {
	netconntest.TestConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		raw1, raw2 := net.Pipe()
		c1 = WrapConnWithUnderlyingClose(raw1, WrapReadWriteCloser(raw1, raw1, nil))
		c2 = WrapConnWithUnderlyingClose(raw2, WrapReadWriteCloser(raw2, raw2, nil))
		stop = func() {
			_ = c1.Close()
			_ = c2.Close()
		}
		return c1, c2, stop, nil
	})
}

func TestLimitedConn_NetConn(t *testing.T) {
	netconntest.TestConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		raw1, raw2 := net.Pipe()
		c1 = NewLimitedConn(raw1)
		c2 = NewLimitedConn(raw2)
		stop = func() {
			_ = c1.Close()
			_ = c2.Close()
		}
		return c1, c2, stop, nil
	})
}
