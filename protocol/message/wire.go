package message

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

// Protobuf wire types
const (
	wireVarint = 0
	wireBytes  = 2
)

// --- Encoding ---

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendTag(b []byte, fieldNum uint32, wireType uint8) []byte {
	return appendVarint(b, uint64(fieldNum)<<3|uint64(wireType))
}

func appendString(b []byte, fieldNum uint32, s string) []byte {
	if s == "" {
		return b
	}
	b = appendTag(b, fieldNum, wireBytes)
	b = appendVarint(b, uint64(len(s)))
	return append(b, s...)
}

func appendRawBytes(b []byte, fieldNum uint32, data []byte) []byte {
	if len(data) == 0 {
		return b
	}
	b = appendTag(b, fieldNum, wireBytes)
	b = appendVarint(b, uint64(len(data)))
	return append(b, data...)
}

func appendBool(b []byte, fieldNum uint32, v bool) []byte {
	if !v {
		return b
	}
	b = appendTag(b, fieldNum, wireVarint)
	return append(b, 1)
}

func appendInt32(b []byte, fieldNum uint32, v int32) []byte {
	if v == 0 {
		return b
	}
	b = appendTag(b, fieldNum, wireVarint)
	return appendVarint(b, uint64(v))
}

func appendUint64Field(b []byte, fieldNum uint32, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = appendTag(b, fieldNum, wireVarint)
	return appendVarint(b, v)
}

func appendInt64Field(b []byte, fieldNum uint32, v int64) []byte {
	if v == 0 {
		return b
	}
	b = appendTag(b, fieldNum, wireVarint)
	return appendVarint(b, uint64(v))
}

func appendMessageField(b []byte, fieldNum uint32, msg Message) []byte {
	if msg == nil {
		return b
	}
	size := msg.SizeVT()
	if size == 0 {
		return b
	}
	b = appendTag(b, fieldNum, wireBytes)
	b = appendVarint(b, uint64(size))
	data, _ := msg.MarshalVT()
	return append(b, data...)
}

// --- Decoding ---

func consumeVarint(b []byte) (uint64, int) {
	var v uint64
	for i, c := range b {
		if i >= binary.MaxVarintLen64 {
			return 0, -1
		}
		v |= uint64(c&0x7f) << (i * 7)
		if c < 0x80 {
			return v, i + 1
		}
	}
	return 0, -1
}

func consumeTag(b []byte) (fieldNum uint32, wireType uint8, n int) {
	v, n := consumeVarint(b)
	if n < 0 {
		return 0, 0, -1
	}
	return uint32(v >> 3), uint8(v & 0x7), n
}

func consumeBytes(b []byte) ([]byte, int) {
	length, n := consumeVarint(b)
	if n < 0 {
		return nil, -1
	}
	if uint64(len(b)-n) < length {
		return nil, -1
	}
	return b[n : n+int(length)], n + int(length)
}

func skipField(b []byte, wireType uint8) int {
	switch wireType {
	case wireVarint:
		_, n := consumeVarint(b)
		return n
	case 1: // 64-bit
		if len(b) < 8 {
			return -1
		}
		return 8
	case wireBytes:
		_, n := consumeBytes(b)
		return n
	case 5: // 32-bit
		if len(b) < 4 {
			return -1
		}
		return 4
	default:
		return -1
	}
}

// --- Size helpers ---

func sizeVarint(v uint64) int {
	return (bits.Len64(v|1) + 6) / 7
}

func sizeTag(fieldNum uint32) int {
	return sizeVarint(uint64(fieldNum) << 3)
}

func sizeString(fieldNum uint32, s string) int {
	if s == "" {
		return 0
	}
	return sizeTag(fieldNum) + sizeVarint(uint64(len(s))) + len(s)
}

func sizeRawBytes(fieldNum uint32, data []byte) int {
	if len(data) == 0 {
		return 0
	}
	return sizeTag(fieldNum) + sizeVarint(uint64(len(data))) + len(data)
}

func sizeBool(fieldNum uint32, v bool) int {
	if !v {
		return 0
	}
	return sizeTag(fieldNum) + 1
}

func sizeInt32(fieldNum uint32, v int32) int {
	if v == 0 {
		return 0
	}
	return sizeTag(fieldNum) + sizeVarint(uint64(v))
}

func sizeUint64Field(fieldNum uint32, v uint64) int {
	if v == 0 {
		return 0
	}
	return sizeTag(fieldNum) + sizeVarint(v)
}

func sizeInt64Field(fieldNum uint32, v int64) int {
	if v == 0 {
		return 0
	}
	return sizeTag(fieldNum) + sizeVarint(uint64(v))
}

func sizeMessageField(fieldNum uint32, msg Message) int {
	if msg == nil {
		return 0
	}
	s := msg.SizeVT()
	if s == 0 {
		return 0
	}
	return sizeTag(fieldNum) + sizeVarint(uint64(s)) + s
}

var errInvalidData = fmt.Errorf("invalid protobuf data")
