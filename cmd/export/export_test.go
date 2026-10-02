package main

import (
	"net"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/core"
)

func storeRawConn(handle int, conn net.Conn) {
	// Store as BufferedConn to match what MemoryDial does.
	conns.Store(handle, NewBufferedConn(conn))
}

func TestMemoryReadEOFReturnsZeroNoError(t *testing.T) {
	const handle = 710001

	server, client := net.Pipe()
	storeRawConn(handle, server)
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
		conns.Delete(handle)
	})

	if err := client.Close(); err != nil {
		t.Fatalf("close peer: %v", err)
	}

	// Wait for the read goroutine to see the close.
	time.Sleep(50 * time.Millisecond)

	buf := make([]byte, 16)
	bc, _ := getBufferedConn(handle)
	n, errCode := bc.TryRead(buf)
	if errCode != 0 {
		t.Fatalf("TryRead EOF error code = %d, want 0", errCode)
	}
	if n != 0 {
		t.Fatalf("TryRead EOF bytes = %d, want 0", n)
	}
}

func TestMemoryReadDataThenEOFDoesNotReportError(t *testing.T) {
	const handle = 710002
	want := []byte("abc")

	server, client := net.Pipe()
	storeRawConn(handle, server)
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
		conns.Delete(handle)
	})

	go func() {
		_, _ = client.Write(want)
		_ = client.Close()
	}()

	// Wait for goroutine to buffer the data.
	time.Sleep(50 * time.Millisecond)

	buf := make([]byte, 16)
	bc, _ := getBufferedConn(handle)

	n, errCode := bc.TryRead(buf)
	if errCode != 0 {
		t.Fatalf("first TryRead error code = %d, want 0", errCode)
	}
	if got := string(buf[:int(n)]); got != string(want) {
		t.Fatalf("first TryRead bytes = %q, want %q", got, want)
	}

	// Second read: EOF (buffer drained, conn closed).
	n, errCode = bc.TryRead(buf)
	if errCode != 0 {
		t.Fatalf("second TryRead EOF error code = %d, want 0", errCode)
	}
	if n != 0 {
		t.Fatalf("second TryRead EOF bytes = %d, want 0", n)
	}
}

func TestMemoryCloseRemovesConnectionHandle(t *testing.T) {
	const handle = 710003

	server, client := net.Pipe()
	storeRawConn(handle, server)
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
		conns.Delete(handle)
	})

	if got := memoryCloseGo(handle); got != 0 {
		t.Fatalf("MemoryClose error code = %d, want 0", got)
	}
	if _, ok := conns.Load(handle); ok {
		t.Fatalf("connection handle %d still present after MemoryClose", handle)
	}
}

// ========================================================================
// Non-blocking TryRead tests
// ========================================================================

func TestTryReadReturnsWouldBlockOnEmptyPipe(t *testing.T) {
	const handle = 710010

	server, client := net.Pipe()
	storeRawConn(handle, server)
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
		conns.Delete(handle)
	})

	// No data written — TryRead should return WouldBlock instantly.
	buf := make([]byte, 16)
	bc, _ := getBufferedConn(handle)
	start := time.Now()
	n, errCode := bc.TryRead(buf)
	elapsed := time.Since(start)

	if errCode != core.ErrWouldBlock {
		t.Fatalf("expected ErrWouldBlock (%d), got errCode=%d, n=%d", core.ErrWouldBlock, errCode, n)
	}
	if elapsed > 10*time.Millisecond {
		t.Fatalf("TryRead should return instantly, took %v", elapsed)
	}
}

func TestTryReadReturnsDataWhenBuffered(t *testing.T) {
	const handle = 710011
	want := []byte("hello")

	server, client := net.Pipe()
	storeRawConn(handle, server)
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
		conns.Delete(handle)
	})

	go func() {
		_, _ = client.Write(want)
	}()

	// Wait for the goroutine to buffer the data.
	time.Sleep(50 * time.Millisecond)

	buf := make([]byte, 16)
	bc, _ := getBufferedConn(handle)
	n, errCode := bc.TryRead(buf)
	if errCode != 0 {
		t.Fatalf("expected success, got errCode=%d", errCode)
	}
	if got := string(buf[:n]); got != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTryReadReturnsEOFAfterDrain(t *testing.T) {
	const handle = 710012

	server, client := net.Pipe()
	storeRawConn(handle, server)
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
		conns.Delete(handle)
	})

	_ = client.Close()
	time.Sleep(50 * time.Millisecond)

	buf := make([]byte, 16)
	bc, _ := getBufferedConn(handle)
	n, errCode := bc.TryRead(buf)
	if errCode != 0 {
		t.Fatalf("EOF should return errCode=0, got %d", errCode)
	}
	if n != 0 {
		t.Fatalf("EOF should return n=0, got %d", n)
	}
}

func TestTryReadNoDeadlineSideEffects(t *testing.T) {
	// Verify that TryRead does NOT call SetReadDeadline.
	// After multiple TryRead calls, a subsequent blocking Read should
	// still work (no stale deadline).
	const handle = 710013
	want := []byte("after-try")

	server, client := net.Pipe()
	storeRawConn(handle, server)
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
		conns.Delete(handle)
	})

	bc, _ := getBufferedConn(handle)
	buf := make([]byte, 16)

	// Do several TryRead calls (all WouldBlock).
	for i := 0; i < 10; i++ {
		_, errCode := bc.TryRead(buf)
		if errCode != core.ErrWouldBlock {
			t.Fatalf("TryRead %d: expected WouldBlock, got %d", i, errCode)
		}
	}

	// Now write data and do a blocking read — should succeed.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = client.Write(want)
	}()

	n, errCode := bc.BlockingRead(buf)
	if errCode != 0 {
		t.Fatalf("BlockingRead after TryRead: errCode=%d", errCode)
	}
	if got := string(buf[:n]); got != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteSucceeds(t *testing.T) {
	const handle = 710014
	want := []byte("world")

	server, client := net.Pipe()
	storeRawConn(handle, server)
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
		conns.Delete(handle)
	})

	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := client.Read(buf)
		done <- buf[:n]
	}()

	bc, _ := getBufferedConn(handle)
	n, errCode := bc.Write(want)
	if errCode != 0 {
		t.Fatalf("write should succeed, got errCode=%d", errCode)
	}
	if n != len(want) {
		t.Fatalf("wrote %d bytes, want %d", n, len(want))
	}

	got := <-done
	if string(got) != string(want) {
		t.Fatalf("reader got %q, want %q", got, want)
	}
}
