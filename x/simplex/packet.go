package simplex

import (
	"encoding/binary"
	"fmt"
)

// SimplexPacketType 定义数据包类型
type SimplexPacketType uint8

const (
	SimplexPacketTypeCTRL SimplexPacketType = 0x96 // 控制包
	SimplexPacketTypeDATA SimplexPacketType = 0x36 // 数据包
	SimplexPacketTypeRST  SimplexPacketType = 0xFE // server 重启通知
)

// SimplexPacket 定义单个数据包结构体
type SimplexPacket struct {
	PacketType SimplexPacketType
	Data       []byte
}

// NewSimplexPacket 创建新的数据包
func NewSimplexPacket(packetType SimplexPacketType, data []byte) *SimplexPacket {
	return &SimplexPacket{
		PacketType: packetType,
		Data:       data,
	}
}

func ParseSimplexPacket(data []byte) (*SimplexPacket, error) {
	packet := &SimplexPacket{}
	err := packet.Parse(data)
	if err != nil {
		return nil, err
	}
	return packet, nil
}

// Size 返回序列化后的大小
func (sp *SimplexPacket) Size() int {
	// TLV格式: Type(1) + Length(4) + Value(len(data))
	return 5 + len(sp.Data)
}

func (sp *SimplexPacket) Parse(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	// 只解析第一个包
	if len(data) < 5 {
		return nil
	}
	packetType := SimplexPacketType(data[0])
	length := binary.LittleEndian.Uint32(data[1:5])
	if int(length)+5 > len(data) {
		return nil
	}
	sp.PacketType = packetType
	sp.Data = data[5 : 5+length]
	return nil
}

// Marshal 序列化数据包
func (sp *SimplexPacket) Marshal() []byte {
	size := sp.Size()
	buf := make([]byte, size)

	// Type (1 byte)
	buf[0] = uint8(sp.PacketType)

	// Length (4 bytes)
	binary.LittleEndian.PutUint32(buf[1:5], uint32(len(sp.Data)))

	// Value
	if len(sp.Data) > 0 {
		copy(buf[5:], sp.Data)
	}

	return buf
}

func ParseSimplexPackets(data []byte) (*SimplexPackets, error) {
	packets := &SimplexPackets{}
	err := packets.Parse(data)
	if err != nil {
		return nil, err
	}
	return packets, nil
}

// SimplexPackets 定义数据包集合结构体
type SimplexPackets struct {
	Packets []*SimplexPacket
}

// NewSimplexPackets 创建新的数据包集合
func NewSimplexPackets() *SimplexPackets {
	return &SimplexPackets{
		Packets: make([]*SimplexPacket, 0),
	}
}

func NewSimplexPacketWithMaxSize(data []byte, t SimplexPacketType, maxSize int) *SimplexPackets {
	pkts := NewSimplexPackets()
	maxSize = maxSize - 5
	for len(data) > 0 {
		size := len(data)
		if size > maxSize {
			size = maxSize
		}
		pkt := NewSimplexPacket(t, data[:size])
		pkts.Append(pkt)
		data = data[size:]
	}
	return pkts
}

func (sps *SimplexPackets) Append(pkt *SimplexPacket) {
	sps.Packets = append(sps.Packets, pkt)
}

// Size 返回序列化后的大小
func (sps *SimplexPackets) Size() int {
	totalSize := 0
	for _, packet := range sps.Packets {
		totalSize += packet.Size()
	}
	return totalSize
}

// Marshal 序列化所有数据包
func (sps *SimplexPackets) Marshal() []byte {
	totalSize := sps.Size()
	buf := make([]byte, totalSize)
	offset := 0

	for _, packet := range sps.Packets {
		data := packet.Marshal()
		copy(buf[offset:], data)
		offset += len(data)
	}

	return buf
}

// Parse 从字节数据反序列化数据包集合
func (sps *SimplexPackets) Parse(data []byte) error {
	sps.Packets = make([]*SimplexPacket, 0)
	offset := 0

	for offset < len(data) {
		if offset+5 > len(data) {
			return fmt.Errorf("data too short for packet header")
		}

		pkt, err := ParseSimplexPacket(data[offset:])
		if err != nil {
			return err
		}
		sps.Append(pkt)

		offset += pkt.Size()
	}

	return nil
}
