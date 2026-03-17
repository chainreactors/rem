package runner

import (
	"net"
	"testing"

	"github.com/chainreactors/rem/internal/netconntest"
)

func TestMergeHalfConn_NetConn(t *testing.T) {
	netconntest.TestConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		write1, read2 := net.Pipe()
		write2, read1 := net.Pipe()

		c1 = mergeHalfConn(read1, write1)
		c2 = mergeHalfConn(read2, write2)

		stop = func() {
			_ = c1.Close()
			_ = c2.Close()
		}
		return c1, c2, stop, nil
	})
}
