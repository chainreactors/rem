package packetconntest

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Addr struct {
	network string
	value   string
}

func NewAddr(network, value string) Addr {
	if network == "" {
		network = "packet"
	}
	return Addr{
		network: network,
		value:   value,
	}
}

func (a Addr) Network() string { return a.network }

func (a Addr) String() string { return a.value }

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type packet struct {
	data []byte
	addr net.Addr
}

type PipePacketConn struct {
	localAddr net.Addr
	peerAddr  net.Addr

	sendCh chan packet
	recvCh chan packet

	closeCh   chan struct{}
	closeOnce *sync.Once
	closed    int32

	readDeadline  int64
	writeDeadline int64
}

func NewPacketPipe() (*PipePacketConn, *PipePacketConn) {
	return NewPacketPipeWithAddrs(
		NewAddr("packet", "pipe-a"),
		NewAddr("packet", "pipe-b"),
	)
}

func NewPacketPipeWithAddrs(localA, localB net.Addr) (*PipePacketConn, *PipePacketConn) {
	chAB := make(chan packet, 4096)
	chBA := make(chan packet, 4096)
	closeCh := make(chan struct{})
	closeOnce := &sync.Once{}

	a := &PipePacketConn{
		localAddr: localA,
		peerAddr:  localB,
		sendCh:    chAB,
		recvCh:    chBA,
		closeCh:   closeCh,
		closeOnce: closeOnce,
	}
	b := &PipePacketConn{
		localAddr: localB,
		peerAddr:  localA,
		sendCh:    chBA,
		recvCh:    chAB,
		closeCh:   closeCh,
		closeOnce: closeOnce,
	}
	return a, b
}

func (p *PipePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if atomic.LoadInt32(&p.closed) != 0 {
		return 0, nil, net.ErrClosed
	}

	return p.readPacket(b, atomic.LoadInt64(&p.readDeadline))
}

func (p *PipePacketConn) readPacket(b []byte, deadline int64) (int, net.Addr, error) {
	if deadline > 0 {
		remaining := time.Until(time.Unix(0, deadline))
		if remaining <= 0 {
			return 0, nil, timeoutError{}
		}
		timer := time.NewTimer(remaining)
		defer timer.Stop()

		select {
		case pkt := <-p.recvCh:
			return copy(b, pkt.data), pkt.addr, nil
		case <-timer.C:
			return 0, nil, timeoutError{}
		case <-p.closeCh:
			return 0, nil, net.ErrClosed
		}
	}

	select {
	case pkt := <-p.recvCh:
		return copy(b, pkt.data), pkt.addr, nil
	case <-p.closeCh:
		return 0, nil, net.ErrClosed
	}
}

func (p *PipePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if atomic.LoadInt32(&p.closed) != 0 {
		return 0, net.ErrClosed
	}

	pkt := packet{
		data: make([]byte, len(b)),
		addr: p.localAddr,
	}
	copy(pkt.data, b)
	if addr != nil {
		pkt.addr = addr
	}

	deadline := atomic.LoadInt64(&p.writeDeadline)
	if deadline > 0 {
		remaining := time.Until(time.Unix(0, deadline))
		if remaining <= 0 {
			return 0, timeoutError{}
		}
		timer := time.NewTimer(remaining)
		defer timer.Stop()

		select {
		case p.sendCh <- pkt:
			return len(b), nil
		case <-timer.C:
			return 0, timeoutError{}
		case <-p.closeCh:
			return 0, net.ErrClosed
		}
	}

	select {
	case p.sendCh <- pkt:
		return len(b), nil
	case <-p.closeCh:
		return 0, net.ErrClosed
	case <-time.After(5 * time.Second):
		return 0, net.ErrClosed
	}
}

func (p *PipePacketConn) Close() error {
	if atomic.CompareAndSwapInt32(&p.closed, 0, 1) {
		p.closeOnce.Do(func() {
			close(p.closeCh)
		})
	}
	return nil
}

func (p *PipePacketConn) LocalAddr() net.Addr { return p.localAddr }

func (p *PipePacketConn) SetDeadline(t time.Time) error {
	if err := p.SetReadDeadline(t); err != nil {
		return err
	}
	return p.SetWriteDeadline(t)
}

func (p *PipePacketConn) SetReadDeadline(t time.Time) error {
	atomic.StoreInt64(&p.readDeadline, deadlineNano(t))
	return nil
}

func (p *PipePacketConn) SetWriteDeadline(t time.Time) error {
	atomic.StoreInt64(&p.writeDeadline, deadlineNano(t))
	return nil
}

func deadlineNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

type MakePacketPipe func() (c1, c2 net.PacketConn, stop func(), err error)

func TestPacketConnBasic(makePipe MakePacketPipe) error {
	c1, c2, stop, err := makePipe()
	if err != nil {
		return err
	}
	defer stop()

	payload := []byte("packetconntest")
	gotCh := make(chan []byte, 1)
	errCh := make(chan error, 1)

	go func() {
		buf := make([]byte, 64)
		n, _, readErr := c2.ReadFrom(buf)
		if readErr != nil {
			errCh <- readErr
			return
		}
		got := make([]byte, n)
		copy(got, buf[:n])
		gotCh <- got
	}()

	if _, err = c1.WriteTo(payload, c2.LocalAddr()); err != nil {
		return err
	}

	select {
	case got := <-gotCh:
		if string(got) != string(payload) {
			return errors.New("packet payload mismatch")
		}
	case err = <-errCh:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("packet read timed out")
	}

	return nil
}
