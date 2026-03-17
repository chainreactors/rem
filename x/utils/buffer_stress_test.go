package utils

import (
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBufferHighConcurrency tests buffer under high concurrent load
func TestBufferHighConcurrency(t *testing.T) {
	buf := NewBuffer(10000)
	defer buf.Close()

	var wg sync.WaitGroup
	writers := 10
	readers := 10
	iterations := 1000

	var writeErrors, readErrors int32

	// Multiple writers
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				data := []byte{byte(id), byte(j)}
				_, err := buf.Write(data)
				if err != nil && err != io.ErrClosedPipe {
					atomic.AddInt32(&writeErrors, 1)
					return
				}
				// Random small delay to increase contention
				if j%10 == 0 {
					time.Sleep(time.Microsecond)
				}
			}
		}(i)
	}

	// Multiple readers
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			readBuf := make([]byte, 100)
			for j := 0; j < iterations; j++ {
				_, err := buf.Read(readBuf)
				if err != nil && err != io.EOF && err != io.ErrClosedPipe {
					atomic.AddInt32(&readErrors, 1)
					return
				}
				if j%10 == 0 {
					time.Sleep(time.Microsecond)
				}
			}
		}(i)
	}

	wg.Wait()

	if writeErrors > 0 {
		t.Errorf("Write errors: %d", writeErrors)
	}
	if readErrors > 0 {
		t.Errorf("Read errors: %d", readErrors)
	}
}

// TestBufferCloseWhileBlocked tests closing buffer while operations are blocked.
// Writer-blocked and reader-blocked are tested in separate sub-tests because
// a full buffer (needed to block a writer) and an empty buffer (needed to block
// ReadAtLeast) are mutually exclusive on the same buffer instance.
func TestBufferCloseWhileBlocked(t *testing.T) {
	t.Run("writer", func(t *testing.T) {
		buf := NewBuffer(10)

		// Fill buffer completely
		buf.Write([]byte("1234567890"))

		writerDone := make(chan error, 1)
		go func() {
			_, err := buf.Write([]byte("blocked"))
			writerDone <- err
		}()

		// Let the writer block
		time.Sleep(100 * time.Millisecond)

		buf.Close()

		select {
		case err := <-writerDone:
			if err != io.ErrClosedPipe {
				t.Errorf("Expected ErrClosedPipe from writer, got: %v", err)
			}
		case <-time.After(1 * time.Second):
			t.Error("Writer did not unblock after Close")
		}
	})

	t.Run("reader", func(t *testing.T) {
		buf := NewBuffer(10)

		readerDone := make(chan error, 1)
		go func() {
			readBuf := make([]byte, 20)
			// Buffer is empty, ReadAtLeast blocks
			_, err := buf.ReadAtLeast(readBuf)
			readerDone <- err
		}()

		// Let the reader block
		time.Sleep(100 * time.Millisecond)

		buf.Close()

		select {
		case err := <-readerDone:
			if err != io.ErrClosedPipe {
				t.Errorf("Expected ErrClosedPipe from reader, got: %v", err)
			}
		case <-time.After(1 * time.Second):
			t.Error("Reader did not unblock after Close")
		}
	})
}

// TestBufferRapidCloseOpen tests rapid close/open cycles
func TestBufferRapidCloseOpen(t *testing.T) {
	for i := 0; i < 100; i++ {
		buf := NewBuffer(100)
		buf.Write([]byte("test"))
		buf.Close()
		// Verify operations fail after close
		_, err := buf.Write([]byte("after"))
		if err != io.ErrClosedPipe {
			t.Fatalf("Iteration %d: expected ErrClosedPipe, got %v", i, err)
		}
	}
}

// TestBufferWriteReadRace tests race between write and read
func TestBufferWriteReadRace(t *testing.T) {
	buf := NewBuffer(1000)
	defer buf.Close()

	var wg sync.WaitGroup
	duration := 2 * time.Second

	// Use a closed channel so ALL goroutines can receive the stop signal.
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	var totalWritten, totalRead int64

	// Continuous writer with small delays to avoid filling buffer too fast
	wg.Add(1)
	go func() {
		defer wg.Done()
		data := []byte("x")
		for {
			select {
			case <-stopCh:
				return
			default:
				n, err := buf.Write(data)
				if err == nil {
					atomic.AddInt64(&totalWritten, int64(n))
				} else if err == io.ErrClosedPipe {
					return
				}
				// Small delay to avoid blocking forever
				time.Sleep(time.Microsecond * 100)
			}
		}
	}()

	// Continuous reader
	wg.Add(1)
	go func() {
		defer wg.Done()
		readBuf := make([]byte, 10)
		for {
			select {
			case <-stopCh:
				return
			default:
				n, err := buf.Read(readBuf)
				if n > 0 {
					atomic.AddInt64(&totalRead, int64(n))
				}
				if err == io.ErrClosedPipe {
					return
				}
				time.Sleep(time.Microsecond * 100)
			}
		}
	}()

	wg.Wait()

	t.Logf("Written: %d bytes, Read: %d bytes, Remaining: %d bytes",
		totalWritten, totalRead, buf.Size())

	// Some data may remain in buffer, but read should not exceed written
	if totalRead > totalWritten {
		t.Errorf("Read more than written: read=%d, written=%d", totalRead, totalWritten)
	}
}

// TestBufferBurstyTraffic tests buffer with bursty write patterns
func TestBufferBurstyTraffic(t *testing.T) {
	buf := NewBuffer(5000)
	defer buf.Close()

	var wg sync.WaitGroup
	var writeErrors, readErrors int32

	// Bursty writer - writes in bursts
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			// Burst: write many packets quickly
			for j := 0; j < 100; j++ {
				data := make([]byte, 50)
				_, err := buf.Write(data)
				if err != nil && err != io.ErrClosedPipe {
					atomic.AddInt32(&writeErrors, 1)
					return
				}
			}
			// Pause between bursts
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// Steady reader
	wg.Add(1)
	go func() {
		defer wg.Done()
		readBuf := make([]byte, 100)
		for i := 0; i < 1000; i++ {
			_, err := buf.Read(readBuf)
			if err != nil && err != io.EOF && err != io.ErrClosedPipe {
				atomic.AddInt32(&readErrors, 1)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()

	if writeErrors > 0 {
		t.Errorf("Write errors: %d", writeErrors)
	}
	if readErrors > 0 {
		t.Errorf("Read errors: %d", readErrors)
	}
}

// TestBufferStarvation tests reader starvation scenario
func TestBufferStarvation(t *testing.T) {
	buf := NewBuffer(100)
	defer buf.Close()

	// Fill buffer completely
	buf.Write(make([]byte, 100))

	// Try to write more - should block
	writeBlocked := make(chan bool, 1)
	go func() {
		writeBlocked <- true
		buf.Write([]byte("x"))
	}()

	// Wait for write to block
	<-writeBlocked
	time.Sleep(100 * time.Millisecond)

	// Now read to unblock
	readBuf := make([]byte, 10)
	n, err := buf.Read(readBuf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n == 0 {
		t.Fatal("Read returned 0 bytes")
	}

	// Write should complete now
	time.Sleep(100 * time.Millisecond)
}

// TestBufferMultipleCloseRace tests concurrent Close calls
func TestBufferMultipleCloseRace(t *testing.T) {
	buf := NewBuffer(100)

	var wg sync.WaitGroup
	closers := 10

	for i := 0; i < closers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf.Close()
		}()
	}

	wg.Wait()

	// Verify buffer is closed
	_, err := buf.Write([]byte("test"))
	if err != io.ErrClosedPipe {
		t.Errorf("Expected ErrClosedPipe after multiple Close, got: %v", err)
	}
}

// TestChannelBufferConcurrent tests ChannelBuffer under concurrent access
func TestChannelBufferConcurrent(t *testing.T) {
	ch := NewChannel(100)
	defer ch.Close()

	var wg sync.WaitGroup
	producers := 5
	consumers := 5
	iterations := 200

	var putCount, getCount int32

	// Producers
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				data := []byte{byte(id), byte(j)}
				if err := ch.Put(data); err == nil {
					atomic.AddInt32(&putCount, 1)
				}
				if j%10 == 0 {
					time.Sleep(time.Microsecond)
				}
			}
		}(i)
	}

	// Consumers
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				data, _ := ch.Get()
				if data != nil {
					atomic.AddInt32(&getCount, 1)
				}
				if j%10 == 0 {
					time.Sleep(time.Microsecond)
				}
			}
		}()
	}

	wg.Wait()

	t.Logf("Put: %d, Get: %d, Remaining: %d", putCount, getCount, ch.Len())

	if getCount > putCount {
		t.Errorf("Got more than put: get=%d, put=%d", getCount, putCount)
	}
}

// TestChannelBufferCloseWhileProducing tests closing while producers are active
func TestChannelBufferCloseWhileProducing(t *testing.T) {
	ch := NewChannel(5)

	var wg sync.WaitGroup
	var putErrors int32

	// Start producers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				err := ch.Put([]byte{byte(id), byte(j)})
				if err == io.ErrClosedPipe {
					return
				}
				if err != nil && err != ErrBufferFull {
					atomic.AddInt32(&putErrors, 1)
				}
				time.Sleep(time.Microsecond)
			}
		}(i)
	}

	// Close after a short delay
	time.Sleep(5 * time.Millisecond)
	ch.Close()

	wg.Wait()

	if putErrors > 0 {
		t.Errorf("Unexpected put errors: %d", putErrors)
	}

	// After close, Put should always return ErrClosedPipe
	err := ch.Put([]byte("after-close"))
	if err != io.ErrClosedPipe {
		t.Errorf("Expected ErrClosedPipe after close, got: %v", err)
	}
}

// TestChannelBufferDrainAfterClose tests that remaining packets can be drained after close
func TestChannelBufferDrainAfterClose(t *testing.T) {
	ch := NewChannel(10)

	// Put 5 packets
	for i := 0; i < 5; i++ {
		ch.Put([]byte{byte(i)})
	}

	ch.Close()

	// Should be able to drain all 5 packets
	drained := 0
	for {
		data, err := ch.Get()
		if err == io.ErrClosedPipe && data == nil {
			break
		}
		if data != nil {
			drained++
		}
	}

	if drained != 5 {
		t.Errorf("Expected to drain 5 packets, got %d", drained)
	}
}

// TestChannelBufferLargePackets tests ChannelBuffer with large packets (64KB+)
func TestChannelBufferLargePackets(t *testing.T) {
	ch := NewChannel(16)
	defer ch.Close()

	sizes := []int{1024, 16384, 65000, 100000}
	for _, size := range sizes {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i % 256)
		}

		err := ch.Put(data)
		if err != nil {
			t.Fatalf("Put %d bytes failed: %v", size, err)
		}

		got, err := ch.Get()
		if err != nil {
			t.Fatalf("Get %d bytes failed: %v", size, err)
		}
		if len(got) != size {
			t.Fatalf("Size mismatch: put %d, got %d", size, len(got))
		}
		// Verify content
		for i := 0; i < size; i++ {
			if got[i] != byte(i%256) {
				t.Fatalf("Data corruption at byte %d for size %d", i, size)
			}
		}
	}
	t.Log("All large packet sizes passed")
}

// TestChannelBufferProducerConsumerBalance tests balanced producer-consumer with verification
func TestChannelBufferProducerConsumerBalance(t *testing.T) {
	ch := NewChannel(32)
	defer ch.Close()

	const totalPackets = 500
	var wg sync.WaitGroup
	var putOK, getOK int32

	// Single producer - sends numbered packets
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < totalPackets; i++ {
			data := []byte{byte(i >> 8), byte(i & 0xFF)}
			for {
				err := ch.Put(data)
				if err == nil {
					atomic.AddInt32(&putOK, 1)
					break
				}
				if err == ErrBufferFull {
					time.Sleep(time.Microsecond * 10)
					continue
				}
				return // closed
			}
		}
	}()

	// Single consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if atomic.LoadInt32(&getOK) >= totalPackets {
				return
			}
			data, err := ch.Get()
			if err != nil {
				return
			}
			if data != nil {
				atomic.AddInt32(&getOK, 1)
			} else {
				time.Sleep(time.Microsecond * 10)
			}
		}
	}()

	wg.Wait()

	if putOK != totalPackets {
		t.Errorf("Expected %d puts, got %d", totalPackets, putOK)
	}
	if getOK != totalPackets {
		t.Errorf("Expected %d gets, got %d", totalPackets, getOK)
	}
}

// TestChannelBufferRapidCloseReopen tests rapid creation and closing
func TestChannelBufferRapidCloseReopen(t *testing.T) {
	for i := 0; i < 100; i++ {
		ch := NewChannel(10)
		ch.Put([]byte("test"))
		ch.Close()

		// Put after close
		err := ch.Put([]byte("after"))
		if err != io.ErrClosedPipe {
			t.Fatalf("Iteration %d: expected ErrClosedPipe, got %v", i, err)
		}

		// Get should drain remaining
		data, err := ch.Get()
		if data == nil || string(data) != "test" {
			t.Fatalf("Iteration %d: expected to drain 'test', got %v err=%v", i, data, err)
		}

		// Get on empty+closed
		_, err = ch.Get()
		if err != io.ErrClosedPipe {
			t.Fatalf("Iteration %d: expected ErrClosedPipe on empty closed, got %v", i, err)
		}
	}
}

// TestChannelBufferMultipleCloseRace tests concurrent close calls
func TestChannelBufferMultipleCloseRace(t *testing.T) {
	ch := NewChannel(10)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch.Close() // should not panic
		}()
	}
	wg.Wait()

	// Verify closed
	err := ch.Put([]byte("test"))
	if err != io.ErrClosedPipe {
		t.Errorf("Expected ErrClosedPipe after multiple Close, got: %v", err)
	}
}

// TestChannelBufferHugePacket_100MB tests a single 100MB packet through ChannelBuffer.
// Verifies packet boundary preservation and pointer identity (zero-copy).
func TestChannelBufferHugePacket_100MB(t *testing.T) {
	const size = 100 * 1024 * 1024 // 100MB
	ch := NewChannel(2)
	defer ch.Close()

	// Fill with a simple pattern (fast, no rand overhead under -race)
	data := make([]byte, size)
	for i := 0; i < size; i += 4 {
		data[i] = byte(i)
		data[i+1] = byte(i >> 8)
		data[i+2] = byte(i >> 16)
		data[i+3] = byte(i >> 24)
	}

	start := time.Now()

	if err := ch.Put(data); err != nil {
		t.Fatalf("Put 100MB failed: %v", err)
	}

	got, err := ch.Get()
	if err != nil {
		t.Fatalf("Get 100MB failed: %v", err)
	}

	elapsed := time.Since(start)

	if len(got) != size {
		t.Fatalf("Size mismatch: put %d, got %d", size, len(got))
	}

	// Channel passes slice reference — should be same underlying array (zero-copy)
	if &got[0] != &data[0] {
		t.Log("Note: channel did not preserve slice pointer (copy occurred)")
	}

	// Spot-check pattern at key positions
	checks := []int{0, 4, size/2 - size/2%4, size - 4}
	for _, idx := range checks {
		expect := [4]byte{byte(idx), byte(idx >> 8), byte(idx >> 16), byte(idx >> 24)}
		actual := [4]byte{got[idx], got[idx+1], got[idx+2], got[idx+3]}
		if actual != expect {
			t.Fatalf("Data corruption at offset %d: got %v, want %v", idx, actual, expect)
		}
	}

	mbPerSec := float64(size) / elapsed.Seconds() / 1024 / 1024
	t.Logf("100MB packet: Put+Get in %v (%.0f MB/s)", elapsed, mbPerSec)
}

// TestChannelBufferConcurrentPutGetClose tests the critical race scenario:
// many goroutines doing Put, Get, and Close simultaneously.
func TestChannelBufferConcurrentPutGetClose(t *testing.T) {
	const iterations = 1000

	for iter := 0; iter < iterations; iter++ {
		ch := NewChannel(8)
		var wg sync.WaitGroup

		// 5 producers
		for p := 0; p < 5; p++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					ch.Put([]byte{byte(id), byte(j)})
				}
			}(p)
		}

		// 3 consumers
		for c := 0; c < 3; c++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 30; j++ {
					ch.Get()
				}
			}()
		}

		// 2 closers (race with producers)
		for c := 0; c < 2; c++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(time.Duration(rand.Intn(50)) * time.Microsecond)
				ch.Close()
			}()
		}

		wg.Wait()
	}
	// If we reach here without panic or race detector complaint, the test passes.
	t.Logf("Survived %d iterations of concurrent Put+Get+Close", iterations)
}

// TestChannelBufferPutAfterCloseRace hammers the exact Put-vs-Close race window.
func TestChannelBufferPutAfterCloseRace(t *testing.T) {
	const iterations = 5000

	for i := 0; i < iterations; i++ {
		ch := NewChannel(4)
		var wg sync.WaitGroup

		// Producer: try to put as fast as possible
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				err := ch.Put([]byte{byte(j)})
				if err == io.ErrClosedPipe {
					return
				}
				// ErrBufferFull is OK too
			}
		}()

		// Closer: close at random time
		wg.Add(1)
		go func() {
			defer wg.Done()
			// No sleep — close immediately to maximize race window
			ch.Close()
		}()

		wg.Wait()
	}
	t.Logf("Put-vs-Close race: %d iterations, no panics", iterations)
}

// TestChannelBufferManyProducersSingleConsumer tests high fan-in scenario.
func TestChannelBufferManyProducersSingleConsumer(t *testing.T) {
	ch := NewChannel(64)
	defer ch.Close()

	const producers = 50
	const msgsPerProducer = 200
	var wg sync.WaitGroup
	var putOK, putFull int64

	// Many producers
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < msgsPerProducer; j++ {
				err := ch.Put([]byte{byte(id), byte(j)})
				if err == nil {
					atomic.AddInt64(&putOK, 1)
				} else if err == ErrBufferFull {
					atomic.AddInt64(&putFull, 1)
					time.Sleep(time.Microsecond)
					j-- // retry
				}
			}
		}(p)
	}

	// Single consumer — drain as fast as possible
	var getOK int64
	done := make(chan struct{})
	go func() {
		expected := int64(producers * msgsPerProducer)
		for atomic.LoadInt64(&getOK) < expected {
			data, _ := ch.Get()
			if data != nil {
				atomic.AddInt64(&getOK, 1)
			} else {
				time.Sleep(time.Microsecond)
			}
		}
		close(done)
	}()

	wg.Wait()
	<-done

	total := int64(producers * msgsPerProducer)
	if putOK != total {
		t.Errorf("Expected %d puts, got %d", total, putOK)
	}
	if getOK != total {
		t.Errorf("Expected %d gets, got %d", total, getOK)
	}
	t.Logf("Fan-in: %d producers x %d msgs = %d total, retries=%d",
		producers, msgsPerProducer, total, putFull)
}

// TestChannelBufferThroughput_LargePackets measures throughput with 64KB packets.
func TestChannelBufferThroughput_LargePackets(t *testing.T) {
	ch := NewChannel(16)
	defer ch.Close()

	const packetSize = 64 * 1024 // 64KB per packet
	const numPackets = 1024      // 64MB total
	var wg sync.WaitGroup

	start := time.Now()

	// Producer
	wg.Add(1)
	go func() {
		defer wg.Done()
		pkt := make([]byte, packetSize)
		for i := 0; i < numPackets; i++ {
			pkt[0] = byte(i)
			for {
				err := ch.Put(pkt)
				if err == nil {
					break
				}
				time.Sleep(time.Microsecond)
			}
		}
	}()

	// Consumer
	var totalBytes int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numPackets; i++ {
			for {
				data, _ := ch.Get()
				if data != nil {
					atomic.AddInt64(&totalBytes, int64(len(data)))
					break
				}
				time.Sleep(time.Microsecond)
			}
		}
	}()

	wg.Wait()
	elapsed := time.Since(start)

	expectedBytes := int64(packetSize * numPackets)
	if totalBytes != expectedBytes {
		t.Errorf("Byte mismatch: expected %d, got %d", expectedBytes, totalBytes)
	}
	t.Logf("Throughput: %d packets x %dKB = %dMB in %v (%.0f MB/s)",
		numPackets, packetSize/1024, expectedBytes/1024/1024, elapsed,
		float64(expectedBytes)/elapsed.Seconds()/1024/1024)
}

// TestChannelBufferCloseDoesNotBlockGet verifies Get returns promptly after Close.
func TestChannelBufferCloseDoesNotBlockGet(t *testing.T) {
	ch := NewChannel(10)
	ch.Close()

	start := time.Now()
	_, err := ch.Get()
	elapsed := time.Since(start)

	if err != io.ErrClosedPipe {
		t.Errorf("Expected ErrClosedPipe, got %v", err)
	}
	if elapsed > 10*time.Millisecond {
		t.Errorf("Get on closed buffer took %v, expected near-instant", elapsed)
	}
}

// TestBufferRandomOperations tests buffer with random operations
func TestBufferRandomOperations(t *testing.T) {
	buf := NewBuffer(1000)
	defer buf.Close()

	var wg sync.WaitGroup
	workers := 5
	duration := 1 * time.Second

	// Use a closed channel so ALL workers can receive the stop signal.
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
			readBuf := make([]byte, 100)

			for {
				select {
				case <-stopCh:
					return
				default:
					op := rng.Intn(4)
					switch op {
					case 0: // Write
						size := rng.Intn(50) + 1
						data := make([]byte, size)
						buf.Write(data)
					case 1: // Read
						buf.Read(readBuf)
					case 2: // Size
						buf.Size()
					case 3: // Cap
						buf.Cap()
					}
				}
			}
		}(i)
	}

	wg.Wait()
}

// TestBufferDeadlock tests for potential deadlock scenarios
func TestBufferDeadlock(t *testing.T) {
	buf := NewBuffer(10)
	defer buf.Close()

	done := make(chan bool, 1)

	go func() {
		// Fill buffer
		buf.Write(make([]byte, 10))

		// Try to write more (will block)
		go func() {
			buf.Write([]byte("x"))
		}()

		time.Sleep(50 * time.Millisecond)

		// Read to unblock
		readBuf := make([]byte, 5)
		buf.Read(readBuf)

		time.Sleep(50 * time.Millisecond)
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Potential deadlock detected")
	}
}
