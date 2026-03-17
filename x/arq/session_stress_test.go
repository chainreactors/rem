package arq

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSessionAbruptClose tests session behavior when closed abruptly
func TestSessionAbruptClose(t *testing.T) {
	conn := newMockPacketConn()
	addr := &mockAddr{"client-1"}

	sess := NewARQSession(conn, addr, 1400, 0)

	// Start writing
	go func() {
		for i := 0; i < 100; i++ {
			sess.Write([]byte("test"))
			time.Sleep(10 * time.Millisecond)
		}
	}()

	// Close abruptly after a short time
	time.Sleep(100 * time.Millisecond)
	sess.Close()

	// Further operations should fail
	_, err := sess.Write([]byte("after close"))
	if err != io.ErrClosedPipe {
		t.Errorf("Expected ErrClosedPipe, got: %v", err)
	}

	_, err = sess.Read(make([]byte, 100))
	if err != io.EOF {
		t.Errorf("Expected EOF, got: %v", err)
	}
}

// TestSessionConcurrentReadWrite tests concurrent read and write on session
func TestSessionConcurrentReadWrite(t *testing.T) {
	conn := newMockPacketConn()
	addr := &mockAddr{"client-1"}

	sess := NewARQSession(conn, addr, 1400, 0)
	defer sess.Close()

	var wg sync.WaitGroup
	duration := 2 * time.Second
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	var writeCount, readCount int32

	// Continuous writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				n, err := sess.Write([]byte("test data"))
				if err == nil && n > 0 {
					atomic.AddInt32(&writeCount, 1)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Inject some packets for reading
	wg.Add(1)
	go func() {
		defer wg.Done()
		counter := uint32(0)
		for {
			select {
			case <-stopCh:
				return
			default:
				pkt := makeARQDataPacket(counter, []byte("incoming"))
				conn.Inject(pkt, addr)
				counter++
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()

	// Continuous reader – set a short read deadline so Read() returns
	// periodically, allowing the goroutine to notice stopCh closing.
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 100)
		for {
			select {
			case <-stopCh:
				return
			default:
				sess.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				n, _ := sess.Read(buf)
				if n > 0 {
					atomic.AddInt32(&readCount, 1)
				}
			}
		}
	}()

	wg.Wait()

	t.Logf("Writes: %d, Reads: %d", writeCount, readCount)

	if writeCount == 0 {
		t.Error("No writes completed")
	}
}

// TestSessionReadTimeout tests read timeout behavior
func TestSessionReadTimeout(t *testing.T) {
	conn := newMockPacketConn()
	addr := &mockAddr{"client-1"}

	sess := NewARQSession(conn, addr, 1400, 100*time.Millisecond)
	defer sess.Close()

	// Set read deadline
	sess.SetReadDeadline(time.Now().Add(200 * time.Millisecond))

	// Try to read (should timeout)
	buf := make([]byte, 100)
	start := time.Now()
	_, err := sess.Read(buf)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Expected timeout error")
	}

	if elapsed < 100*time.Millisecond {
		t.Errorf("Timeout too fast: %v", elapsed)
	}

	t.Logf("Read timed out after %v", elapsed)
}

// TestSessionWriteTimeout tests write timeout behavior
func TestSessionWriteTimeout(t *testing.T) {
	conn := newMockPacketConn()
	addr := &mockAddr{"client-1"}

	sess := NewARQSession(conn, addr, 1400, 0)
	defer sess.Close()

	// Set write deadline in the past
	sess.SetWriteDeadline(time.Now().Add(-1 * time.Second))

	// Try to write (should timeout immediately)
	_, err := sess.Write([]byte("test"))
	if err == nil {
		t.Error("Expected timeout error")
	}

	t.Logf("Write timeout error: %v", err)
}

// TestListenerMultipleSessionsStress tests listener with many concurrent sessions
func TestListenerMultipleSessionsStress(t *testing.T) {
	conn := newMockPacketConn()
	listener, err := ServeConn(conn, 1400, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	numSessions := 20
	var wg sync.WaitGroup

	// Create multiple sessions
	for i := 0; i < numSessions; i++ {
		addr := &mockAddr{string(rune('A' + i))}

		// Inject initial packet to create session
		conn.Inject(makeARQDataPacket(0, []byte{byte(i)}), addr)
	}

	// Accept all sessions
	sessions := make([]net.Conn, 0, numSessions)
	for i := 0; i < numSessions; i++ {
		sess := acceptWithTimeout(t, listener, 2*time.Second)
		if sess == nil {
			t.Fatalf("Failed to accept session %d", i)
		}
		// Set a read deadline so that reads don't block indefinitely when the
		// session's receive buffer is empty (only one packet was injected per session).
		sess.SetReadDeadline(time.Now().Add(3 * time.Second))
		sessions = append(sessions, sess)
	}

	// Interact with all sessions concurrently
	for i, sess := range sessions {
		wg.Add(1)
		go func(id int, s net.Conn) {
			defer wg.Done()

			// Write some data
			for j := 0; j < 10; j++ {
				s.Write([]byte{byte(id), byte(j)})
				time.Sleep(10 * time.Millisecond)
			}

			// Read some data (may return timeout error once buffer is empty)
			buf := make([]byte, 100)
			for j := 0; j < 5; j++ {
				s.Read(buf)
				time.Sleep(20 * time.Millisecond)
			}
		}(i, sess)
	}

	wg.Wait()

	// Close all sessions
	for _, sess := range sessions {
		sess.Close()
	}

	t.Logf("Successfully handled %d concurrent sessions", numSessions)
}

// TestListenerSessionCloseRace tests race between session close and new packets
func TestListenerSessionCloseRace(t *testing.T) {
	conn := newMockPacketConn()
	listener, err := ServeConn(conn, 1400, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	addr := &mockAddr{"client-1"}

	// Create session
	conn.Inject(makeARQDataPacket(0, []byte("init")), addr)
	sess := acceptWithTimeout(t, listener, 2*time.Second)

	var wg sync.WaitGroup

	// stopInject signals the injector to stop before we close the session so
	// that no further packets from this address re-create a new session after
	// the close, which would make the session-removal assertion fail.
	stopInject := make(chan struct{})

	// Inject packets continuously
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i < 100; i++ {
			select {
			case <-stopInject:
				return
			default:
			}
			conn.Inject(makeARQDataPacket(uint32(i), []byte{byte(i)}), addr)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Close session after a short time, then stop the injector so no new
	// packets re-create the session under the same address.
	time.Sleep(100 * time.Millisecond)
	sess.Close()
	close(stopInject)

	wg.Wait()

	// Verify session is removed from listener
	time.Sleep(200 * time.Millisecond)
	listener.sessionLock.RLock()
	_, exists := listener.sessions[addr.String()]
	listener.sessionLock.RUnlock()

	if exists {
		t.Error("Session still exists in listener after close")
	}
}

// TestListenerAcceptBacklog tests listener accept queue overflow
func TestListenerAcceptBacklog(t *testing.T) {
	conn := newMockPacketConn()
	listener, err := ServeConn(conn, 1400, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Create more sessions than accept backlog (128)
	numSessions := 150
	for i := 0; i < numSessions; i++ {
		addr := &mockAddr{string(rune(i))}
		conn.Inject(makeARQDataPacket(0, []byte{byte(i)}), addr)
		time.Sleep(time.Millisecond)
	}

	// Wait for monitor to process
	time.Sleep(500 * time.Millisecond)

	// Accept as many as possible
	accepted := 0
	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-timeout:
			goto done
		default:
			listener.SetDeadline(time.Now().Add(100 * time.Millisecond))
			sess, err := listener.Accept()
			if err != nil {
				goto done
			}
			if sess != nil {
				accepted++
				sess.Close()
			}
		}
	}

done:
	t.Logf("Accepted %d/%d sessions (backlog: %d)", accepted, numSessions, acceptBacklog)

	if accepted == 0 {
		t.Error("No sessions accepted")
	}

	// Some sessions should have been dropped due to backlog
	if accepted >= numSessions {
		t.Log("All sessions accepted (backlog not exceeded)")
	}
}

// TestListenerCloseWithActiveSessions tests closing listener with active sessions
func TestListenerCloseWithActiveSessions(t *testing.T) {
	conn := newMockPacketConn()
	listener, err := ServeConn(conn, 1400, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Create multiple sessions
	for i := 0; i < 5; i++ {
		addr := &mockAddr{string(rune('A' + i))}
		conn.Inject(makeARQDataPacket(0, []byte{byte(i)}), addr)
	}

	// Accept sessions
	sessions := make([]net.Conn, 0)
	for i := 0; i < 5; i++ {
		sess := acceptWithTimeout(t, listener, 1*time.Second)
		if sess != nil {
			sessions = append(sessions, sess)
		}
	}

	// Close listener
	listener.Close()

	// Verify all sessions are closed
	for i, sess := range sessions {
		_, err := sess.Write([]byte("test"))
		if err == nil {
			t.Errorf("Session %d still writable after listener close", i)
		}
	}

	// Verify Accept fails
	_, err = listener.Accept()
	if err == nil {
		t.Error("Accept should fail after listener close")
	}
}

// TestSessionBufferOverflow tests session behavior when read buffer overflows
func TestSessionBufferOverflow(t *testing.T) {
	conn := newMockPacketConn()
	addr := &mockAddr{"client-1"}
	mtu := 100

	sess := NewARQSession(conn, addr, mtu, 0)
	defer sess.Close()

	// Inject many packets to overflow buffer (mtu*2 = 200 bytes)
	for i := 0; i < 10; i++ {
		pkt := makeARQDataPacket(uint32(i), make([]byte, 50))
		conn.Inject(pkt, addr)
	}

	// Wait for backgroundLoop to process
	time.Sleep(500 * time.Millisecond)

	// Buffer should be full or near full
	bufSize := sess.readBuffer.Size()
	bufCap := sess.readBuffer.Cap()

	t.Logf("Buffer: %d/%d bytes", bufSize, bufCap)

	if bufSize == 0 {
		t.Error("No data in buffer despite injecting packets")
	}
}

// TestSessionRapidCloseOpen tests rapid session creation and destruction
func TestSessionRapidCloseOpen(t *testing.T) {
	conn := newMockPacketConn()
	addr := &mockAddr{"client-1"}

	for i := 0; i < 50; i++ {
		sess := NewARQSession(conn, addr, 1400, 0)
		sess.Write([]byte("test"))
		sess.Close()
	}

	t.Log("Rapid session creation/destruction completed")
}

// TestListenerConcurrentAccept tests concurrent Accept calls
func TestListenerConcurrentAccept(t *testing.T) {
	conn := newMockPacketConn()
	listener, err := ServeConn(conn, 1400, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Inject sessions
	for i := 0; i < 10; i++ {
		addr := &mockAddr{string(rune('A' + i))}
		conn.Inject(makeARQDataPacket(0, []byte{byte(i)}), addr)
	}

	var wg sync.WaitGroup
	var acceptCount int32

	// Multiple concurrent Accept calls
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				listener.SetDeadline(time.Now().Add(500 * time.Millisecond))
				sess, err := listener.Accept()
				if err == nil && sess != nil {
					atomic.AddInt32(&acceptCount, 1)
					sess.Close()
				}
			}
		}()
	}

	wg.Wait()

	t.Logf("Accepted %d sessions from concurrent Accept calls", acceptCount)

	if acceptCount == 0 {
		t.Error("No sessions accepted")
	}
}

// TestSessionDeadlineRace tests race between deadline and operations
func TestSessionDeadlineRace(t *testing.T) {
	conn := newMockPacketConn()
	addr := &mockAddr{"client-1"}

	sess := NewARQSession(conn, addr, 1400, 0)
	defer sess.Close()

	var wg sync.WaitGroup

	// Continuously set deadlines
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			sess.SetDeadline(time.Now().Add(100 * time.Millisecond))
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Continuously read
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 100)
		for i := 0; i < 100; i++ {
			sess.Read(buf)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Continuously write
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			sess.Write([]byte("test"))
			time.Sleep(5 * time.Millisecond)
		}
	}()

	wg.Wait()

	t.Log("Deadline race test completed without crash")
}

// TestListenerMonitorStress tests listener monitor under stress
func TestListenerMonitorStress(t *testing.T) {
	conn := newMockPacketConn()
	listener, err := ServeConn(conn, 1400, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var wg sync.WaitGroup
	duration := 2 * time.Second
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	// Inject packets rapidly from multiple addresses
	wg.Add(1)
	go func() {
		defer wg.Done()
		counter := 0
		for {
			select {
			case <-stopCh:
				return
			default:
				addr := &mockAddr{string(rune('A' + (counter % 10)))}
				pkt := makeARQDataPacket(uint32(counter/10), []byte{byte(counter)})
				conn.Inject(pkt, addr)
				counter++
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Accept sessions
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				listener.SetDeadline(time.Now().Add(100 * time.Millisecond))
				sess, err := listener.Accept()
				if err == nil && sess != nil {
					go func(s net.Conn) {
						time.Sleep(100 * time.Millisecond)
						s.Close()
					}(sess)
				}
			}
		}
	}()

	wg.Wait()

	t.Log("Monitor stress test completed")
}
