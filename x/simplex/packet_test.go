package simplex

import (
	"bytes"
	"testing"
)

// TestSimplexPacketBasic tests basic packet creation and marshaling
func TestSimplexPacketBasic(t *testing.T) {
	data := []byte("test data")
	pkt := NewSimplexPacket(SimplexPacketTypeDATA, data)

	if pkt.PacketType != SimplexPacketTypeDATA {
		t.Fatalf("Expected packet type DATA, got %d", pkt.PacketType)
	}

	if !bytes.Equal(pkt.Data, data) {
		t.Fatalf("Packet data mismatch: got %v, expected %v", pkt.Data, data)
	}

	// Test size calculation
	expectedSize := 5 + len(data) // Type(1) + Length(4) + Data
	if pkt.Size() != expectedSize {
		t.Fatalf("Expected size %d, got %d", expectedSize, pkt.Size())
	}
}

// TestSimplexPacketMarshalParse tests marshal and parse round-trip
func TestSimplexPacketMarshalParse(t *testing.T) {
	testCases := []struct {
		name       string
		packetType SimplexPacketType
		data       []byte
	}{
		{"DATA packet", SimplexPacketTypeDATA, []byte("hello world")},
		{"CTRL packet", SimplexPacketTypeCTRL, []byte("control")},
		{"Empty data", SimplexPacketTypeDATA, []byte{}},
		{"Large data", SimplexPacketTypeDATA, make([]byte, 1000)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			original := NewSimplexPacket(tc.packetType, tc.data)
			marshaled := original.Marshal()

			parsed, err := ParseSimplexPacket(marshaled)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}

			if parsed.PacketType != original.PacketType {
				t.Fatalf("PacketType mismatch: got %d, expected %d", parsed.PacketType, original.PacketType)
			}

			if !bytes.Equal(parsed.Data, original.Data) {
				t.Fatalf("Data mismatch: got %v, expected %v", parsed.Data, original.Data)
			}
		})
	}
}

// TestSimplexPacketParseInvalid tests parsing invalid data
func TestSimplexPacketParseInvalid(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{"Empty data", []byte{}},
		{"Too short", []byte{1, 2, 3}},
		{"Incomplete header", []byte{0x36, 0x00, 0x00}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pkt, err := ParseSimplexPacket(tc.data)
			// Should not error but return empty packet
			if err != nil {
				t.Fatalf("Parse should not error on invalid data, got: %v", err)
			}
			if pkt == nil {
				t.Fatal("Parse should return non-nil packet")
			}
		})
	}
}

// TestSimplexPacketParseTruncated tests parsing when length exceeds available data
func TestSimplexPacketParseTruncated(t *testing.T) {
	// Create a packet header claiming 100 bytes but only provide 10
	data := make([]byte, 15)
	data[0] = byte(SimplexPacketTypeDATA)
	// Length = 100 (but we only have 10 bytes after header)
	data[1] = 100
	data[2] = 0
	data[3] = 0
	data[4] = 0

	pkt, err := ParseSimplexPacket(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Should return empty packet when data is truncated
	if len(pkt.Data) != 0 {
		t.Fatalf("Expected empty data for truncated packet, got %d bytes", len(pkt.Data))
	}
}

// TestSimplexPacketsBasic tests SimplexPackets collection
func TestSimplexPacketsBasic(t *testing.T) {
	pkts := NewSimplexPackets()

	if len(pkts.Packets) != 0 {
		t.Fatalf("New SimplexPackets should be empty, got %d packets", len(pkts.Packets))
	}

	if pkts.Size() != 0 {
		t.Fatalf("Empty SimplexPackets size should be 0, got %d", pkts.Size())
	}

	// Append packets
	pkt1 := NewSimplexPacket(SimplexPacketTypeDATA, []byte("packet1"))
	pkt2 := NewSimplexPacket(SimplexPacketTypeCTRL, []byte("packet2"))

	pkts.Append(pkt1)
	pkts.Append(pkt2)

	if len(pkts.Packets) != 2 {
		t.Fatalf("Expected 2 packets, got %d", len(pkts.Packets))
	}

	expectedSize := pkt1.Size() + pkt2.Size()
	if pkts.Size() != expectedSize {
		t.Fatalf("Expected size %d, got %d", expectedSize, pkts.Size())
	}
}

// TestSimplexPacketsMarshalParse tests marshal and parse of packet collection
func TestSimplexPacketsMarshalParse(t *testing.T) {
	original := NewSimplexPackets()
	original.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("first")))
	original.Append(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("second")))
	original.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("third")))

	marshaled := original.Marshal()

	parsed, err := ParseSimplexPackets(marshaled)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(parsed.Packets) != len(original.Packets) {
		t.Fatalf("Packet count mismatch: got %d, expected %d", len(parsed.Packets), len(original.Packets))
	}

	for i := range original.Packets {
		if parsed.Packets[i].PacketType != original.Packets[i].PacketType {
			t.Fatalf("Packet %d type mismatch: got %d, expected %d", i, parsed.Packets[i].PacketType, original.Packets[i].PacketType)
		}
		if !bytes.Equal(parsed.Packets[i].Data, original.Packets[i].Data) {
			t.Fatalf("Packet %d data mismatch", i)
		}
	}
}

// TestSimplexPacketsEmpty tests empty packet collection
func TestSimplexPacketsEmpty(t *testing.T) {
	pkts := NewSimplexPackets()
	marshaled := pkts.Marshal()

	if len(marshaled) != 0 {
		t.Fatalf("Empty SimplexPackets should marshal to empty bytes, got %d bytes", len(marshaled))
	}

	parsed, err := ParseSimplexPackets(marshaled)
	if err != nil {
		t.Fatalf("Parse empty packets failed: %v", err)
	}

	if len(parsed.Packets) != 0 {
		t.Fatalf("Parsed empty packets should have 0 packets, got %d", len(parsed.Packets))
	}
}

// TestNewSimplexPacketWithMaxSize tests packet splitting with max size
func TestNewSimplexPacketWithMaxSize(t *testing.T) {
	data := make([]byte, 250)
	for i := range data {
		data[i] = byte(i)
	}

	maxSize := 100
	pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeDATA, maxSize)

	// Should split into multiple packets
	if len(pkts.Packets) < 3 {
		t.Fatalf("Expected at least 3 packets for 250 bytes with max 100, got %d", len(pkts.Packets))
	}

	// Verify each packet is within size limit
	for i, pkt := range pkts.Packets {
		if pkt.Size() > maxSize {
			t.Fatalf("Packet %d size %d exceeds max %d", i, pkt.Size(), maxSize)
		}
	}

	// Verify all packets are DATA type
	for i, pkt := range pkts.Packets {
		if pkt.PacketType != SimplexPacketTypeDATA {
			t.Fatalf("Packet %d has wrong type: %d", i, pkt.PacketType)
		}
	}

	// Verify data integrity by reassembling
	var reassembled []byte
	for _, pkt := range pkts.Packets {
		reassembled = append(reassembled, pkt.Data...)
	}

	if !bytes.Equal(reassembled, data) {
		t.Fatal("Reassembled data does not match original")
	}
}

// TestNewSimplexPacketWithMaxSizeSmallData tests splitting with data smaller than max
func TestNewSimplexPacketWithMaxSizeSmallData(t *testing.T) {
	data := []byte("small")
	maxSize := 1000

	pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeCTRL, maxSize)

	if len(pkts.Packets) != 1 {
		t.Fatalf("Expected 1 packet for small data, got %d", len(pkts.Packets))
	}

	if !bytes.Equal(pkts.Packets[0].Data, data) {
		t.Fatal("Packet data does not match original")
	}

	if pkts.Packets[0].PacketType != SimplexPacketTypeCTRL {
		t.Fatalf("Expected CTRL packet type, got %d", pkts.Packets[0].PacketType)
	}
}

// TestNewSimplexPacketWithMaxSizeEmpty tests splitting empty data
func TestNewSimplexPacketWithMaxSizeEmpty(t *testing.T) {
	data := []byte{}
	maxSize := 100

	pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeDATA, maxSize)

	if len(pkts.Packets) != 0 {
		t.Fatalf("Expected 0 packets for empty data, got %d", len(pkts.Packets))
	}
}

// TestSimplexPacketTypeValues tests packet type constants
func TestSimplexPacketTypeValues(t *testing.T) {
	if SimplexPacketTypeCTRL != 0x96 {
		t.Fatalf("SimplexPacketTypeCTRL should be 0x96, got 0x%x", SimplexPacketTypeCTRL)
	}

	if SimplexPacketTypeDATA != 0x36 {
		t.Fatalf("SimplexPacketTypeDATA should be 0x36, got 0x%x", SimplexPacketTypeDATA)
	}
}

// TestSimplexPacketsMarshalSize tests that marshaled size matches Size()
func TestSimplexPacketsMarshalSize(t *testing.T) {
	pkts := NewSimplexPackets()
	pkts.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("test1")))
	pkts.Append(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("test2")))

	marshaled := pkts.Marshal()
	size := pkts.Size()

	if len(marshaled) != size {
		t.Fatalf("Marshaled length %d does not match Size() %d", len(marshaled), size)
	}
}

// TestSimplexPacketsParsePartial tests parsing when data ends mid-packet
func TestSimplexPacketsParsePartial(t *testing.T) {
	// Create valid packets
	pkts := NewSimplexPackets()
	pkts.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("complete")))
	pkts.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("another")))
	marshaled := pkts.Marshal()

	// Truncate the marshaled data to cut off the second packet
	truncated := marshaled[:len(marshaled)-5]

	// Should error on incomplete packet
	parsed, err := ParseSimplexPackets(truncated)
	if err == nil {
		t.Fatal("Expected error when parsing truncated data")
	}

	// Verify we got at least the first complete packet before the error
	if parsed != nil && len(parsed.Packets) > 0 {
		t.Logf("Parsed %d complete packets before error", len(parsed.Packets))
	}
}
