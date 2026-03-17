package simplex

import (
	"math/rand"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPeekableChannelConcurrentPeekGet tests concurrent Peek and Get operations
func TestPeekableChannelConcurrentPeekGet(t *testing.T) {
	ch := NewPeekableChannel(100)
	defer ch.Close()

	var wg sync.WaitGroup
	duration := 500 * time.Millisecond
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	var putCount, getCount, peekCount int32

	// Producer
	wg.Add(1)
	go func() {
		defer wg.Done()
		counter := 0
		for {
			select {
			case <-stopCh:
				return
			default:
				pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(counter)})
				err := ch.Put(pkt)
				if err == nil {
					atomic.AddInt32(&putCount, 1)
					counter++
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	// Peek consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				pkt, _ := ch.Peek()
				if pkt != nil {
					atomic.AddInt32(&peekCount, 1)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Get consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				pkt, _ := ch.Get()
				if pkt != nil {
					atomic.AddInt32(&getCount, 1)
				}
				time.Sleep(15 * time.Millisecond)
			}
		}
	}()

	wg.Wait()

	t.Logf("Put: %d, Peek: %d, Get: %d", putCount, peekCount, getCount)

	if getCount > putCount {
		t.Errorf("Got more than put: get=%d, put=%d", getCount, putCount)
	}
}

// TestPeekableChannelCloseWhileBlocked tests closing channel with blocked operations
func TestPeekableChannelCloseWhileBlocked(t *testing.T) {
	ch := NewPeekableChannel(2)

	// Fill channel
	ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("1")))
	ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("2")))

	// Try to put more (will fail immediately since non-blocking)
	err := ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("3")))
	if err == nil {
		t.Error("Put to full channel should fail")
	}

	// Close channel
	ch.Close()

	// Operations should fail
	err = ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("4")))
	if err == nil {
		t.Error("Put after close should fail")
	}

	_, err = ch.Get()
	if err == nil {
		t.Error("Get after close should fail")
	}
}

// TestSimplexBufferConcurrentPutGet tests concurrent packet operations
func TestSimplexBufferConcurrentPutGet(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 10000,
	}

	buf := NewSimplexBuffer(addr)

	var wg sync.WaitGroup
	duration := 1 * time.Second
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	var putCount, getCount int32

	// Multiple producers
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			counter := 0
			for {
				select {
				case <-stopCh:
					return
				default:
					pktType := SimplexPacketTypeDATA
					if counter%5 == 0 {
						pktType = SimplexPacketTypeCTRL
					}
					pkt := NewSimplexPacket(pktType, []byte{byte(id), byte(counter)})
					err := buf.PutPacket(pkt)
					if err == nil {
						atomic.AddInt32(&putCount, 1)
					}
					counter++
					time.Sleep(5 * time.Millisecond)
				}
			}
		}(i)
	}

	// Multiple consumers
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
					pkt, _ := buf.GetPacket()
					if pkt != nil {
						atomic.AddInt32(&getCount, 1)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()
	}

	wg.Wait()

	t.Logf("Put: %d, Get: %d", putCount, getCount)

	if getCount > putCount {
		t.Errorf("Got more than put: get=%d, put=%d", getCount, putCount)
	}
}

// TestSimplexBufferPriorityUnderLoad tests CTRL priority under heavy load
func TestSimplexBufferPriorityUnderLoad(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	// Fill with DATA packets
	for i := 0; i < 50; i++ {
		buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(i)}))
	}

	// Add CTRL packets
	for i := 0; i < 10; i++ {
		buf.PutPacket(NewSimplexPacket(SimplexPacketTypeCTRL, []byte{byte(100 + i)}))
	}

	// Get packets - should get CTRL first
	ctrlCount := 0
	for i := 0; i < 10; i++ {
		pkt, _ := buf.GetPacket()
		if pkt != nil && pkt.PacketType == SimplexPacketTypeCTRL {
			ctrlCount++
		}
	}

	if ctrlCount != 10 {
		t.Errorf("Expected 10 CTRL packets first, got %d", ctrlCount)
	}
}

// TestSimplexBufferGetPacketsBoundary tests GetPackets at size boundary
func TestSimplexBufferGetPacketsBoundary(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 100,
	}

	buf := NewSimplexBuffer(addr)

	// Add packets that exactly fill maxBodySize
	// Each packet with 10 bytes data = 5+10=15 bytes
	// 100/15 = 6 packets (90 bytes), 7th would exceed
	for i := 0; i < 20; i++ {
		buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, make([]byte, 10)))
	}

	pkts, err := buf.GetPackets()
	if err != nil {
		t.Fatalf("GetPackets failed: %v", err)
	}

	// Should get exactly 6 packets
	if len(pkts.Packets) != 6 {
		t.Errorf("Expected 6 packets, got %d (size: %d)", len(pkts.Packets), pkts.Size())
	}

	// Total size should not exceed maxBodySize
	if pkts.Size() > addr.maxBodySize {
		t.Errorf("Size %d exceeds maxBodySize %d", pkts.Size(), addr.maxBodySize)
	}
}

// TestSimplexPacketLargeData tests packet with very large data
func TestSimplexPacketLargeData(t *testing.T) {
	// Create 1MB packet
	largeData := make([]byte, 1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	pkt := NewSimplexPacket(SimplexPacketTypeDATA, largeData)
	marshaled := pkt.Marshal()

	// Parse it back
	parsed, err := ParseSimplexPacket(marshaled)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(parsed.Data) != len(largeData) {
		t.Errorf("Data length mismatch: got %d, expected %d", len(parsed.Data), len(largeData))
	}

	// Verify data integrity
	for i := 0; i < len(largeData); i++ {
		if parsed.Data[i] != largeData[i] {
			t.Errorf("Data mismatch at position %d", i)
			break
		}
	}
}

// TestSimplexPacketsSplitReassemble tests splitting and reassembling large data
func TestSimplexPacketsSplitReassemble(t *testing.T) {
	// Create large data
	originalData := make([]byte, 10000)
	for i := range originalData {
		originalData[i] = byte(i % 256)
	}

	// Split into packets
	maxSize := 1000
	pkts := NewSimplexPacketWithMaxSize(originalData, SimplexPacketTypeDATA, maxSize)

	t.Logf("Split %d bytes into %d packets", len(originalData), len(pkts.Packets))

	// Reassemble
	var reassembled []byte
	for _, pkt := range pkts.Packets {
		reassembled = append(reassembled, pkt.Data...)
	}

	// Verify
	if len(reassembled) != len(originalData) {
		t.Fatalf("Length mismatch: got %d, expected %d", len(reassembled), len(originalData))
	}

	for i := range originalData {
		if reassembled[i] != originalData[i] {
			t.Errorf("Data mismatch at position %d", i)
			break
		}
	}
}

// TestSimplexPacketsRandomOrder tests parsing packets in random order
func TestSimplexPacketsRandomOrder(t *testing.T) {
	// Create multiple packets
	original := NewSimplexPackets()
	for i := 0; i < 20; i++ {
		original.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(i)}))
	}

	marshaled := original.Marshal()

	// Parse should work regardless of internal order
	parsed, err := ParseSimplexPackets(marshaled)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(parsed.Packets) != len(original.Packets) {
		t.Errorf("Packet count mismatch: got %d, expected %d", len(parsed.Packets), len(original.Packets))
	}
}

// TestSimplexBufferRapidPutGet tests rapid put/get cycles
func TestSimplexBufferRapidPutGet(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	for i := 0; i < 1000; i++ {
		pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(i)})
		buf.PutPacket(pkt)

		retrieved, _ := buf.GetPacket()
		if retrieved == nil {
			t.Fatalf("Failed to get packet at iteration %d", i)
		}

		if retrieved.Data[0] != byte(i) {
			t.Fatalf("Data mismatch at iteration %d: got %d, expected %d", i, retrieved.Data[0], byte(i))
		}
	}
}

// TestPeekableChannelPeekStability tests that peeked packet remains stable
func TestPeekableChannelPeekStability(t *testing.T) {
	ch := NewPeekableChannel(10)
	defer ch.Close()

	pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("test"))
	ch.Put(pkt)

	// Peek multiple times
	for i := 0; i < 10; i++ {
		peeked, _ := ch.Peek()
		if peeked == nil {
			t.Fatal("Peek returned nil")
		}
		if string(peeked.Data) != "test" {
			t.Fatalf("Peeked data changed: %s", peeked.Data)
		}
	}

	// Get should return same packet
	retrieved, _ := ch.Get()
	if string(retrieved.Data) != "test" {
		t.Fatalf("Get returned different data: %s", retrieved.Data)
	}

	// Channel should be empty now
	empty, _ := ch.Peek()
	if empty != nil {
		t.Fatal("Channel should be empty after Get")
	}
}

// TestSimplexBufferMixedPacketTypes tests buffer with mixed packet types
func TestSimplexBufferMixedPacketTypes(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	// Add packets in mixed order
	buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte("data1")))
	buf.PutPacket(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("ctrl1")))
	buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte("data2")))
	buf.PutPacket(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("ctrl2")))
	buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte("data3")))

	// Should get CTRL packets first
	pkt1, _ := buf.GetPacket()
	if pkt1.PacketType != SimplexPacketTypeCTRL || string(pkt1.Data) != "ctrl1" {
		t.Error("First packet should be ctrl1")
	}

	pkt2, _ := buf.GetPacket()
	if pkt2.PacketType != SimplexPacketTypeCTRL || string(pkt2.Data) != "ctrl2" {
		t.Error("Second packet should be ctrl2")
	}

	// Then DATA packets
	pkt3, _ := buf.GetPacket()
	if pkt3.PacketType != SimplexPacketTypeDATA || string(pkt3.Data) != "data1" {
		t.Error("Third packet should be data1")
	}
}

// TestSimplexBufferStress tests buffer under stress
func TestSimplexBufferStress(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 5000,
	}

	buf := NewSimplexBuffer(addr)

	var wg sync.WaitGroup
	duration := 1500 * time.Millisecond
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	var operations int64

	// Random operations
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))

			for {
				select {
				case <-stopCh:
					return
				default:
					op := rng.Intn(5)
					switch op {
					case 0: // PutPacket
						pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(rng.Intn(256))})
						buf.PutPacket(pkt)
					case 1: // GetPacket
						buf.GetPacket()
					case 2: // Peek
						buf.Peek()
					case 3: // GetPackets
						buf.GetPackets()
					case 4: // PutPackets
						pkts := NewSimplexPackets()
						pkts.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(rng.Intn(256))}))
						buf.PutPackets(pkts)
					}
					atomic.AddInt64(&operations, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Completed %d operations in %v", operations, duration)
}

// TestAsymBufferConcurrent tests AsymBuffer under concurrent access
func TestAsymBufferConcurrent(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	asymBuf := NewAsymBuffer(addr)
	defer asymBuf.Close()

	var wg sync.WaitGroup
	duration := 500 * time.Millisecond
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	// Write to WriteBuf
	wg.Add(1)
	go func() {
		defer wg.Done()
		counter := 0
		for {
			select {
			case <-stopCh:
				return
			default:
				pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(counter)})
				asymBuf.WriteBuf().PutPacket(pkt)
				counter++
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Read from WriteBuf
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				asymBuf.WriteBuf().GetPacket()
				time.Sleep(15 * time.Millisecond)
			}
		}
	}()

	// Write to ReadBuf
	wg.Add(1)
	go func() {
		defer wg.Done()
		counter := 0
		for {
			select {
			case <-stopCh:
				return
			default:
				pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(counter)})
				asymBuf.ReadBuf().PutPacket(pkt)
				counter++
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Read from ReadBuf
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				asymBuf.ReadBuf().GetPacket()
				time.Sleep(15 * time.Millisecond)
			}
		}
	}()

	wg.Wait()

	t.Log("AsymBuffer concurrent test completed")
}

// TestSimplexPacketCorruption tests handling of corrupted packet data
func TestSimplexPacketCorruption(t *testing.T) {
	pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("test data"))
	marshaled := pkt.Marshal()

	// Corrupt the length field
	marshaled[1] = 0xFF
	marshaled[2] = 0xFF
	marshaled[3] = 0xFF
	marshaled[4] = 0xFF

	// Should handle gracefully
	parsed, err := ParseSimplexPacket(marshaled)
	if err != nil {
		t.Logf("Parse returned error (expected): %v", err)
	}
	if parsed != nil && len(parsed.Data) > 0 {
		t.Error("Should not parse corrupted packet successfully")
	}
}
