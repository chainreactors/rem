//go:build !tinygo

package streamhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chainreactors/rem/x/utils"
)

// connBase holds fields and methods shared by server and client conns.
type connBase struct {
	localAddr  net.Addr
	remoteAddr net.Addr
	readBuf    *utils.Buffer
	done       chan struct{}
	closeOnce  sync.Once
}

func (b *connBase) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := b.readBuf.ReadAtLeast(p)
	if err == io.ErrClosedPipe {
		return n, io.EOF
	}
	return n, err
}

func (b *connBase) LocalAddr() net.Addr              { return b.localAddr }
func (b *connBase) RemoteAddr() net.Addr             { return b.remoteAddr }
func (b *connBase) SetDeadline(time.Time) error      { return nil }
func (b *connBase) SetReadDeadline(time.Time) error  { return nil }
func (b *connBase) SetWriteDeadline(time.Time) error { return nil }

// ════════════════════════════════════════════════════════════
//  streamHTTPConn — server-side net.Conn (returned by Accept)
// ════════════════════════════════════════════════════════════

type streamHTTPConn struct {
	connBase
	replayBuf *replayBuffer // App.Write() → here → downlink goroutine
	onClose   func()
}

func newServerConn(local, remote net.Addr, onClose func()) *streamHTTPConn {
	return &streamHTTPConn{
		connBase: connBase{
			localAddr:  local,
			remoteAddr: remote,
			readBuf:    utils.NewBuffer(readBufSize),
			done:       make(chan struct{}),
		},
		replayBuf: newReplayBuffer(replayCapacity),
		onClose:   onClose,
	}
}

func (c *streamHTTPConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	written := 0
	for written < len(p) {
		end := written + maxFramePayload
		if end > len(p) {
			end = len(p)
		}
		chunk := p[written:end]

		select {
		case <-c.done:
			return written, io.ErrClosedPipe
		default:
		}

		c.replayBuf.Append(chunk)
		written = end
	}
	return len(p), nil
}

func (c *streamHTTPConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.replayBuf.Close()
		_ = c.readBuf.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}

// ════════════════════════════════════════════════════════════
//  streamHTTPClientConn — client-side net.Conn (returned by Dial)
// ════════════════════════════════════════════════════════════

type streamHTTPClientConn struct {
	connBase
	writeBuf *utils.Buffer // App.Write() writes, POST goroutine reads

	sessionID string
	reqURL    string
	client    *http.Client
	transport *http.Transport
	lastSeqID atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc

	sseReady  chan struct{} // closed when first downlink connects
	postReady chan struct{} // closed when first POST connects
}

func (c *streamHTTPClientConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return c.writeBuf.Write(p)
}

func (c *streamHTTPClientConn) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		close(c.done)
		_ = c.readBuf.Close()
		_ = c.writeBuf.Close()
		c.transport.CloseIdleConnections()
	})
	return nil
}
