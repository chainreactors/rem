package yamux

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/chainreactors/rem/internal/netconntest"
)

func TestStream_NetConn(t *testing.T) {
	netconntest.TestConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		rawClient, rawServer := net.Pipe()

		cfg := DefaultConfig()
		cfg.LogOutput = io.Discard
		cfg.EnableKeepAlive = false
		cfg.ConnectionWriteTimeout = 5 * time.Second
		cfg.StreamOpenTimeout = 5 * time.Second
		cfg.StreamCloseTimeout = time.Second

		clientSession, err := Client(rawClient, cfg)
		if err != nil {
			_ = rawClient.Close()
			_ = rawServer.Close()
			return nil, nil, nil, err
		}
		serverSession, err := Server(rawServer, cfg.Clone())
		if err != nil {
			_ = clientSession.Close()
			_ = rawClient.Close()
			_ = rawServer.Close()
			return nil, nil, nil, err
		}

		type acceptResult struct {
			conn *Stream
			err  error
		}
		acceptCh := make(chan acceptResult, 1)
		go func() {
			conn, acceptErr := serverSession.AcceptStream()
			acceptCh <- acceptResult{conn: conn, err: acceptErr}
		}()

		clientStream, err := clientSession.OpenStream()
		if err != nil {
			_ = serverSession.Close()
			_ = clientSession.Close()
			_ = rawClient.Close()
			_ = rawServer.Close()
			return nil, nil, nil, err
		}

		var serverStream *Stream
		select {
		case accepted := <-acceptCh:
			if accepted.err != nil {
				_ = clientStream.Close()
				_ = serverSession.Close()
				_ = clientSession.Close()
				_ = rawClient.Close()
				_ = rawServer.Close()
				return nil, nil, nil, accepted.err
			}
			serverStream = accepted.conn
		case <-time.After(5 * time.Second):
			_ = clientStream.Close()
			_ = serverSession.Close()
			_ = clientSession.Close()
			_ = rawClient.Close()
			_ = rawServer.Close()
			return nil, nil, nil, contextDeadlineExceededError{}
		}

		stop = func() {
			_ = clientStream.Close()
			_ = serverStream.Close()
			_ = clientSession.Close()
			_ = serverSession.Close()
			_ = rawClient.Close()
			_ = rawServer.Close()
		}
		return clientStream, serverStream, stop, nil
	})
}

type contextDeadlineExceededError struct{}

func (contextDeadlineExceededError) Error() string { return "deadline exceeded" }
