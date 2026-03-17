package cio

import (
	"context"
	"errors"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

func JoinWithError(c1 io.ReadWriteCloser, c2 io.ReadWriteCloser) (inCount int64, outCount int64, errors []error) {
	var wait sync.WaitGroup
	recordErrs := make([]error, 2)
	pipe := func(number int, to io.ReadWriteCloser, from io.ReadWriteCloser, count *int64) {
		defer func() {
			wait.Done()
			to.Close()
			from.Close()
		}()
		buf := GetBuf(16 * 1024)
		defer PutBuf(buf)
		*count, recordErrs[number] = io.CopyBuffer(to, from, buf)
	}

	wait.Add(2)
	go pipe(0, c1, c2, &inCount)
	go pipe(1, c2, c1, &outCount)
	wait.Wait()

	for _, e := range recordErrs {
		if e != nil {
			errors = append(errors, e)
		}
	}
	return
}

// Join two io.ReadWriteCloser and do some operations.
func Join(c1 io.ReadWriteCloser, c2 io.ReadWriteCloser) (inCount int64, outCount int64) {
	var wait sync.WaitGroup
	pipe := func(to io.ReadWriteCloser, from io.ReadWriteCloser, count *int64) {
		defer func() {
			to.Close()
			from.Close()
			wait.Done()
		}()

		buf := GetBuf(16 * 1024)
		defer PutBuf(buf)
		var err error
		*count, err = io.CopyBuffer(to, from, buf)
		if err != nil {
			utils.Log.Debug(err)
		}
	}

	wait.Add(2)
	go pipe(c1, c2, &inCount)
	go pipe(c2, c1, &outCount)
	wait.Wait()
	return
}

// closeFn will be called only once
func WrapConn(conn net.Conn, rwc io.ReadWriteCloser) net.Conn {
	return &WrappedConn{
		rwc:  rwc,
		Conn: conn,
	}
}

// WrapConnWithUnderlyingClose exposes rwc as a net.Conn while ensuring Close
// also tears down the underlying transport.
func WrapConnWithUnderlyingClose(conn net.Conn, rwc io.ReadWriteCloser) net.Conn {
	return WrapConn(conn, WrapReadWriteCloser(rwc, rwc, func() error {
		return closeAllIgnoreClosed(rwc, conn)
	}))
}

type WrappedConn struct {
	rwc io.ReadWriteCloser
	net.Conn
}

func (conn *WrappedConn) Read(p []byte) (n int, err error) {
	return conn.rwc.Read(p)
}

func (conn *WrappedConn) Write(p []byte) (n int, err error) {
	return conn.rwc.Write(p)
}

func (conn *WrappedConn) Close() error {
	return conn.rwc.Close()
}

func closeAllIgnoreClosed(closers ...io.Closer) error {
	var errs []error
	for _, closer := range closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type ReadWriteCloser struct {
	r       io.Reader
	w       io.Writer
	closeFn func() error

	closed bool
	mu     sync.Mutex
}

func WrapReadWriteCloser(r io.Reader, w io.Writer, closeFn func() error) io.ReadWriteCloser {
	return &ReadWriteCloser{
		r:       r,
		w:       w,
		closeFn: closeFn,
		closed:  false,
	}
}

func (rwc *ReadWriteCloser) Read(p []byte) (n int, err error) {
	return rwc.r.Read(p)
}

func (rwc *ReadWriteCloser) Write(p []byte) (n int, err error) {
	return rwc.w.Write(p)
}

func (rwc *ReadWriteCloser) Close() error {
	rwc.mu.Lock()
	if rwc.closed {
		rwc.mu.Unlock()
		return nil
	}
	rwc.closed = true
	rwc.mu.Unlock()

	if rwc.closeFn != nil {
		return rwc.closeFn()
	}
	return nil
}

// LimitedConn 限速连接，每个连接有独立的限速器
type LimitedConn struct {
	net.Conn
	ReadCount  int64
	WriteCount int64
	Reader     *TokenBucketLimiter
	Writer     *TokenBucketLimiter
}

func NewLimitedConn(conn net.Conn) *LimitedConn {
	return &LimitedConn{
		Conn:   conn,
		Reader: NewTokenBucketLimiter(core.MaxBufferSize, true),
		Writer: NewTokenBucketLimiter(core.MaxBufferSize, false),
	}
}

func (l *LimitedConn) Read(p []byte) (n int, err error) {
	n, err = l.Conn.Read(p)
	if n > 0 {
		// 等待令牌
		if err := l.Reader.WaitForTokens(context.Background(), int64(n)); err != nil {
			return n, err
		}
		atomic.AddInt64(&l.ReadCount, int64(n))
		atomic.AddInt64(&l.Reader.readCount, int64(n))
	}
	return
}

func (l *LimitedConn) Write(p []byte) (n int, err error) {
	// 等待令牌
	if err := l.Writer.WaitForTokens(context.Background(), int64(len(p))); err != nil {
		return 0, err
	}

	n, err = l.Conn.Write(p)
	if n > 0 {
		atomic.AddInt64(&l.WriteCount, int64(n))
		atomic.AddInt64(&l.Writer.writeCount, int64(n))
	}
	return
}
