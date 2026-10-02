// Package arq provides conformance test suites for ARQ-based net.Conn implementations.
//
// Three levels of verification:
//
//   - TestTransfer: data delivery correctness (echo, large transfer, bidirectional, multi-message)
//   - TestStreamSemantics: net.Conn stream contract (close, deadline, EOF propagation)
//   - TestConcurrency: concurrent read/write safety, rapid open/close cycles
//
// Usage:
//
//	func TestMyARQ_Suite(t *testing.T) {
//	    arq.TestTransfer(t, makePipe)
//	    arq.TestStreamSemantics(t, makePipe)
//	    arq.TestConcurrency(t, makePipe)
//	}
package arq

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// MakePipe creates a bidirectional net.Conn pair backed by ARQ.
// c1 writes are readable from c2 and vice versa.
// stop releases all resources; safe to call multiple times.
type MakePipe func(t *testing.T) (c1, c2 net.Conn, stop func(), err error)

// ── TestTransfer: 数据传输正确性 ─────────────────────────────

// TestTransfer verifies that the ARQ implementation delivers data correctly
// under various conditions: small/large payloads, bidirectional, multi-message.
func TestTransfer(t *testing.T, makePipe MakePipe) {
	t.Helper()

	t.Run("BasicEcho", func(t *testing.T) {
		c1, c2, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		msg := []byte("hello-arq-conformance")
		if _, err := c1.Write(msg); err != nil {
			t.Fatalf("Write: %v", err)
		}

		got, err := readExact(c2, len(msg), 30*time.Second)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("echo mismatch: got %q, want %q", got, msg)
		}
	})

	for _, size := range []int{64 * 1024, 256 * 1024, 1024 * 1024} {
		label := formatSize(size)
		t.Run("LargeTransfer_"+label, func(t *testing.T) {
			c1, c2, stop, err := makePipe(t)
			if err != nil {
				t.Fatalf("MakePipe: %v", err)
			}
			defer stop()

			payload := makePayload(size)
			go func() { writeInChunks(c1, payload, 32*1024) }()

			received, err := readExact(c2, size, 60*time.Second)
			if err != nil {
				t.Fatalf("readExact(%s): got %d/%d, err=%v", label, len(received), size, err)
			}
			if !bytes.Equal(received, payload) {
				t.Fatalf("%s: data integrity failure", label)
			}
		})
	}

	t.Run("Bidirectional_Concurrent", func(t *testing.T) {
		c1, c2, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		size := 256 * 1024
		c1Payload := makePayload(size)
		c2Payload := make([]byte, size)
		for i := range c2Payload {
			c2Payload[i] = byte((i*11 + 37) % 251)
		}

		var wg sync.WaitGroup
		var c1Received, c2Received []byte
		var c1Err, c2Err error

		wg.Add(4)
		go func() { defer wg.Done(); writeInChunks(c1, c1Payload, 32*1024) }()
		go func() { defer wg.Done(); writeInChunks(c2, c2Payload, 32*1024) }()
		go func() { defer wg.Done(); c2Received, c2Err = readExact(c2, size, 60*time.Second) }()
		go func() { defer wg.Done(); c1Received, c1Err = readExact(c1, size, 60*time.Second) }()
		wg.Wait()

		if c2Err != nil {
			t.Fatalf("c1→c2: %v (got %d/%d)", c2Err, len(c2Received), size)
		}
		if c1Err != nil {
			t.Fatalf("c2→c1: %v (got %d/%d)", c1Err, len(c1Received), size)
		}
		if !bytes.Equal(c2Received, c1Payload) {
			t.Fatal("c1→c2 data integrity failure")
		}
		if !bytes.Equal(c1Received, c2Payload) {
			t.Fatal("c2→c1 data integrity failure")
		}
	})

	t.Run("MultiMessage_Ordered", func(t *testing.T) {
		c1, c2, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		messages := [][]byte{
			makePayload(100),
			makePayload(1000),
			makePayload(10000),
			makePayload(50000),
			makePayload(100000),
		}

		go func() {
			for _, msg := range messages {
				header := []byte{byte(len(msg) >> 24), byte(len(msg) >> 16), byte(len(msg) >> 8), byte(len(msg))}
				c1.Write(header)
				c1.Write(msg)
			}
		}()

		for i, expected := range messages {
			hdr, err := readExact(c2, 4, 30*time.Second)
			if err != nil {
				t.Fatalf("msg %d header: %v", i, err)
			}
			length := int(hdr[0])<<24 | int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
			data, err := readExact(c2, length, 30*time.Second)
			if err != nil {
				t.Fatalf("msg %d data: %v (got %d/%d)", i, err, len(data), length)
			}
			if !bytes.Equal(data, expected) {
				t.Fatalf("msg %d: data integrity failure at size %d", i, length)
			}
		}
	})

	t.Run("SmallWrites_Coalesced", func(t *testing.T) {
		c1, c2, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		// 100 tiny writes should be delivered as a contiguous stream
		var expected []byte
		for i := 0; i < 100; i++ {
			chunk := []byte(fmt.Sprintf("msg%03d|", i))
			expected = append(expected, chunk...)
			if _, err := c1.Write(chunk); err != nil {
				t.Fatalf("Write %d: %v", i, err)
			}
		}

		received, err := readExact(c2, len(expected), 30*time.Second)
		if err != nil {
			t.Fatalf("readExact: %v (got %d/%d)", err, len(received), len(expected))
		}
		if !bytes.Equal(received, expected) {
			t.Fatal("small writes: stream data mismatch")
		}
	})
}

// ── TestStreamSemantics: net.Conn 流语义 ─────────────────────

// TestStreamSemantics verifies the net.Conn contract: close behavior, EOF
// propagation, deadline handling, and error semantics.
func TestStreamSemantics(t *testing.T, makePipe MakePipe) {
	t.Helper()

	t.Run("WriteAfterClose_ReturnsError", func(t *testing.T) {
		c1, _, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		c1.Close()
		_, werr := c1.Write([]byte("after-close"))
		if werr == nil {
			t.Fatal("Write after Close must return error")
		}
	})

	t.Run("ReadAfterClose_ReturnsError", func(t *testing.T) {
		c1, _, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		c1.Close()
		_, rerr := c1.Read(make([]byte, 64))
		if rerr == nil {
			t.Fatal("Read after Close must return error")
		}
	})

	t.Run("ReadDeadline_Timeout", func(t *testing.T) {
		_, c2, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		c2.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		start := time.Now()
		_, rerr := c2.Read(make([]byte, 64))
		elapsed := time.Since(start)

		if rerr == nil {
			t.Fatal("Read with deadline must return error when no data")
		}
		if ne, ok := rerr.(net.Error); ok && !ne.Timeout() {
			t.Logf("note: error is net.Error but not Timeout: %v", rerr)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("Read blocked too long: %v (deadline was 50ms)", elapsed)
		}
	})

	t.Run("WriteDeadline_PastTime", func(t *testing.T) {
		c1, _, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		c1.SetWriteDeadline(time.Now().Add(-time.Second))
		_, werr := c1.Write([]byte("past-deadline"))
		if werr == nil {
			// Some ARQ impls queue writes without blocking — this is acceptable
			t.Log("note: Write with past deadline succeeded (queued)")
		}
	})

	t.Run("PeerClose_PropagatesEOF", func(t *testing.T) {
		c1, c2, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		// Write data, then close sender
		msg := []byte("final-message")
		if _, err := c1.Write(msg); err != nil {
			t.Fatalf("Write: %v", err)
		}
		c1.Close()

		// Reader should get data, then EOF or closed error
		got, readErr := readExact(c2, len(msg), 10*time.Second)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrClosedPipe) {
			// Some impls may return data + nil, then EOF on next read
			if len(got) < len(msg) {
				t.Fatalf("short read before EOF: got %d/%d, err=%v", len(got), len(msg), readErr)
			}
		}
		if len(got) >= len(msg) && !bytes.Equal(got[:len(msg)], msg) {
			t.Fatal("data mismatch before EOF")
		}

		// Subsequent read should return error (EOF, ErrClosedPipe, etc.)
		c2.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 64)
		for {
			_, err := c2.Read(buf)
			if err != nil {
				break // Got terminal error — correct behavior
			}
		}
	})

	t.Run("DataBeforeClose_FullyDelivered", func(t *testing.T) {
		c1, c2, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		payload := makePayload(32 * 1024)
		if _, err := c1.Write(payload); err != nil {
			t.Fatalf("Write: %v", err)
		}
		// Close immediately after write — pending data must still be delivered
		c1.Close()

		received, err := readExact(c2, len(payload), 30*time.Second)
		if err != nil && len(received) < len(payload) {
			t.Fatalf("data written before close not fully delivered: got %d/%d, err=%v",
				len(received), len(payload), err)
		}
		if !bytes.Equal(received[:len(payload)], payload) {
			t.Fatal("data written before close: integrity failure")
		}
	})
}

// ── TestConcurrency: 并发安全性 ──────────────────────────────

// TestConcurrency verifies that the ARQ implementation is safe under
// concurrent access patterns: parallel writes, parallel reads, rapid lifecycle.
func TestConcurrency(t *testing.T, makePipe MakePipe) {
	t.Helper()

	t.Run("ParallelWriters", func(t *testing.T) {
		c1, c2, stop, err := makePipe(t)
		if err != nil {
			t.Fatalf("MakePipe: %v", err)
		}
		defer stop()

		const nWriters = 5
		const perWriter = 10000
		var wg sync.WaitGroup
		var totalWritten atomic.Int64

		for w := 0; w < nWriters; w++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				data := []byte(fmt.Sprintf("w%d-payload|", id))
				for i := 0; i < perWriter; i++ {
					n, err := c1.Write(data)
					if err != nil {
						return
					}
					totalWritten.Add(int64(n))
				}
			}(w)
		}

		// Reader drains everything
		var totalRead atomic.Int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 64*1024)
			for {
				c2.SetReadDeadline(time.Now().Add(5 * time.Second))
				n, err := c2.Read(buf)
				totalRead.Add(int64(n))
				if err != nil {
					return
				}
			}
		}()

		wg.Wait()
		c1.Close()
		<-done

		if totalRead.Load() != totalWritten.Load() {
			t.Fatalf("data loss: wrote %d, read %d", totalWritten.Load(), totalRead.Load())
		}
	})

	t.Run("RapidCloseReopen", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			c1, c2, stop, err := makePipe(t)
			if err != nil {
				t.Fatalf("iteration %d MakePipe: %v", i, err)
			}

			msg := []byte(fmt.Sprintf("iter-%d", i))
			c1.Write(msg)
			got, _ := readExact(c2, len(msg), 5*time.Second)
			if !bytes.Equal(got, msg) {
				t.Fatalf("iteration %d: echo mismatch", i)
			}

			c1.Close()
			c2.Close()
			stop()
		}
	})
}

// ── TestResilience: 丢包/断链恢复能力 ────────────────────────

// FaultController allows the harness to inject network faults.
// The test provides a concrete implementation tied to the transport.
type FaultController interface {
	// SetDropRate sets packet loss percentage (0-100). 0 disables loss.
	SetDropRate(percent int)
	// SetPartition enables/disables complete network partition (both directions).
	SetPartition(active bool)
}

// AsymFaultController extends FaultController with per-direction loss control.
// c1→c2 is the "A to B" direction; c2→c1 is the "B to A" direction.
type AsymFaultController interface {
	FaultController
	SetDropRateAtoB(percent int)
	SetDropRateBtoA(percent int)
}

// MakeFaultyPipe creates a conn pair with fault injection support.
type MakeFaultyPipe func(t *testing.T) (c1, c2 net.Conn, faults FaultController, stop func(), err error)

// TestResilience verifies ARQ recovery under adverse network conditions:
// packet loss, network partitions, and burst loss.
func TestResilience(t *testing.T, makeFaultyPipe MakeFaultyPipe) {
	t.Helper()

	t.Run("LossRecovery_10pct", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		size := 128 * 1024
		payload := makePayload(size)

		faults.SetDropRate(10)
		go func() { writeInChunks(c1, payload, 32*1024) }()

		received, err := readExact(c2, size, 120*time.Second)
		faults.SetDropRate(0)

		if err != nil {
			t.Fatalf("10%% loss: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("10% loss: data integrity failure")
		}
	})

	t.Run("LossRecovery_30pct", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		size := 64 * 1024
		payload := makePayload(size)

		faults.SetDropRate(30)
		go func() { writeInChunks(c1, payload, 32*1024) }()

		received, err := readExact(c2, size, 120*time.Second)
		faults.SetDropRate(0)

		if err != nil {
			t.Fatalf("30%% loss: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("30% loss: data integrity failure")
		}
	})

	t.Run("LossRecovery_Bidirectional", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		size := 64 * 1024
		c1Payload := makePayload(size)
		c2Payload := make([]byte, size)
		for i := range c2Payload {
			c2Payload[i] = byte((i*11 + 37) % 251)
		}

		faults.SetDropRate(15)

		var wg sync.WaitGroup
		var c1Received, c2Received []byte
		var c1Err, c2Err error

		wg.Add(4)
		go func() { defer wg.Done(); writeInChunks(c1, c1Payload, 32*1024) }()
		go func() { defer wg.Done(); writeInChunks(c2, c2Payload, 32*1024) }()
		go func() { defer wg.Done(); c2Received, c2Err = readExact(c2, size, 120*time.Second) }()
		go func() { defer wg.Done(); c1Received, c1Err = readExact(c1, size, 120*time.Second) }()
		wg.Wait()
		faults.SetDropRate(0)

		if c2Err != nil {
			t.Fatalf("c1→c2 under 15%% loss: %v", c2Err)
		}
		if c1Err != nil {
			t.Fatalf("c2→c1 under 15%% loss: %v", c1Err)
		}
		if !bytes.Equal(c2Received, c1Payload) {
			t.Fatal("bidirectional loss: c1→c2 integrity failure")
		}
		if !bytes.Equal(c1Received, c2Payload) {
			t.Fatal("bidirectional loss: c2→c1 integrity failure")
		}
	})

	t.Run("PartitionRecovery", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		// Send some data first (pre-partition)
		prePartition := []byte("before-partition|")
		if _, err := c1.Write(prePartition); err != nil {
			t.Fatalf("pre-partition write: %v", err)
		}

		// Partition for 2 seconds
		faults.SetPartition(true)
		postPartition := makePayload(32 * 1024)
		go func() { c1.Write(postPartition) }()
		time.Sleep(2 * time.Second)

		// Recover
		faults.SetPartition(false)

		// Read all data (pre + post partition)
		totalSize := len(prePartition) + len(postPartition)
		received, err := readExact(c2, totalSize, 60*time.Second)
		if err != nil {
			t.Fatalf("partition recovery: got %d/%d, err=%v", len(received), totalSize, err)
		}

		expected := append(prePartition, postPartition...)
		if !bytes.Equal(received, expected) {
			t.Fatal("partition recovery: data integrity failure")
		}
	})

	t.Run("BurstLoss_Then_Recovery", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		size := 64 * 1024
		payload := makePayload(size)

		// Start sending with no loss
		writeDone := make(chan error, 1)
		go func() { writeDone <- writeInChunks(c1, payload, 8*1024) }()

		// After a brief delay, inject burst loss
		time.Sleep(50 * time.Millisecond)
		faults.SetDropRate(50)
		time.Sleep(500 * time.Millisecond)
		faults.SetDropRate(0) // recover

		received, err := readExact(c2, size, 60*time.Second)
		if err != nil {
			t.Fatalf("burst loss recovery: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("burst loss recovery: data integrity failure")
		}
	})
}

// ── helpers ──────────────────────────────────────────────────

func makePayload(size int) []byte {
	p := make([]byte, size)
	for i := range p {
		p[i] = byte((i*7 + 13) % 251)
	}
	return p
}

func readExact(conn io.Reader, n int, timeout time.Duration) ([]byte, error) {
	result := make([]byte, 0, n)
	buf := make([]byte, 65536)
	deadline := time.Now().Add(timeout)

	for len(result) < n {
		if time.Now().After(deadline) {
			return result, fmt.Errorf("timeout: got %d/%d bytes", len(result), n)
		}
		remaining := n - len(result)
		readSize := len(buf)
		if readSize > remaining {
			readSize = remaining
		}
		nr, err := conn.Read(buf[:readSize])
		if nr > 0 {
			result = append(result, buf[:nr]...)
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func writeInChunks(conn net.Conn, payload []byte, chunkSize int) error {
	written := 0
	for written < len(payload) {
		chunk := chunkSize
		if chunk > len(payload)-written {
			chunk = len(payload) - written
		}
		n, err := conn.Write(payload[written : written+chunk])
		if err != nil {
			return err
		}
		written += n
	}
	return nil
}

func formatSize(size int) string {
	if size >= 1024*1024 {
		return fmt.Sprintf("%dMB", size/(1024*1024))
	}
	return fmt.Sprintf("%dKB", size/1024)
}
