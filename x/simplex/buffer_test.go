package simplex

import (
	"math/rand"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSimplexBufferBasic tests basic SimplexBuffer operations
func TestSimplexBufferBasic(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	// Put packet
	pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("test"))
	err := buf.PutPacket(pkt)
	if err != nil {
		t.Fatalf("PutPacket failed: %v", err)
	}

	// Get packet
	retrieved, err := buf.GetPacket()
	if err != nil {
		t.Fatalf("GetPacket failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetPacket returned nil")
	}

	if string(retrieved.Data) != "test" {
		t.Fatalf("Data mismatch: got %q, expected %q", retrieved.Data, "test")
	}
}

// TestSimplexBufferPriority tests that CTRL packets have priority over DATA
func TestSimplexBufferPriority(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	// Put DATA packet first
	buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte("data")))

	// Put CTRL packet second
	buf.PutPacket(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("ctrl")))

	// Get should return CTRL packet first (priority)
	pkt, _ := buf.GetPacket()
	if pkt.PacketType != SimplexPacketTypeCTRL {
		t.Fatal("CTRL packet should have priority over DATA packet")
	}

	// Next Get should return DATA packet
	pkt, _ = buf.GetPacket()
	if pkt.PacketType != SimplexPacketTypeDATA {
		t.Fatal("Expected DATA packet after CTRL")
	}
}

// TestSimplexBufferGetPackets tests GetPackets with size limit
func TestSimplexBufferGetPackets(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 50, // Small size to test limit
	}

	buf := NewSimplexBuffer(addr)

	// Put multiple packets - each packet with "data" is 5+4=9 bytes
	// So 50/9 = 5 packets can fit
	for i := 0; i < 10; i++ {
		buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte("data")))
	}

	// GetPackets should respect maxBodySize
	pkts, err := buf.GetPackets()
	if err != nil {
		t.Fatalf("GetPackets failed: %v", err)
	}

	// Should not return all packets due to size limit
	if len(pkts.Packets) >= 10 {
		t.Fatalf("GetPackets should respect maxBodySize limit, got all %d packets", len(pkts.Packets))
	}

	// Should return at least some packets
	if len(pkts.Packets) == 0 {
		t.Fatal("GetPackets should return at least some packets")
	}

	// Total size should not exceed maxBodySize
	if pkts.Size() > addr.maxBodySize {
		t.Fatalf("GetPackets size %d exceeds maxBodySize %d", pkts.Size(), addr.maxBodySize)
	}

	t.Logf("GetPackets returned %d packets with total size %d (max %d)", len(pkts.Packets), pkts.Size(), addr.maxBodySize)
}

// TestSimplexBufferPutPackets tests PutPackets
func TestSimplexBufferPutPackets(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	pkts := NewSimplexPackets()
	pkts.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("pkt1")))
	pkts.Append(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("pkt2")))

	err := buf.PutPackets(pkts)
	if err != nil {
		t.Fatalf("PutPackets failed: %v", err)
	}

	// Should be able to retrieve both packets
	pkt1, _ := buf.GetPacket()
	pkt2, _ := buf.GetPacket()

	if pkt1 == nil || pkt2 == nil {
		t.Fatal("Failed to retrieve packets after PutPackets")
	}
}

// TestSimplexBufferRecvPutGet tests packet-boundary-preserving receive buffer
func TestSimplexBufferRecvPutGet(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 65000,
	}

	buf := NewSimplexBuffer(addr)

	// Put packets of different sizes (simulating ARQ segments)
	small := []byte("hello")
	large := make([]byte, 64000)
	for i := range large {
		large[i] = byte(i % 256)
	}

	if err := buf.RecvPut(small); err != nil {
		t.Fatalf("RecvPut small failed: %v", err)
	}
	if err := buf.RecvPut(large); err != nil {
		t.Fatalf("RecvPut large failed: %v", err)
	}

	// Get should return complete packets with boundaries preserved
	got1, err := buf.RecvGet()
	if err != nil {
		t.Fatalf("RecvGet 1 failed: %v", err)
	}
	if string(got1) != "hello" {
		t.Fatalf("Packet 1: got %q, expected %q", got1, "hello")
	}

	got2, err := buf.RecvGet()
	if err != nil {
		t.Fatalf("RecvGet 2 failed: %v", err)
	}
	if len(got2) != 64000 {
		t.Fatalf("Packet 2: got len=%d, expected 64000", len(got2))
	}
	if got2[0] != 0 || got2[255] != 255 {
		t.Fatal("Packet 2: data corruption")
	}

	// Empty: should return nil, nil
	got3, err := buf.RecvGet()
	if err != nil || got3 != nil {
		t.Fatalf("RecvGet empty: got (%v, %v), expected (nil, nil)", got3, err)
	}
}

func TestSimplexBufferBackpressureNoDrop(t *testing.T) {
	oldCtrlCap := defaultSimplexCtrlChannelCapacity
	oldSendCap := defaultSimplexSendChannelCapacity
	defaultSimplexCtrlChannelCapacity = 2
	defaultSimplexSendChannelCapacity = 2
	t.Cleanup(func() {
		defaultSimplexCtrlChannelCapacity = oldCtrlCap
		defaultSimplexSendChannelCapacity = oldSendCap
	})

	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 4096,
	}
	buf := NewSimplexBuffer(addr)

	const totalPackets = 64
	done := make(chan []int, 1)
	go func() {
		seen := make([]int, 0, totalPackets)
		for len(seen) < totalPackets {
			pkt, err := buf.GetPacket()
			if err != nil {
				t.Errorf("GetPacket: %v", err)
				return
			}
			if pkt == nil {
				time.Sleep(time.Millisecond)
				continue
			}
			seen = append(seen, int(pkt.Data[0]))
			time.Sleep(2 * time.Millisecond)
		}
		done <- seen
	}()

	for i := 0; i < totalPackets; i++ {
		pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(i)})
		if err := buf.PutPacket(pkt); err != nil {
			t.Fatalf("PutPacket %d: %v", i, err)
		}
	}

	select {
	case seen := <-done:
		if len(seen) != totalPackets {
			t.Fatalf("received %d packets, want %d", len(seen), totalPackets)
		}
		for i, got := range seen {
			if got != i {
				t.Fatalf("packet order mismatch at %d: got %d want %d", i, got, i)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for backpressured packets")
	}
}

func TestSimplexBufferRecvBackpressureNoDrop(t *testing.T) {
	oldRecvCap := defaultSimplexRecvChannelCapacity
	defaultSimplexRecvChannelCapacity = 2
	t.Cleanup(func() {
		defaultSimplexRecvChannelCapacity = oldRecvCap
	})

	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 4096,
	}
	buf := NewSimplexBuffer(addr)

	const totalPackets = 32
	done := make(chan [][]byte, 1)
	go func() {
		seen := make([][]byte, 0, totalPackets)
		for len(seen) < totalPackets {
			pkt, err := buf.RecvGet()
			if err != nil {
				t.Errorf("RecvGet: %v", err)
				return
			}
			if pkt == nil {
				time.Sleep(time.Millisecond)
				continue
			}
			copyPkt := append([]byte(nil), pkt...)
			seen = append(seen, copyPkt)
			time.Sleep(2 * time.Millisecond)
		}
		done <- seen
	}()

	for i := 0; i < totalPackets; i++ {
		payload := []byte{byte(i), byte(i + 1), byte(i + 2)}
		if err := buf.RecvPut(payload); err != nil {
			t.Fatalf("RecvPut %d: %v", i, err)
		}
	}

	select {
	case seen := <-done:
		if len(seen) != totalPackets {
			t.Fatalf("received %d packets, want %d", len(seen), totalPackets)
		}
		for i, got := range seen {
			if len(got) != 3 || got[0] != byte(i) || got[1] != byte(i+1) || got[2] != byte(i+2) {
				t.Fatalf("packet mismatch at %d: got %v", i, got)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for recv backpressure packets")
	}
}

// TestSimplexBufferAddr tests Addr method
func TestSimplexBufferAddr(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	if buf.Addr() != addr {
		t.Fatal("Addr() returned different address")
	}
}

// TestAsymBufferBasic tests AsymBuffer creation and access
func TestAsymBufferBasic(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	asymBuf := NewAsymBuffer(addr)
	defer asymBuf.Close()

	if asymBuf.Addr() != addr {
		t.Fatal("AsymBuffer Addr() returned different address")
	}

	if asymBuf.ReadBuf() == nil {
		t.Fatal("ReadBuf() returned nil")
	}

	if asymBuf.WriteBuf() == nil {
		t.Fatal("WriteBuf() returned nil")
	}

	// Verify read and write buffers are different
	if asymBuf.ReadBuf() == asymBuf.WriteBuf() {
		t.Fatal("ReadBuf and WriteBuf should be different instances")
	}
}

// TestAsymBufferSeparateChannels tests that read and write buffers are independent
func TestAsymBufferSeparateChannels(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	asymBuf := NewAsymBuffer(addr)
	defer asymBuf.Close()

	// Put packet in write buffer
	writePkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("write"))
	asymBuf.WriteBuf().PutPacket(writePkt)

	// Put packet in read buffer
	readPkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("read"))
	asymBuf.ReadBuf().PutPacket(readPkt)

	// Get from write buffer should return write packet
	retrieved, _ := asymBuf.WriteBuf().GetPacket()
	if string(retrieved.Data) != "write" {
		t.Fatal("WriteBuf returned wrong packet")
	}

	// Get from read buffer should return read packet
	retrieved, _ = asymBuf.ReadBuf().GetPacket()
	if string(retrieved.Data) != "read" {
		t.Fatal("ReadBuf returned wrong packet")
	}
}

// TestSimplexBufferEmpty tests operations on empty buffer
func TestSimplexBufferEmpty(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	// GetPacket from empty buffer
	pkt, err := buf.GetPacket()
	if err != nil {
		t.Fatalf("GetPacket from empty buffer should not error, got: %v", err)
	}
	if pkt != nil {
		t.Fatal("GetPacket from empty buffer should return nil")
	}

	// GetPackets from empty buffer
	pkts, err := buf.GetPackets()
	if err != nil {
		t.Fatalf("GetPackets from empty buffer should not error, got: %v", err)
	}
	if len(pkts.Packets) != 0 {
		t.Fatal("GetPackets from empty buffer should return empty collection")
	}
}

// TestSimplexBufferNilPacket tests handling of nil packet
func TestSimplexBufferNilPacket(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	// PutPacket with nil should not error
	err := buf.PutPacket(nil)
	if err != nil {
		t.Fatalf("PutPacket(nil) should not error, got: %v", err)
	}

	// Buffer should still be empty
	pkt, _ := buf.GetPacket()
	if pkt != nil {
		t.Fatal("Buffer should be empty after PutPacket(nil)")
	}
}

// TestSimplexAddrMethods tests SimplexAddr methods
func TestSimplexAddrMethods(t *testing.T) {
	u, _ := url.Parse("test://localhost:8080/path?interval=100")
	addr := &SimplexAddr{
		URL:         u,
		id:          "test-id",
		interval:    100 * time.Millisecond,
		maxBodySize: 1000,
		options:     u.Query(),
	}

	if addr.Network() != "test" {
		t.Fatalf("Network() returned %q, expected %q", addr.Network(), "test")
	}

	if addr.ID() != "test-id" {
		t.Fatalf("ID() returned %q, expected %q", addr.ID(), "test-id")
	}

	if addr.Interval() != 100*time.Millisecond {
		t.Fatalf("Interval() returned %v, expected %v", addr.Interval(), 100*time.Millisecond)
	}

	// MaxBodySize() returns maxBodySize - 5
	if addr.MaxBodySize() != 995 {
		t.Fatalf("MaxBodySize() returned %d, expected %d", addr.MaxBodySize(), 995)
	}
}

// TestSimplexAddrConfig tests Config get/set
func TestSimplexAddrConfig(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	// Initially nil
	if addr.Config() != nil {
		t.Fatal("Initial Config() should be nil")
	}

	// Set config
	type testConfig struct {
		Value string
	}
	cfg := &testConfig{Value: "test"}
	addr.SetConfig(cfg)

	// Get config
	retrieved := addr.Config()
	if retrieved == nil {
		t.Fatal("Config() returned nil after SetConfig")
	}

	retrievedCfg, ok := retrieved.(*testConfig)
	if !ok {
		t.Fatal("Config() returned wrong type")
	}

	if retrievedCfg.Value != "test" {
		t.Fatalf("Config value mismatch: got %q, expected %q", retrievedCfg.Value, "test")
	}
}

// TestSimplexAddrSetOption tests dynamic option updates via SetOption.
func TestSimplexAddrSetOption(t *testing.T) {
	u, _ := url.Parse("test://localhost:8080/path?interval=100")
	addr := &SimplexAddr{
		URL:         u,
		id:          "test-id",
		interval:    100 * time.Millisecond,
		maxBodySize: 1000,
		options:     u.Query(),
	}

	// Verify initial state
	if addr.Interval() != 100*time.Millisecond {
		t.Fatalf("initial Interval() = %v, want 100ms", addr.Interval())
	}
	if addr.GetOption("interval") != "100" {
		t.Fatalf("initial GetOption(interval) = %q, want '100'", addr.GetOption("interval"))
	}

	// Update interval via SetOption
	addr.SetOption("interval", "5000")
	if addr.Interval() != 5*time.Second {
		t.Fatalf("after SetOption, Interval() = %v, want 5s", addr.Interval())
	}
	if addr.GetOption("interval") != "5000" {
		t.Fatalf("after SetOption, GetOption(interval) = %q, want '5000'", addr.GetOption("interval"))
	}

	// Update a non-interval option
	addr.SetOption("custom_key", "custom_value")
	if addr.GetOption("custom_key") != "custom_value" {
		t.Fatalf("GetOption(custom_key) = %q, want 'custom_value'", addr.GetOption("custom_key"))
	}
	// interval should be unchanged
	if addr.Interval() != 5*time.Second {
		t.Fatalf("interval changed after setting unrelated option: %v", addr.Interval())
	}

	// Invalid interval value should not change interval
	addr.SetOption("interval", "invalid")
	if addr.Interval() != 5*time.Second {
		t.Fatalf("interval changed after invalid SetOption: %v", addr.Interval())
	}
}

// TestSimplexAddrSetOptionConcurrent tests that SetOption and Interval are safe under concurrent access.
func TestSimplexAddrSetOptionConcurrent(t *testing.T) {
	u, _ := url.Parse("test://localhost:8080/path?interval=100")
	addr := &SimplexAddr{
		URL:         u,
		id:          "test-id",
		interval:    100 * time.Millisecond,
		maxBodySize: 1000,
		options:     u.Query(),
	}

	done := make(chan struct{})
	// Writer goroutine
	go func() {
		for i := 0; i < 1000; i++ {
			addr.SetOption("interval", "200")
			addr.SetOption("interval", "300")
		}
		close(done)
	}()

	// Reader goroutine — must not panic or race
	for i := 0; i < 1000; i++ {
		iv := addr.Interval()
		if iv != 100*time.Millisecond && iv != 200*time.Millisecond && iv != 300*time.Millisecond {
			t.Fatalf("unexpected interval: %v", iv)
		}
		_ = addr.GetOption("interval")
	}
	<-done
}

// TestSimplexAddrStringConcurrent verifies that rendering an address does not
// mutate the shared URL while options are being updated.
func TestSimplexAddrStringConcurrent(t *testing.T) {
	u, _ := url.Parse("test://localhost:8080/path?client_id=initial")
	addr := &SimplexAddr{
		URL:         u,
		id:          "test-id",
		interval:    100 * time.Millisecond,
		maxBodySize: 1000,
		options:     u.Query(),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			addr.SetOption("client_id", "client-a")
			addr.SetOption("client_id", "client-b")
		}
	}()

	for i := 0; i < 1000; i++ {
		if got := addr.String(); got == "" {
			t.Fatal("String returned an empty address")
		}
	}
	<-done
}

// ---------------------------------------------------------------------------
// Stress / concurrency tests (merged from stress_test.go)
// ---------------------------------------------------------------------------

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
					op := rng.Intn(4)
					switch op {
					case 0: // PutPacket
						pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte{byte(rng.Intn(256))})
						buf.PutPacket(pkt)
					case 1: // GetPacket
						buf.GetPacket()
					case 2: // GetPackets
						buf.GetPackets()
					case 3: // PutPackets
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
