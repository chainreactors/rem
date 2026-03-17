package simplex

import (
	"net/url"
	"testing"
	"time"
)

// TestPeekableChannelBasic tests basic Put/Get/Peek operations
func TestPeekableChannelBasic(t *testing.T) {
	ch := NewPeekableChannel(5)
	defer ch.Close()

	pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("test"))

	// Put packet
	err := ch.Put(pkt)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get packet
	retrieved, err := ch.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("Get returned nil packet")
	}

	if retrieved.PacketType != pkt.PacketType {
		t.Fatalf("PacketType mismatch: got %d, expected %d", retrieved.PacketType, pkt.PacketType)
	}
}

// TestPeekableChannelPeek tests Peek operation
func TestPeekableChannelPeek(t *testing.T) {
	ch := NewPeekableChannel(5)
	defer ch.Close()

	pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("test"))
	ch.Put(pkt)

	// Peek should return packet without removing it
	peeked, err := ch.Peek()
	if err != nil {
		t.Fatalf("Peek failed: %v", err)
	}

	if peeked == nil {
		t.Fatal("Peek returned nil")
	}

	// Peek again should return same packet
	peeked2, err := ch.Peek()
	if err != nil {
		t.Fatalf("Second Peek failed: %v", err)
	}

	if peeked != peeked2 {
		t.Fatal("Second Peek returned different packet")
	}

	// Get should return the peeked packet
	retrieved, err := ch.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved != peeked {
		t.Fatal("Get returned different packet than Peek")
	}

	// Channel should now be empty
	empty, _ := ch.Get()
	if empty != nil {
		t.Fatal("Channel should be empty after Get")
	}
}

// TestPeekableChannelEmpty tests operations on empty channel
func TestPeekableChannelEmpty(t *testing.T) {
	ch := NewPeekableChannel(5)
	defer ch.Close()

	// Get from empty channel
	pkt, err := ch.Get()
	if err != nil {
		t.Fatalf("Get from empty channel should not error, got: %v", err)
	}
	if pkt != nil {
		t.Fatal("Get from empty channel should return nil")
	}

	// Peek from empty channel
	pkt, err = ch.Peek()
	if err != nil {
		t.Fatalf("Peek from empty channel should not error, got: %v", err)
	}
	if pkt != nil {
		t.Fatal("Peek from empty channel should return nil")
	}
}

// TestPeekableChannelFull tests Put on full channel
func TestPeekableChannelFull(t *testing.T) {
	ch := NewPeekableChannel(2)
	defer ch.Close()

	// Fill channel
	ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("1")))
	ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("2")))

	// Try to put more - should error
	err := ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("3")))
	if err == nil {
		t.Fatal("Put to full channel should return error")
	}
}

// TestPeekableChannelSize tests Size method
func TestPeekableChannelSize(t *testing.T) {
	ch := NewPeekableChannel(5)
	defer ch.Close()

	if ch.Size() != 0 {
		t.Fatalf("Initial size should be 0, got %d", ch.Size())
	}

	ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("1")))
	if ch.Size() != 1 {
		t.Fatalf("Size after Put should be 1, got %d", ch.Size())
	}

	ch.Peek()
	if ch.Size() != 1 {
		t.Fatalf("Size after Peek should still be 1, got %d", ch.Size())
	}

	ch.Get()
	if ch.Size() != 0 {
		t.Fatalf("Size after Get should be 0, got %d", ch.Size())
	}
}

// TestPeekableChannelClose tests Close operation
func TestPeekableChannelClose(t *testing.T) {
	ch := NewPeekableChannel(5)

	ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("test")))
	ch.Close()

	// Operations after close should error
	err := ch.Put(NewSimplexPacket(SimplexPacketTypeDATA, []byte("test")))
	if err == nil {
		t.Fatal("Put after Close should return error")
	}

	_, err = ch.Get()
	if err == nil {
		t.Fatal("Get after Close should return error")
	}

	_, err = ch.Peek()
	if err == nil {
		t.Fatal("Peek after Close should return error")
	}
}

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

// TestSimplexBufferPeek tests Peek operation
func TestSimplexBufferPeek(t *testing.T) {
	addr := &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "localhost"},
		maxBodySize: 1000,
	}

	buf := NewSimplexBuffer(addr)

	pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("test"))
	buf.PutPacket(pkt)

	// Peek should not remove packet
	peeked, _ := buf.Peek()
	if peeked == nil {
		t.Fatal("Peek returned nil")
	}

	// Get should return same packet
	retrieved, _ := buf.GetPacket()
	if string(retrieved.Data) != string(peeked.Data) {
		t.Fatal("Get returned different packet than Peek")
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

	// Peek from empty buffer
	pkt, err = buf.Peek()
	if err != nil {
		t.Fatalf("Peek from empty buffer should not error, got: %v", err)
	}
	if pkt != nil {
		t.Fatal("Peek from empty buffer should return nil")
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
