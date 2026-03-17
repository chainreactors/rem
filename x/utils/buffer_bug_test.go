package utils

import (
	"io"
	"testing"
	"time"
)

// TestBufferDeadlockBug tests a potential deadlock bug in Buffer
// BUG: When buffer is full and Write() is blocked, if Read() returns EOF
// (because buffer becomes empty between Lock and check), the blocked
// Write() will never be woken up.
func TestBufferDeadlockBug(t *testing.T) {
	buf := NewBuffer(10)
	defer buf.Close()

	// Fill buffer completely
	buf.Write([]byte("1234567890"))

	// Start a writer that will block
	writerBlocked := make(chan bool, 1)
	writerDone := make(chan bool, 1)
	go func() {
		writerBlocked <- true
		_, err := buf.Write([]byte("x"))
		if err != nil {
			t.Logf("Writer error: %v", err)
		}
		writerDone <- true
	}()

	// Wait for writer to block
	<-writerBlocked
	time.Sleep(100 * time.Millisecond)

	// Read all data
	readBuf := make([]byte, 20)
	n, _ := buf.Read(readBuf)
	t.Logf("Read %d bytes", n)

	// Try to read again (buffer is empty, will return EOF)
	n, err := buf.Read(readBuf)
	t.Logf("Second read: %d bytes, err=%v", n, err)

	// Writer should be unblocked now
	select {
	case <-writerDone:
		t.Log("SUCCESS: Writer unblocked after Read")
	case <-time.After(2 * time.Second):
		t.Fatal("BUG FOUND: Writer deadlocked! Read() returned EOF without signaling blocked Write()")
	}
}

// TestBufferDeadlockScenario2 tests another deadlock scenario
// When multiple writers are blocked and reader reads small amounts
func TestBufferDeadlockScenario2(t *testing.T) {
	buf := NewBuffer(10)
	defer buf.Close()

	// Fill buffer
	buf.Write([]byte("1234567890"))

	// Start multiple blocked writers
	numWriters := 3
	writersDone := make(chan bool, numWriters)

	for i := 0; i < numWriters; i++ {
		go func(id int) {
			_, err := buf.Write([]byte("x"))
			if err != nil {
				t.Logf("Writer %d error: %v", id, err)
			}
			writersDone <- true
		}(i)
	}

	// Wait for writers to block
	time.Sleep(200 * time.Millisecond)

	// Track how many completions we drain from the intermediate selects
	drained := 0

	// Read small amount
	readBuf := make([]byte, 2)
	buf.Read(readBuf)

	// Only one writer should be unblocked
	// But due to Signal() (not Broadcast()), only one will wake up
	select {
	case <-writersDone:
		drained++
		t.Log("One writer unblocked")
	case <-time.After(1 * time.Second):
		t.Fatal("No writers unblocked after Read")
	}

	// Read more
	buf.Read(readBuf)

	// Another writer should unblock
	select {
	case <-writersDone:
		drained++
		t.Log("Second writer unblocked")
	case <-time.After(1 * time.Second):
		t.Log("Warning: Second writer still blocked (expected with Signal)")
	}

	// Close to unblock remaining writers
	buf.Close()

	// Wait only for writers not yet accounted for
	remaining := numWriters - drained
	timeout := time.After(1 * time.Second)
	for i := 0; i < remaining; i++ {
		select {
		case <-writersDone:
			// OK
		case <-timeout:
			t.Fatalf("Not all writers completed after Close")
		}
	}
}

// TestBufferReadEOFRace tests race condition in Read() EOF check
func TestBufferReadEOFRace(t *testing.T) {
	const totalBytes = 100
	buf := NewBuffer(totalBytes)
	defer buf.Close()

	done := make(chan bool, 2)

	// Writer
	go func() {
		for i := 0; i < totalBytes; i++ {
			buf.Write([]byte{byte(i)})
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Reader — track bytes, not iterations, to avoid infinite retry on EOF
	go func() {
		readBuf := make([]byte, 10)
		totalRead := 0
		for totalRead < totalBytes {
			n, err := buf.Read(readBuf)
			if err == io.EOF && n == 0 {
				time.Sleep(time.Millisecond)
				continue
			}
			if err != nil && err != io.EOF {
				t.Logf("Read error: %v", err)
				break
			}
			totalRead += n
		}
		done <- true
	}()

	// Wait for both to complete
	<-done
	<-done

	t.Log("Read/Write race test completed")
}
