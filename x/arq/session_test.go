package arq

import (
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// === Mock infrastructure ===

type mockAddr struct{ id string }

func (a *mockAddr) Network() string { return "mock" }
func (a *mockAddr) String() string  { return a.id }

type packetEntry struct {
	data []byte
	addr net.Addr
}

type mockPacketConn struct {
	mu        sync.Mutex
	incoming  []packetEntry
	outgoing  []packetEntry
	closed    int32
	readCount int64
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

func (m *mockPacketConn) LocalAddr() net.Addr                { return &mockAddr{"local"} }
func (m *mockPacketConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockPacketConn) SetWriteDeadline(t time.Time) error { return nil }

func makeARQDataPacket(sn uint32, payload []byte) []byte {
	buf := make([]byte, ARQ_OVERHEAD+len(payload))
	buf[0] = CMD_DATA
	binary.BigEndian.PutUint32(buf[1:5], sn)
	binary.BigEndian.PutUint32(buf[5:9], 0) // ack = 0
	binary.BigEndian.PutUint16(buf[9:11], uint16(len(payload)))
	copy(buf[ARQ_OVERHEAD:], payload)
	return buf
}

// acceptWithTimeout is a test helper that accepts a session with a timeout.
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

// ========================== Regression Tests ==========================
// These tests assert CORRECT behavior. They should FAIL with the current
// buggy code and PASS after the fix is applied.

// TestListenerCleansUpClosedSessions verifies that when ARQSession.Close()
// is called, the session is removed from listener.sessions map.
//
// ROOT CAUSE: ARQSession.Close() does not notify the listener. The chClosed
// channel is defined in ARQListener but never written to.
func TestListenerCleansUpClosedSessions(t *testing.T) {
	conn := newMockPacketConn()
	listener, err := ServeConn(conn, 1400, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	addr := &mockAddr{"client-1"}
	conn.Inject(makeARQDataPacket(0, []byte("hello")), addr)

	sess := acceptWithTimeout(t, listener, 2*time.Second)

	// Verify session exists
	listener.sessionLock.RLock()
	_, exists := listener.sessions[addr.String()]
	listener.sessionLock.RUnlock()
	if !exists {
		t.Fatal("precondition: session should exist in listener.sessions after Accept")
	}

	// Close session (simulates yamux death)
	sess.Close()
	time.Sleep(200 * time.Millisecond)

	// EXPECTED: closed session should be removed from the map
	listener.sessionLock.RLock()
	_, stillExists := listener.sessions[addr.String()]
	listener.sessionLock.RUnlock()
	if stillExists {
		t.Fatal("closed session still in listener.sessions — cleanup not implemented")
	}
}

// TestBackgroundLoopDoesNotStealData verifies that when a session is managed
// by a listener, its backgroundLoop does NOT call ReadFrom on the shared conn.
// Only the listener's monitor should read from the shared conn.
//
// ROOT CAUSE: backgroundLoop calls s.conn.ReadFrom() which competes with the
// monitor. It reads data for other addresses and discards it (data loss).
//
// This test injects data for a DIFFERENT address and verifies it is not consumed
// by the existing session's backgroundLoop. With the fix, managed sessions skip
// ReadFrom in backgroundLoop, and the data stays available for the monitor.
func TestBackgroundLoopDoesNotStealData(t *testing.T) {
	conn := newMockPacketConn()
	addrA := &mockAddr{"client-A"}
	addrB := &mockAddr{"client-B"}

	listener, err := ServeConn(conn, 1400, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Create session A via listener
	conn.Inject(makeARQDataPacket(0, []byte("init-A")), addrA)
	acceptWithTimeout(t, listener, 2*time.Second)

	// Give session A's backgroundLoop time to start consuming
	time.Sleep(100 * time.Millisecond)

	// Inject data for addrB — the monitor should handle this, NOT session A
	for sn := uint32(0); sn < 5; sn++ {
		conn.Inject(makeARQDataPacket(sn, []byte("B")), addrB)
	}

	// Monitor should create session B for this new address
	sessB := acceptWithTimeout(t, listener, 3*time.Second)

	// Session B should have received at least the first packet's data
	time.Sleep(500 * time.Millisecond)
	buf := make([]byte, 4096)
	sessB.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, err := sessB.Read(buf)
	if n == 0 {
		t.Fatalf("session B received no data (err=%v) — data was likely stolen by session A's backgroundLoop", err)
	}
	t.Logf("session B received %d bytes — no data stealing", n)

	sessB.Close()
}

// ========================== Diagnostic Tests ==========================
// These tests demonstrate underlying issues. They PASS with current code
// and serve as documentation of the behavior.

// TestReadBufferBlocksBackgroundLoop demonstrates that when nobody reads from
// ARQSession, readBuffer fills up and backgroundLoop blocks permanently.
func TestReadBufferBlocksBackgroundLoop(t *testing.T) {
	conn := newMockPacketConn()
	addr := &mockAddr{"client-1"}
	mtu := 100 // readBuffer = mtu * 2 = 200 bytes

	sess := NewARQSession(conn, addr, mtu, 0)
	defer sess.Close()

	// Inject 5 packets x 50 bytes = 250 bytes (exceeds 200-byte buffer)
	for sn := uint32(0); sn < 5; sn++ {
		conn.Inject(makeARQDataPacket(sn, make([]byte, 50)), addr)
	}
	time.Sleep(500 * time.Millisecond)

	// Canary: if backgroundLoop is blocked, this stays in the queue
	conn.Inject(makeARQDataPacket(5, []byte("canary")), addr)
	time.Sleep(500 * time.Millisecond)

	pending := conn.PendingCount()
	if pending == 0 {
		t.Error("canary was consumed — backgroundLoop is NOT blocked (unexpected)")
	} else {
		t.Logf("backgroundLoop is blocked: %d packet(s) stuck, readBuffer %d/%d",
			pending, sess.readBuffer.Size(), sess.readBuffer.Cap())
	}

	// ReadFrom call count should be frozen
	rc1 := atomic.LoadInt64(&conn.readCount)
	time.Sleep(200 * time.Millisecond)
	rc2 := atomic.LoadInt64(&conn.readCount)
	if rc2 > rc1 {
		t.Error("backgroundLoop still calling ReadFrom while supposedly blocked")
	}
}

// TestMonitorDrainsWhenBackgroundLoopBlocked confirms that the listener's
// monitor continues to drain the shared conn even when a session's
// backgroundLoop is blocked. This proves SimplexServer's buffer won't fill up.
func TestMonitorDrainsWhenBackgroundLoopBlocked(t *testing.T) {
	conn := newMockPacketConn()
	addr := &mockAddr{"client-1"}
	mtu := 100

	listener, err := ServeConn(conn, mtu, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	conn.Inject(makeARQDataPacket(0, make([]byte, 50)), addr)
	sess := acceptWithTimeout(t, listener, 2*time.Second)

	// Fill readBuffer to block backgroundLoop
	for sn := uint32(1); sn <= 5; sn++ {
		conn.Inject(makeARQDataPacket(sn, make([]byte, 50)), addr)
	}
	time.Sleep(500 * time.Millisecond)

	// Inject more data — monitor should drain it
	for sn := uint32(6); sn <= 10; sn++ {
		conn.Inject(makeARQDataPacket(sn, make([]byte, 50)), addr)
	}
	time.Sleep(500 * time.Millisecond)

	pending := conn.PendingCount()
	if pending == 0 {
		t.Log("CONFIRMED: monitor drains conn — SimplexServer buffer will NOT fill up")
	} else {
		t.Errorf("conn still has %d unread packets — monitor not draining", pending)
	}

	arqSession := sess.(*ARQSession)
	arqSession.arq.mu.Lock()
	queueLen := len(arqSession.arq.rcv_queue)
	arqSession.arq.mu.Unlock()
	if queueLen > 0 {
		t.Logf("arq.rcv_queue has %d bytes (memory leak from unread data)", queueLen)
	}

	sess.Close()
}
