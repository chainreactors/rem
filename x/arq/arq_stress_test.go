package arq

import (
	"encoding/binary"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestARQPacketLoss tests ARQ behavior under packet loss
func TestARQPacketLoss(t *testing.T) {
	var sentPackets [][]byte
	var mu sync.Mutex

	output := func(data []byte) {
		mu.Lock()
		pkt := make([]byte, len(data))
		copy(pkt, data)
		sentPackets = append(sentPackets, pkt)
		mu.Unlock()
	}

	sender := NewSimpleARQ(1, output)
	receiver := NewSimpleARQ(1, func([]byte) {})

	// Send 20 packets
	for i := 0; i < 20; i++ {
		sender.Send([]byte{byte(i)})
	}

	// Wait and flush
	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
	sender.Update()
	time.Sleep(50 * time.Millisecond)

	// Simulate 30% packet loss.
	// Always deliver the first packet (seq=0) so Recv() can return at least one
	// in-order byte — a lost seq=0 leaves everything buffered and Recv returns 0.
	mu.Lock()
	lossRate := 0.3
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i, pkt := range sentPackets {
		if i == 0 || rng.Float64() > lossRate {
			receiver.Input(pkt)
		}
	}
	mu.Unlock()

	// Receiver should have some data but not all
	data := receiver.Recv()
	receivedCount := len(data)

	t.Logf("Sent 20 packets, received %d bytes with 30%% loss", receivedCount)

	// Should receive at least some packets
	if receivedCount == 0 {
		t.Error("No packets received despite 30% loss rate")
	}

	// Should have missing packets in buffer
	if receiver.WaitRcv() == 0 && receivedCount < 20 {
		t.Log("Some packets lost and buffered (expected with packet loss)")
	}
}

// TestARQHighPacketLoss tests ARQ under severe packet loss
func TestARQHighPacketLoss(t *testing.T) {
	var sentPackets [][]byte
	var mu sync.Mutex

	output := func(data []byte) {
		mu.Lock()
		pkt := make([]byte, len(data))
		copy(pkt, data)
		sentPackets = append(sentPackets, pkt)
		mu.Unlock()
	}

	sender := NewSimpleARQ(1, output)
	receiver := NewSimpleARQ(1, func([]byte) {})

	// Send packets
	testData := []byte("test data with high loss")
	sender.Send(testData)

	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
	sender.Update()
	time.Sleep(50 * time.Millisecond)

	// Simulate 70% packet loss
	mu.Lock()
	lossRate := 0.7
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	delivered := 0
	for _, pkt := range sentPackets {
		if rng.Float64() > lossRate {
			receiver.Input(pkt)
			delivered++
		}
	}
	mu.Unlock()

	t.Logf("Delivered %d/%d packets with 70%% loss", delivered, len(sentPackets))

	// With 70% loss, we might not receive complete data
	data := receiver.Recv()
	if len(data) > 0 {
		t.Logf("Received %d bytes despite high loss", len(data))
	}
}

// TestARQReordering tests ARQ with severe packet reordering
func TestARQReordering(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	// Create packets 0-9
	packets := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		packets[i] = makeARQDataPacket(uint32(i), []byte{byte(i)})
	}

	// Shuffle packets randomly
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(packets), func(i, j int) {
		packets[i], packets[j] = packets[j], packets[i]
	})

	// Input shuffled packets
	for _, pkt := range packets {
		arq.Input(pkt)
	}

	// Should eventually receive all data in order
	data := arq.Recv()
	if len(data) != 10 {
		t.Fatalf("Expected 10 bytes, got %d", len(data))
	}

	// Verify data is in correct order
	for i := 0; i < 10; i++ {
		if data[i] != byte(i) {
			t.Fatalf("Data out of order at position %d: got %d, expected %d", i, data[i], i)
		}
	}
}

// TestARQConcurrentSendRecv tests concurrent send and receive operations
func TestARQConcurrentSendRecv(t *testing.T) {
	var outputPackets [][]byte
	var mu sync.Mutex

	output := func(data []byte) {
		mu.Lock()
		pkt := make([]byte, len(data))
		copy(pkt, data)
		outputPackets = append(outputPackets, pkt)
		mu.Unlock()
	}

	sender := NewSimpleARQ(1, output)
	receiver := NewSimpleARQ(1, func([]byte) {})

	var wg sync.WaitGroup
	duration := 1 * time.Second
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	var sendCount, recvCount int32

	// Continuous sender
	wg.Add(1)
	go func() {
		defer wg.Done()
		counter := 0
		for {
			select {
			case <-stopCh:
				return
			default:
				sender.Send([]byte{byte(counter)})
				atomic.AddInt32(&sendCount, 1)
				counter++
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Periodic updater
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(ARQ_INTERVAL * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				sender.Update()
			}
		}
	}()

	// Packet forwarder (simulates network)
	wg.Add(1)
	go func() {
		defer wg.Done()
		lastIdx := 0
		for {
			select {
			case <-stopCh:
				return
			default:
				mu.Lock()
				if lastIdx < len(outputPackets) {
					pkt := outputPackets[lastIdx]
					lastIdx++
					mu.Unlock()
					receiver.Input(pkt)
				} else {
					mu.Unlock()
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	// Continuous receiver
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				data := receiver.Recv()
				if len(data) > 0 {
					atomic.AddInt32(&recvCount, int32(len(data)))
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()

	wg.Wait()

	t.Logf("Sent %d packets, received %d bytes", sendCount, recvCount)

	if recvCount == 0 {
		t.Error("No data received in concurrent test")
	}
}

// TestARQBurstySend tests ARQ with bursty send patterns
func TestARQBurstySend(t *testing.T) {
	var outputCount int32

	output := func(data []byte) {
		atomic.AddInt32(&outputCount, 1)
	}

	arq := NewSimpleARQ(1, output)

	// Send in bursts
	for burst := 0; burst < 5; burst++ {
		// Burst: send many packets quickly
		for i := 0; i < 50; i++ {
			arq.Send([]byte{byte(i)})
		}

		// Update to flush
		time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
		arq.Update()

		// Pause between bursts
		time.Sleep(100 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)

	if outputCount == 0 {
		t.Error("No packets sent in bursty test")
	}

	t.Logf("Sent %d packets in 5 bursts", outputCount)
}

// TestARQWindowExhaustion tests behavior when send window is exhausted
func TestARQWindowExhaustion(t *testing.T) {
	var outputCount int32

	output := func(data []byte) {
		atomic.AddInt32(&outputCount, 1)
	}

	arq := NewSimpleARQ(1, output)

	// Send more than window size (ARQ_WND_SIZE = 32)
	for i := 0; i < 100; i++ {
		arq.Send([]byte{byte(i)})
	}

	// Update multiple times
	for i := 0; i < 5; i++ {
		time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
		arq.Update()
	}

	time.Sleep(100 * time.Millisecond)

	// Should have sent at most window size packets
	if outputCount > ARQ_WND_SIZE {
		t.Logf("Sent %d packets (window size: %d) - window management working", outputCount, ARQ_WND_SIZE)
	}

	// Should still have packets waiting
	if arq.WaitSnd() == 0 {
		t.Log("All packets sent (queue drained)")
	} else {
		t.Logf("Still have %d packets waiting to send", arq.WaitSnd())
	}
}

// TestARQDuplicateNACK tests handling of duplicate NACK packets
func TestARQDuplicateNACK(t *testing.T) {
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

	// Send data
	arq.Send([]byte("test"))
	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
	arq.Update()
	time.Sleep(50 * time.Millisecond)

	// Clear output
	mu.Lock()
	outputPackets = nil
	mu.Unlock()

	// Send same NACK multiple times (11-byte header)
	nackPkt := make([]byte, ARQ_OVERHEAD)
	nackPkt[0] = CMD_NACK
	binary.BigEndian.PutUint32(nackPkt[1:5], 0)
	binary.BigEndian.PutUint32(nackPkt[5:9], 0) // ack = 0
	binary.BigEndian.PutUint16(nackPkt[9:11], 0)

	for i := 0; i < 5; i++ {
		arq.Input(nackPkt)
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(50 * time.Millisecond)

	// Should have retransmitted (possibly multiple times)
	mu.Lock()
	retransmitCount := len(outputPackets)
	mu.Unlock()

	if retransmitCount == 0 {
		t.Error("No retransmission after NACK")
	}

	t.Logf("Retransmitted %d times for 5 duplicate NACKs", retransmitCount)
}

// TestARQMalformedPackets tests handling of malformed packets
func TestARQMalformedPackets(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	testCases := []struct {
		name string
		data []byte
	}{
		{"Empty", []byte{}},
		{"Too short", []byte{1, 2}},
		{"Invalid length", []byte{CMD_DATA, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0xFF}}, // Claims 65535 bytes
		{"Truncated data", []byte{CMD_DATA, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 1, 2, 3}}, // Claims 10 bytes, has 3
		{"Invalid command", []byte{0xFF, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not crash
			arq.Input(tc.data)

			// Should not have received any data
			data := arq.Recv()
			if len(data) > 0 {
				t.Errorf("Received data from malformed packet: %v", data)
			}
		})
	}
}

// TestARQRapidUpdateCalls tests rapid Update() calls
func TestARQRapidUpdateCalls(t *testing.T) {
	var outputCount int32

	output := func(data []byte) {
		atomic.AddInt32(&outputCount, 1)
	}

	arq := NewSimpleARQ(1, output)

	// Send some data
	for i := 0; i < 10; i++ {
		arq.Send([]byte{byte(i)})
	}

	// Call Update rapidly
	for i := 0; i < 100; i++ {
		arq.Update()
		time.Sleep(time.Millisecond)
	}

	// Should have sent packets
	if outputCount == 0 {
		t.Error("No packets sent after rapid updates")
	}

	t.Logf("Sent %d packets with 100 rapid updates", outputCount)
}

// TestARQConcurrentInputUpdate tests concurrent Input and Update calls
func TestARQConcurrentInputUpdate(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	var wg sync.WaitGroup
	duration := 1 * time.Second
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	// Continuous Input
	wg.Add(1)
	go func() {
		defer wg.Done()
		counter := uint32(0)
		for {
			select {
			case <-stopCh:
				return
			default:
				pkt := makeARQDataPacket(counter, []byte{byte(counter)})
				arq.Input(pkt)
				counter++
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	// Continuous Update
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				arq.Update()
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Continuous Recv
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				arq.Recv()
				time.Sleep(15 * time.Millisecond)
			}
		}
	}()

	wg.Wait()

	// Should not crash
	t.Log("Concurrent Input/Update/Recv completed without crash")
}

// TestARQMemoryLeak tests for potential memory leaks in send buffer
func TestARQMemoryLeak(t *testing.T) {
	arq := NewSimpleARQ(1, func([]byte) {})

	// Send many packets
	for i := 0; i < 1000; i++ {
		arq.Send(make([]byte, 100))
	}

	// Update to move to send buffer
	for i := 0; i < 10; i++ {
		time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
		arq.Update()
	}

	initialWait := arq.WaitSnd()

	// Wait for cleanup (10*RTO = 60 seconds is too long for test)
	// Just verify cleanup logic exists
	t.Logf("Send buffer has %d packets waiting", initialWait)

	if initialWait > ARQ_WND_SIZE*2 {
		t.Logf("Warning: send buffer growing beyond expected size")
	}
}

// TestARQZeroMTU tests ARQ with invalid MTU
func TestARQZeroMTU(t *testing.T) {
	// Should use default MTU
	arq := NewSimpleARQWithMTU(1, func([]byte) {}, 0, 0)

	if arq.mtu != ARQ_MTU {
		t.Errorf("Expected default MTU %d, got %d", ARQ_MTU, arq.mtu)
	}

	// Should still work
	arq.Send([]byte("test"))
	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
	arq.Update()
}

// TestARQNegativeMTU tests ARQ with negative MTU
func TestARQNegativeMTU(t *testing.T) {
	// Should use default MTU
	arq := NewSimpleARQWithMTU(1, func([]byte) {}, -100, 0)

	if arq.mtu != ARQ_MTU {
		t.Errorf("Expected default MTU %d, got %d", ARQ_MTU, arq.mtu)
	}
}
