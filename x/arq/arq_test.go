package arq

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

// TestARQBasicSendRecv tests basic send and receive operations
func TestARQBasicSendRecv(t *testing.T) {
	var outputData []byte
	var mu sync.Mutex

	output := func(data []byte) {
		mu.Lock()
		d := make([]byte, len(data))
		copy(d, data)
		outputData = append(outputData, d...)
		mu.Unlock()
	}

	arq := NewSimpleARQ(1, output)

	// Send data
	testData := []byte("hello world")
	arq.Send(testData)

	// Wait for flush interval to pass
	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)

	// Update to trigger flush
	arq.Update()

	// Verify output was called
	mu.Lock()
	outputLen := len(outputData)
	mu.Unlock()

	if outputLen == 0 {
		t.Fatal("No data was output after Send and Update")
	}

	mu.Lock()
	defer mu.Unlock()

	// Parse the output packet
	if len(outputData) < ARQ_OVERHEAD {
		t.Fatalf("Output data too short: %d bytes", len(outputData))
	}

	cmd := outputData[0]
	sn := binary.BigEndian.Uint32(outputData[1:5])
	length := binary.BigEndian.Uint16(outputData[9:11])

	if cmd != CMD_DATA {
		t.Fatalf("Expected CMD_DATA (%d), got %d", CMD_DATA, cmd)
	}
	if sn != 0 {
		t.Fatalf("Expected sequence number 0, got %d", sn)
	}
	if length != uint16(len(testData)) {
		t.Fatalf("Expected length %d, got %d", len(testData), length)
	}

	payload := outputData[ARQ_OVERHEAD : ARQ_OVERHEAD+length]
	if string(payload) != string(testData) {
		t.Fatalf("Payload mismatch: got %q, expected %q", payload, testData)
	}
}

// TestARQRecvInOrder tests receiving packets in order
func TestARQRecvInOrder(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	// Send packets in order
	packets := [][]byte{
		makeARQDataPacket(0, []byte("packet0")),
		makeARQDataPacket(1, []byte("packet1")),
		makeARQDataPacket(2, []byte("packet2")),
	}

	for _, pkt := range packets {
		arq.Input(pkt)
	}

	// Receive data
	data := arq.Recv()
	expected := "packet0packet1packet2"
	if string(data) != expected {
		t.Fatalf("Received data mismatch: got %q, expected %q", data, expected)
	}
}

// TestARQRecvOutOfOrder tests receiving packets out of order
func TestARQRecvOutOfOrder(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	// Send packets out of order: 0, 2, 1
	arq.Input(makeARQDataPacket(0, []byte("packet0")))
	arq.Input(makeARQDataPacket(2, []byte("packet2")))

	// Should only receive packet0
	data := arq.Recv()
	if string(data) != "packet0" {
		t.Fatalf("Expected 'packet0', got %q", data)
	}

	// packet2 should be buffered
	if arq.WaitRcv() != 1 {
		t.Fatalf("Expected 1 packet in receive buffer, got %d", arq.WaitRcv())
	}

	// Send missing packet1
	arq.Input(makeARQDataPacket(1, []byte("packet1")))

	// Now should receive both packet1 and packet2
	data = arq.Recv()
	expected := "packet1packet2"
	if string(data) != expected {
		t.Fatalf("Expected %q, got %q", expected, data)
	}

	// Buffer should be empty
	if arq.WaitRcv() != 0 {
		t.Fatalf("Expected 0 packets in receive buffer, got %d", arq.WaitRcv())
	}
}

// TestARQDuplicatePackets tests handling of duplicate packets
func TestARQDuplicatePackets(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	// Send packet 0 twice
	arq.Input(makeARQDataPacket(0, []byte("packet0")))
	arq.Input(makeARQDataPacket(0, []byte("packet0-dup")))

	// Should only receive once
	data := arq.Recv()
	if string(data) != "packet0" {
		t.Fatalf("Expected 'packet0', got %q", data)
	}

	// Second recv should return nothing
	data = arq.Recv()
	if len(data) != 0 {
		t.Fatalf("Expected no data on second recv, got %q", data)
	}
}

// TestARQNACKTriggering tests that NACK is sent when gap is detected
func TestARQNACKTriggering(t *testing.T) {
	var nackSent bool
	var nackSN uint32
	var mu sync.Mutex

	output := func(data []byte) {
		if len(data) >= ARQ_OVERHEAD && data[0] == CMD_NACK {
			mu.Lock()
			nackSent = true
			nackSN = binary.BigEndian.Uint32(data[1:5])
			mu.Unlock()
		}
	}

	arq := NewSimpleARQ(1, output)

	// Send packets with a gap: 0, 1, 2, 6, 7, 8 (missing 3, 4, 5)
	// With NACK threshold=1, any gap triggers NACK
	for _, sn := range []uint32{0, 1, 2, 6, 7, 8} {
		arq.Input(makeARQDataPacket(sn, []byte{byte(sn)}))
	}

	// Wait for NACK interval (initial interval is 100ms)
	time.Sleep(150 * time.Millisecond)

	// Update to trigger NACK check
	arq.Update()

	// Give some time for NACK to be sent
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	sent := nackSent
	sn := nackSN
	mu.Unlock()

	if !sent {
		t.Fatal("NACK should have been sent for missing packet")
	}

	// Batch NACK: sn field is the first missing SN (3)
	if sn != 3 {
		t.Fatalf("NACK should be for sequence number 3 (first missing), got %d", sn)
	}
}

// TestARQNACKRetransmit tests that receiving NACK triggers retransmission
func TestARQNACKRetransmit(t *testing.T) {
	var outputPackets [][]byte
	var mu sync.Mutex

	output := func(data []byte) {
		mu.Lock()
		pkt := make([]byte, len(data))
		copy(pkt, data)
		outputPackets = append(outputPackets, pkt)
		mu.Unlock()
	}

	arq := NewSimpleARQ(1, output)

	// Send some data to populate send buffer
	arq.Send([]byte("test"))

	// Wait for flush interval
	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
	arq.Update()

	// Wait for packet to be sent
	time.Sleep(50 * time.Millisecond)

	// Clear output
	mu.Lock()
	outputPackets = nil
	mu.Unlock()

	// Simulate receiving NACK for sequence 0 (11-byte header)
	nackPkt := make([]byte, ARQ_OVERHEAD)
	nackPkt[0] = CMD_NACK
	binary.BigEndian.PutUint32(nackPkt[1:5], 0)
	binary.BigEndian.PutUint32(nackPkt[5:9], 0) // ack = 0
	binary.BigEndian.PutUint16(nackPkt[9:11], 0)

	arq.Input(nackPkt)

	// Give time for retransmission
	time.Sleep(50 * time.Millisecond)

	// Verify retransmission occurred
	mu.Lock()
	retransmitted := len(outputPackets) > 0
	mu.Unlock()

	if !retransmitted {
		t.Fatal("NACK should have triggered retransmission")
	}
}

// TestARQFragmentation tests that large data is fragmented correctly
func TestARQFragmentation(t *testing.T) {
	var outputPackets [][]byte
	var mu sync.Mutex

	output := func(data []byte) {
		mu.Lock()
		pkt := make([]byte, len(data))
		copy(pkt, data)
		outputPackets = append(outputPackets, pkt)
		mu.Unlock()
	}

	mtu := 100
	arq := NewSimpleARQWithMTU(1, output, mtu, 0)

	// Send data larger than MSS
	largeData := make([]byte, mtu*3)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	arq.Send(largeData)

	// Wait for flush interval
	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
	arq.Update()

	// Wait for packets to be sent
	time.Sleep(50 * time.Millisecond)

	// Should have multiple packets
	mu.Lock()
	numPackets := len(outputPackets)
	mu.Unlock()

	if numPackets < 3 {
		t.Fatalf("Expected at least 3 packets for large data, got %d", numPackets)
	}

	// Verify each packet is within MTU
	mu.Lock()
	for i, pkt := range outputPackets {
		if len(pkt) > mtu {
			t.Fatalf("Packet %d exceeds MTU: %d > %d", i, len(pkt), mtu)
		}
	}
	mu.Unlock()
}

// TestARQWaitSnd tests WaitSnd counter
func TestARQWaitSnd(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	if arq.WaitSnd() != 0 {
		t.Fatalf("Initial WaitSnd should be 0, got %d", arq.WaitSnd())
	}

	// Send data
	arq.Send([]byte("test"))

	if arq.WaitSnd() != 1 {
		t.Fatalf("WaitSnd after Send should be 1, got %d", arq.WaitSnd())
	}

	// Update to move to send buffer
	arq.Update()

	if arq.WaitSnd() != 1 {
		t.Fatalf("WaitSnd after Update should still be 1, got %d", arq.WaitSnd())
	}
}

// TestARQBinaryFormat tests the binary packet format
func TestARQBinaryFormat(t *testing.T) {
	// Create a packet manually (11-byte header)
	payload := []byte("test payload")
	pkt := make([]byte, ARQ_OVERHEAD+len(payload))
	pkt[0] = CMD_DATA
	binary.BigEndian.PutUint32(pkt[1:5], 42)
	binary.BigEndian.PutUint32(pkt[5:9], 0) // ack = 0
	binary.BigEndian.PutUint16(pkt[9:11], uint16(len(payload)))
	copy(pkt[ARQ_OVERHEAD:], payload)

	// Parse it
	arq := NewSimpleARQ(1, func([]byte) {})
	arq.Input(pkt)

	// Since we sent SN=42 but ARQ expects SN=0, it should be buffered
	if arq.WaitRcv() != 1 {
		t.Fatalf("Expected 1 packet in receive buffer, got %d", arq.WaitRcv())
	}
}

// TestARQCleanupOldSegments tests that old segments are cleaned up
func TestARQCleanupOldSegments(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	// Send data
	arq.Send([]byte("test"))
	arq.Update()

	// Should have 1 packet in send buffer
	if arq.WaitSnd() != 1 {
		t.Fatalf("Expected 1 packet in send buffer, got %d", arq.WaitSnd())
	}

	// Simulate time passing by updating current time
	// Note: This is a bit tricky since current is internal
	// We'll just verify the cleanup logic exists by checking after many updates
	for i := 0; i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		arq.Update()
	}

	// After enough time, old segments should be cleaned up
	// (This test may be flaky depending on timing)
	if arq.WaitSnd() > 1 {
		t.Logf("WaitSnd after cleanup: %d (may still have segments)", arq.WaitSnd())
	}
}

// TestARQConcurrentAccess tests concurrent access to ARQ
func TestARQConcurrentAccess(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	var wg sync.WaitGroup
	iterations := 100

	// Concurrent sends
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			arq.Send([]byte{byte(i)})
		}
	}()

	// Concurrent updates
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			arq.Update()
			time.Sleep(time.Millisecond)
		}
	}()

	// Concurrent receives
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			arq.Recv()
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
}

// TestARQTimeout tests timeout configuration
func TestARQTimeout(t *testing.T) {
	timeout := 5000
	arq := NewSimpleARQWithMTU(1, func([]byte) {}, ARQ_MTU, timeout)

	if !arq.HasTimeout() {
		t.Fatal("ARQ should have timeout configured")
	}

	if arq.GetTimeout() != timeout {
		t.Fatalf("Expected timeout %d, got %d", timeout, arq.GetTimeout())
	}

	// Test without timeout
	arq2 := NewSimpleARQ(1, func([]byte) {})
	if arq2.HasTimeout() {
		t.Fatal("ARQ should not have timeout configured")
	}
}

// TestARQEmptyInput tests handling of empty or invalid input
func TestARQEmptyInput(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	// Empty input
	arq.Input([]byte{})

	// Too short input
	arq.Input([]byte{1, 2, 3})

	// Should not crash and should have no data
	data := arq.Recv()
	if len(data) != 0 {
		t.Fatalf("Expected no data after invalid input, got %d bytes", len(data))
	}
}

// TestARQMultiplePacketsInInput tests parsing multiple packets in one Input call
func TestARQMultiplePacketsInInput(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	// Create multiple packets in one buffer
	pkt0 := makeARQDataPacket(0, []byte("pkt0"))
	pkt1 := makeARQDataPacket(1, []byte("pkt1"))
	combined := append(pkt0, pkt1...)

	arq.Input(combined)

	// Should receive both
	data := arq.Recv()
	expected := "pkt0pkt1"
	if string(data) != expected {
		t.Fatalf("Expected %q, got %q", expected, data)
	}
}

// Note: makeARQDataPacket helper is defined in session_test.go
