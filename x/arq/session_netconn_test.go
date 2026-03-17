package arq

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/nettest"
)

// ---------------------------------------------------------------------------
// pipePacketConn: lossless, in-order, bidirectional PacketConn pair for testing
// ---------------------------------------------------------------------------

type pipePacketConn struct {
	localAddr    net.Addr
	peerAddr     net.Addr
	sendCh       chan []byte
	recvCh       chan []byte
	closed       int32
	readDeadline int64 // UnixNano, 0 = no deadline
	closeCh      chan struct{} // shared between peers: either close → both see it
	closeOnce    *sync.Once
}

func newPacketPipe() (a, b *pipePacketConn) {
	chAB := make(chan []byte, 4096)
	chBA := make(chan []byte, 4096)
	addrA := &mockAddr{"pipe-a"}
	addrB := &mockAddr{"pipe-b"}
	closeCh := make(chan struct{})
	closeOnce := &sync.Once{}

	a = &pipePacketConn{
		localAddr: addrA,
		peerAddr:  addrB,
		sendCh:    chAB,
		recvCh:    chBA,
		closeCh:   closeCh,
		closeOnce: closeOnce,
	}
	b = &pipePacketConn{
		localAddr: addrB,
		peerAddr:  addrA,
		sendCh:    chBA,
		recvCh:    chAB,
		closeCh:   closeCh,
		closeOnce: closeOnce,
	}
	return a, b
}

func (p *pipePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if atomic.LoadInt32(&p.closed) != 0 {
		return 0, nil, net.ErrClosed
	}

	deadline := atomic.LoadInt64(&p.readDeadline)
	if deadline > 0 {
		remaining := time.Duration(deadline - time.Now().UnixNano())
		if remaining <= 0 {
			return 0, nil, &timeoutError{}
		}
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case data := <-p.recvCh:
			return copy(b, data), p.peerAddr, nil
		case <-timer.C:
			return 0, nil, &timeoutError{}
		case <-p.closeCh:
			return 0, nil, net.ErrClosed
		}
	}

	// No deadline — block until data or close
	select {
	case data := <-p.recvCh:
		return copy(b, data), p.peerAddr, nil
	case <-p.closeCh:
		return 0, nil, net.ErrClosed
	}
}

func (p *pipePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if atomic.LoadInt32(&p.closed) != 0 {
		return 0, net.ErrClosed
	}
	data := make([]byte, len(b))
	copy(data, b)
	select {
	case p.sendCh <- data:
		return len(b), nil
	case <-time.After(5 * time.Second):
		return 0, net.ErrClosed
	}
}

func (p *pipePacketConn) Close() error {
	if atomic.CompareAndSwapInt32(&p.closed, 0, 1) {
		p.closeOnce.Do(func() { close(p.closeCh) })
	}
	return nil
}

func (p *pipePacketConn) LocalAddr() net.Addr { return p.localAddr }

func (p *pipePacketConn) SetDeadline(t time.Time) error {
	return p.SetReadDeadline(t)
}

func (p *pipePacketConn) SetReadDeadline(t time.Time) error {
	nano := int64(0)
	if !t.IsZero() {
		nano = t.UnixNano()
	}
	atomic.StoreInt64(&p.readDeadline, nano)
	return nil
}

func (p *pipePacketConn) SetWriteDeadline(t time.Time) error {
	return nil
}

// ---------------------------------------------------------------------------
// MakePipe: creates two ARQSessions connected via a lossless pipe
// ---------------------------------------------------------------------------

func arqMakePipe() (c1, c2 net.Conn, stop func(), err error) {
	pA, pB := newPacketPipe()
	sessA := NewARQSession(pA, pB.localAddr, 1024, 0)
	sessB := NewARQSession(pB, pA.localAddr, 1024, 0)
	stop = func() {
		sessA.Close()
		sessB.Close()
		// Allow background goroutines to detect close and exit
		// before the test framework invalidates the test's t.
		time.Sleep(50 * time.Millisecond)
	}
	return sessA, sessB, stop, nil
}

// ---------------------------------------------------------------------------
// golang.org/x/net/nettest conformance test suite
// ---------------------------------------------------------------------------

func TestARQSession_NetConn(t *testing.T) {
	nettest.TestConn(t, arqMakePipe)
}
