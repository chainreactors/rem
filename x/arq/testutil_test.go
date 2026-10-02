package arq

import (
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harq "github.com/chainreactors/rem/harness/arq"
)

// === Shared test infrastructure for x/arq tests ===

const ARQ_INTERVAL = 100

// mockAddr is a minimal net.Addr implementation for testing.
type mockAddr struct{ id string }

func (a *mockAddr) Network() string { return "mock" }
func (a *mockAddr) String() string  { return a.id }

// intervalMockAddr extends mockAddr with an Interval() method,
// used to test interval-aware code paths.
type intervalMockAddr struct {
	mockAddr
	interval time.Duration
}

func (a *intervalMockAddr) Interval() time.Duration { return a.interval }

// packetEntry represents a single packet in the mock conn's queue.
type packetEntry struct {
	data []byte
	addr net.Addr
	err  error
}

// mockPacketConn is an in-memory net.PacketConn for unit testing ARQ sessions.
type mockPacketConn struct {
	mu        sync.Mutex
	incoming  []packetEntry
	outgoing  []packetEntry
	closed    int32
	readCount int64

	disconnectMu sync.Mutex
	onDisconnect func(net.Addr)
}

func newMockPacketConn() *mockPacketConn {
	return &mockPacketConn{}
}

func (m *mockPacketConn) Inject(data []byte, addr net.Addr) {
	d := make([]byte, len(data))
	copy(d, data)
	m.mu.Lock()
	m.incoming = append(m.incoming, packetEntry{data: d, addr: addr})
	m.mu.Unlock()
}

func (m *mockPacketConn) InjectError(err error, addr net.Addr) {
	m.mu.Lock()
	m.incoming = append(m.incoming, packetEntry{addr: addr, err: err})
	m.mu.Unlock()
}

func (m *mockPacketConn) PendingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.incoming)
}

func (m *mockPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	atomic.AddInt64(&m.readCount, 1)
	if atomic.LoadInt32(&m.closed) != 0 {
		return 0, nil, net.ErrClosed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.incoming) > 0 {
		entry := m.incoming[0]
		m.incoming = m.incoming[1:]
		if entry.err != nil {
			return 0, entry.addr, entry.err
		}
		n := copy(p, entry.data)
		return n, entry.addr, nil
	}
	return 0, nil, &timeoutError{}
}

func (m *mockPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	d := make([]byte, len(p))
	copy(d, p)
	m.mu.Lock()
	m.outgoing = append(m.outgoing, packetEntry{data: d, addr: addr})
	m.mu.Unlock()
	return len(p), nil
}

func (m *mockPacketConn) Close() error {
	atomic.StoreInt32(&m.closed, 1)
	return nil
}

func (m *mockPacketConn) OnDisconnect(fn func(net.Addr)) {
	m.disconnectMu.Lock()
	m.onDisconnect = fn
	m.disconnectMu.Unlock()
}

func (m *mockPacketConn) TriggerDisconnect(addr net.Addr) {
	m.disconnectMu.Lock()
	fn := m.onDisconnect
	m.disconnectMu.Unlock()
	if fn != nil {
		fn(addr)
	}
}

func (m *mockPacketConn) LocalAddr() net.Addr                { return &mockAddr{"local"} }
func (m *mockPacketConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockPacketConn) SetWriteDeadline(t time.Time) error { return nil }

// makeARQDataPacket builds a raw ARQ DATA packet with the given SN and payload.
func makeARQDataPacket(sn uint32, payload []byte) []byte {
	return makeARQPacket(CMD_DATA, sn, 0, payload)
}

func makeARQFINPacket(sn uint32) []byte {
	return makeARQPacket(CMD_FIN, sn, 0, nil)
}

func makeARQACKPacket(ack uint32) []byte {
	return makeARQPacket(CMD_ACK, 0, ack, nil)
}

func makeARQPacket(cmd uint8, sn, ack uint32, payload []byte) []byte {
	buf := make([]byte, ARQ_OVERHEAD+len(payload))
	buf[0] = cmd
	binary.BigEndian.PutUint32(buf[1:5], sn)
	binary.BigEndian.PutUint32(buf[5:9], ack)
	binary.BigEndian.PutUint16(buf[9:11], uint16(len(payload)))
	copy(buf[ARQ_OVERHEAD:], payload)
	return buf
}

// acceptWithTimeout accepts a session from the listener with a timeout guard.
func acceptWithTimeout(t *testing.T, l *ARQListener, timeout time.Duration) net.Conn {
	t.Helper()
	var sess net.Conn
	done := make(chan struct{})
	go func() {
		sess, _ = l.Accept()
		close(done)
	}()
	select {
	case <-done:
		return sess
	case <-time.After(timeout):
		t.Fatal("Accept timed out")
		return nil
	}
}

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
	closeCh      chan struct{}
	closeOnce    *sync.Once
	sendMu       sync.RWMutex
}

func newPacketPipe() (a, b *pipePacketConn) {
	chAB := make(chan []byte, 4096)
	chBA := make(chan []byte, 4096)
	addrA := &mockAddr{"pipe-a"}
	addrB := &mockAddr{"pipe-b"}

	a = &pipePacketConn{
		localAddr: addrA,
		peerAddr:  addrB,
		sendCh:    chAB,
		recvCh:    chBA,
		closeCh:   make(chan struct{}),
		closeOnce: &sync.Once{},
	}
	b = &pipePacketConn{
		localAddr: addrB,
		peerAddr:  addrA,
		sendCh:    chBA,
		recvCh:    chAB,
		closeCh:   make(chan struct{}),
		closeOnce: &sync.Once{},
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
		case data, ok := <-p.recvCh:
			if !ok {
				return 0, nil, net.ErrClosed
			}
			return copy(b, data), p.peerAddr, nil
		case <-timer.C:
			return 0, nil, &timeoutError{}
		case <-p.closeCh:
			return 0, nil, net.ErrClosed
		}
	}

	// No deadline — block until data or close
	select {
	case data, ok := <-p.recvCh:
		if !ok {
			return 0, nil, net.ErrClosed
		}
		return copy(b, data), p.peerAddr, nil
	case <-p.closeCh:
		return 0, nil, net.ErrClosed
	}
}

func (p *pipePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	p.sendMu.RLock()
	defer p.sendMu.RUnlock()
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
		p.sendMu.Lock()
		defer p.sendMu.Unlock()
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
// arqMakePipe: creates two ARQSessions connected via a lossless pipe
// (nettest.MakePipe signature)
// ---------------------------------------------------------------------------

func arqMakePipe() (c1, c2 net.Conn, stop func(), err error) {
	pA, pB := newPacketPipe()
	sessA := NewARQSession(pA, pB.localAddr, 1024, 0)
	sessB := NewARQSession(pB, pA.localAddr, 1024, 0)
	stop = func() {
		sessA.Close()
		sessB.Close()
		time.Sleep(50 * time.Millisecond)
	}
	return sessA, sessB, stop, nil
}

// ---------------------------------------------------------------------------
// faultyPipePacketConn: wraps pipePacketConn with per-direction packet loss
// ---------------------------------------------------------------------------

var faultyCounter uint64

func cheapRand() int {
	return int(atomic.AddUint64(&faultyCounter, 6364136223846793005) >> 33)
}

type faultyPipePacketConn struct {
	*pipePacketConn
	dropRate int32 // atomic, 0-100
}

func (f *faultyPipePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	rate := int(atomic.LoadInt32(&f.dropRate))
	if rate > 0 && cheapRand()%100 < rate {
		return len(b), nil
	}
	return f.pipePacketConn.WriteTo(b, addr)
}

func newFaultyPacketPipe() (a, b *faultyPipePacketConn) {
	pa, pb := newPacketPipe()
	return &faultyPipePacketConn{pipePacketConn: pa},
		&faultyPipePacketConn{pipePacketConn: pb}
}

// pipeFaultController implements harq.FaultController and AsymFaultController.
type pipeFaultController struct {
	a, b *faultyPipePacketConn
}

func (fc *pipeFaultController) SetDropRate(percent int) {
	atomic.StoreInt32(&fc.a.dropRate, int32(percent))
	atomic.StoreInt32(&fc.b.dropRate, int32(percent))
}

func (fc *pipeFaultController) SetPartition(active bool) {
	if active {
		fc.SetDropRate(100)
	} else {
		fc.SetDropRate(0)
	}
}

func (fc *pipeFaultController) SetDropRateAtoB(percent int) {
	atomic.StoreInt32(&fc.a.dropRate, int32(percent))
}

func (fc *pipeFaultController) SetDropRateBtoA(percent int) {
	atomic.StoreInt32(&fc.b.dropRate, int32(percent))
}

func arqMakeFaultyPipe(t *testing.T) (c1, c2 net.Conn, faults harq.FaultController, stop func(), err error) {
	t.Helper()
	fA, fB := newFaultyPacketPipe()
	cfg := ARQConfig{MTU: 1024, RTO: 500, MaxRetransmissions: 30}
	sessA := NewARQSessionWithConfig(fA, fB.localAddr, cfg)
	sessB := NewARQSessionWithConfig(fB, fA.localAddr, cfg)
	fc := &pipeFaultController{a: fA, b: fB}
	stop = func() {
		sessA.Close()
		sessB.Close()
		time.Sleep(50 * time.Millisecond)
	}
	return sessA, sessB, fc, stop, nil
}
