package utils

import (
	"io"
	"sync"
	"testing"
	"time"
)

// TestBufferBasicReadWrite tests basic read/write operations
func TestBufferBasicReadWrite(t *testing.T) {
	buf := NewBuffer(100)
	defer buf.Close()

	// Write data
	data := []byte("hello world")
	n, err := buf.Write(data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Fatalf("Write returned %d, expected %d", n, len(data))
	}

	// Read data
	readBuf := make([]byte, 20)
	n, err = buf.Read(readBuf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != len(data) {
		t.Fatalf("Read returned %d, expected %d", n, len(data))
	}
	if string(readBuf[:n]) != string(data) {
		t.Fatalf("Read data mismatch: got %q, expected %q", readBuf[:n], data)
	}
}

// TestBufferReadEmpty tests reading from empty buffer returns EOF
func TestBufferReadEmpty(t *testing.T) {
	buf := NewBuffer(100)
	defer buf.Close()

	readBuf := make([]byte, 10)
	n, err := buf.Read(readBuf)
	if err != io.EOF {
		t.Fatalf("Read from empty buffer should return EOF, got: %v", err)
	}
	if n != 0 {
		t.Fatalf("Read from empty buffer should return 0 bytes, got: %d", n)
	}
}

// TestBufferWriteBlocking tests that Write blocks when buffer is full
func TestBufferWriteBlocking(t *testing.T) {
	buf := NewBuffer(10)
	defer buf.Close()

	// Fill buffer
	_, err := buf.Write([]byte("1234567890"))
	if err != nil {
		t.Fatalf("Initial write failed: %v", err)
	}

	// Try to write more - should block
	done := make(chan bool)
	go func() {
		_, err := buf.Write([]byte("extra"))
		if err != nil {
			t.Logf("Write after full returned error: %v", err)
		}
		done <- true
	}()

	// Wait a bit to ensure goroutine is blocked
	select {
	case <-done:
		t.Fatal("Write should have blocked but completed immediately")
	case <-time.After(100 * time.Millisecond):
		// Expected - write is blocked
	}

	// Read some data to make space
	readBuf := make([]byte, 5)
	buf.Read(readBuf)

	// Now the write should complete
	select {
	case <-done:
		// Expected
	case <-time.After(1 * time.Second):
		t.Fatal("Write should have completed after Read but timed out")
	}
}

// TestBufferReadAtLeastBlocking tests that ReadAtLeast blocks when buffer is empty
func TestBufferReadAtLeastBlocking(t *testing.T) {
	buf := NewBuffer(100)
	defer buf.Close()

	done := make(chan bool)
	readBuf := make([]byte, 10)

	go func() {
		n, err := buf.ReadAtLeast(readBuf)
		if err != nil {
			t.Logf("ReadAtLeast returned error: %v", err)
		}
		if n > 0 {
			done <- true
		}
	}()

	// Wait a bit to ensure goroutine is blocked
	select {
	case <-done:
		t.Fatal("ReadAtLeast should have blocked but completed immediately")
	case <-time.After(100 * time.Millisecond):
		// Expected - read is blocked
	}

	// Write some data
	buf.Write([]byte("hello"))

	// Now the read should complete
	select {
	case <-done:
		// Expected
	case <-time.After(1 * time.Second):
		t.Fatal("ReadAtLeast should have completed after Write but timed out")
	}
}

// TestBufferConcurrentReadWrite tests concurrent read/write operations
func TestBufferConcurrentReadWrite(t *testing.T) {
	buf := NewBuffer(1000)
	defer buf.Close()

	var wg sync.WaitGroup
	iterations := 100

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			data := []byte{byte(i)}
			_, err := buf.Write(data)
			if err != nil {
				t.Logf("Write error: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		readBuf := make([]byte, 1)
		for i := 0; i < iterations; i++ {
			for {
				n, err := buf.Read(readBuf)
				if err == io.EOF {
					time.Sleep(time.Millisecond)
					continue
				}
				if err != nil {
					t.Logf("Read error: %v", err)
					return
				}
				if n > 0 {
					break
				}
			}
		}
	}()

	wg.Wait()
}

// TestBufferClose tests that Close properly terminates blocked operations
func TestBufferClose(t *testing.T) {
	buf := NewBuffer(10)

	// Fill buffer
	buf.Write([]byte("1234567890"))

	// Start a blocked write
	done := make(chan error)
	go func() {
		_, err := buf.Write([]byte("extra"))
		done <- err
	}()

	// Wait a bit to ensure goroutine is blocked
	time.Sleep(100 * time.Millisecond)

	// Close buffer
	buf.Close()

	// Write should complete with error
	select {
	case err := <-done:
		if err != io.ErrClosedPipe {
			t.Fatalf("Expected ErrClosedPipe, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Write should have completed after Close but timed out")
	}

	// Further operations should fail
	_, err := buf.Write([]byte("test"))
	if err != io.ErrClosedPipe {
		t.Fatalf("Write after Close should return ErrClosedPipe, got: %v", err)
	}
}

// TestBufferSizeAndCap tests Size and Cap methods
func TestBufferSizeAndCap(t *testing.T) {
	maxLen := 100
	buf := NewBuffer(maxLen)
	defer buf.Close()

	if buf.Cap() != maxLen {
		t.Fatalf("Cap() returned %d, expected %d", buf.Cap(), maxLen)
	}

	if buf.Size() != 0 {
		t.Fatalf("Initial Size() should be 0, got %d", buf.Size())
	}

	data := []byte("hello")
	buf.Write(data)

	if buf.Size() != len(data) {
		t.Fatalf("Size() after write should be %d, got %d", len(data), buf.Size())
	}

	readBuf := make([]byte, 3)
	buf.Read(readBuf)

	if buf.Size() != len(data)-3 {
		t.Fatalf("Size() after partial read should be %d, got %d", len(data)-3, buf.Size())
	}
}

// TestChannelBufferBasic tests basic ChannelBuffer operations
func TestChannelBufferBasic(t *testing.T) {
	ch := NewChannel(5)
	defer ch.Close()

	data := []byte("test")
	if err := ch.Put(data); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	retrieved, err := ch.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(retrieved) != string(data) {
		t.Fatalf("Get returned %q, expected %q", retrieved, data)
	}
}

// TestChannelBufferEmpty tests Get on empty channel
func TestChannelBufferEmpty(t *testing.T) {
	ch := NewChannel(5)
	defer ch.Close()

	retrieved, err := ch.Get()
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if retrieved != nil {
		t.Fatalf("Get from empty channel should return nil, got: %v", retrieved)
	}
}

// TestChannelBufferFull tests Put on full channel
func TestChannelBufferFull(t *testing.T) {
	ch := NewChannel(2)
	defer ch.Close()

	ch.Put([]byte("1"))
	ch.Put([]byte("2"))

	err := ch.Put([]byte("3"))
	if err != ErrBufferFull {
		t.Fatalf("Put to full channel should return ErrBufferFull, got: %v", err)
	}
}

// TestChannelBufferLen tests Len method
func TestChannelBufferLen(t *testing.T) {
	ch := NewChannel(5)
	defer ch.Close()

	if ch.Len() != 0 {
		t.Fatalf("Initial Len should be 0, got: %d", ch.Len())
	}

	ch.Put([]byte("1"))
	if ch.Len() != 1 {
		t.Fatalf("Len after Put should be 1, got: %d", ch.Len())
	}

	ch.Put([]byte("2"))
	if ch.Len() != 2 {
		t.Fatalf("Len after second Put should be 2, got: %d", ch.Len())
	}

	ch.Get()
	if ch.Len() != 1 {
		t.Fatalf("Len after Get should be 1, got: %d", ch.Len())
	}
}

// TestChannelBufferZeroSize tests channel with size 0 defaults to 1
func TestChannelBufferZeroSize(t *testing.T) {
	ch := NewChannel(0)
	defer ch.Close()

	if err := ch.Put([]byte("test")); err != nil {
		t.Fatalf("Put to zero-size channel failed: %v", err)
	}

	retrieved, _ := ch.Get()
	if retrieved == nil {
		t.Fatal("Get from zero-size channel returned nil")
	}
}

// TestChannelBufferClosed tests behavior after Close
func TestChannelBufferClosed(t *testing.T) {
	ch := NewChannel(5)

	ch.Put([]byte("before-close"))
	ch.Close()

	// Put after close should fail
	if err := ch.Put([]byte("after-close")); err == nil {
		t.Fatal("Put after Close should return error")
	}

	// Get should drain remaining data
	data, err := ch.Get()
	if err != nil {
		t.Fatalf("Get after Close should drain remaining: %v", err)
	}
	if string(data) != "before-close" {
		t.Fatalf("Got %q, expected %q", data, "before-close")
	}

	// Get on empty+closed should return ErrClosedPipe
	_, err = ch.Get()
	if err == nil {
		t.Fatal("Get on empty closed channel should return error")
	}
}

// TestChannelBufferDoubleClose tests that double Close is safe
func TestChannelBufferDoubleClose(t *testing.T) {
	ch := NewChannel(5)
	ch.Close()
	ch.Close() // should not panic
}

// TestChannelBufferEmptyPut tests that Put with empty data is no-op
func TestChannelBufferEmptyPut(t *testing.T) {
	ch := NewChannel(5)
	defer ch.Close()

	if err := ch.Put([]byte{}); err != nil {
		t.Fatalf("Put empty should succeed: %v", err)
	}
	if ch.Len() != 0 {
		t.Fatalf("Empty Put should not enqueue, got Len=%d", ch.Len())
	}
}

// TestChannelBufferPacketBoundary verifies packet boundaries are preserved
func TestChannelBufferPacketBoundary(t *testing.T) {
	ch := NewChannel(10)
	defer ch.Close()

	// Put packets of different sizes
	packets := [][]byte{
		make([]byte, 100),
		make([]byte, 65000),
		make([]byte, 1),
		make([]byte, 32768),
	}
	for i, pkt := range packets {
		for j := range pkt {
			pkt[j] = byte(i)
		}
		if err := ch.Put(pkt); err != nil {
			t.Fatalf("Put packet %d failed: %v", i, err)
		}
	}

	// Get and verify each packet is complete and in order
	for i, expected := range packets {
		got, err := ch.Get()
		if err != nil {
			t.Fatalf("Get packet %d failed: %v", i, err)
		}
		if len(got) != len(expected) {
			t.Fatalf("Packet %d: got len=%d, expected len=%d", i, len(got), len(expected))
		}
		if got[0] != expected[0] {
			t.Fatalf("Packet %d: got data[0]=%d, expected %d", i, got[0], expected[0])
		}
	}
}
