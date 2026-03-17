package message

import (
	"bytes"
	"testing"
)

func TestAppendVarint(t *testing.T) {
	tests := []struct {
		name string
		val  uint64
		want []byte
	}{
		{"zero", 0, []byte{0x00}},
		{"one", 1, []byte{0x01}},
		{"127", 127, []byte{0x7f}},
		{"128", 128, []byte{0x80, 0x01}},
		{"300", 300, []byte{0xac, 0x02}},
		{"max_uint64", ^uint64(0), []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendVarint(nil, tt.val)
			if !bytes.Equal(got, tt.want) {
				t.Errorf("appendVarint(%d) = %x, want %x", tt.val, got, tt.want)
			}
			decoded, n := consumeVarint(got)
			if n != len(got) {
				t.Errorf("consumeVarint consumed %d bytes, want %d", n, len(got))
			}
			if decoded != tt.val {
				t.Errorf("consumeVarint = %d, want %d", decoded, tt.val)
			}
		})
	}
}

func TestAppendTag(t *testing.T) {
	tests := []struct {
		name     string
		fieldNum uint32
		wireType uint8
		want     []byte
	}{
		{"field1_varint", 1, 0, []byte{0x08}},         // (1<<3)|0 = 8
		{"field1_bytes", 1, 2, []byte{0x0a}},          // (1<<3)|2 = 10
		{"field2_varint", 2, 0, []byte{0x10}},         // (2<<3)|0 = 16
		{"field2_bytes", 2, 2, []byte{0x12}},          // (2<<3)|2 = 18
		{"field12_bytes", 12, 2, []byte{0x62}},        // (12<<3)|2 = 98
		{"field13_bytes", 13, 2, []byte{0x6a}},        // (13<<3)|2 = 106
		{"field16_varint", 16, 0, []byte{0x80, 0x01}}, // (16<<3)|0 = 128
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendTag(nil, tt.fieldNum, tt.wireType)
			if !bytes.Equal(got, tt.want) {
				t.Errorf("appendTag(%d, %d) = %x, want %x", tt.fieldNum, tt.wireType, got, tt.want)
			}
			fn, wt, n := consumeTag(got)
			if fn != tt.fieldNum || wt != tt.wireType || n != len(got) {
				t.Errorf("consumeTag = (%d, %d, %d), want (%d, %d, %d)",
					fn, wt, n, tt.fieldNum, tt.wireType, len(got))
			}
		})
	}
}

func TestDefaultValuesNotSerialized(t *testing.T) {
	b := appendString(nil, 1, "")
	if len(b) != 0 {
		t.Errorf("empty string should not be serialized, got %x", b)
	}
	b = appendInt32(nil, 1, 0)
	if len(b) != 0 {
		t.Errorf("zero int32 should not be serialized, got %x", b)
	}
	b = appendUint64Field(nil, 1, 0)
	if len(b) != 0 {
		t.Errorf("zero uint64 should not be serialized, got %x", b)
	}
	b = appendBool(nil, 1, false)
	if len(b) != 0 {
		t.Errorf("false bool should not be serialized, got %x", b)
	}
	b = appendRawBytes(nil, 1, nil)
	if len(b) != 0 {
		t.Errorf("nil bytes should not be serialized, got %x", b)
	}
	b = appendInt64Field(nil, 1, 0)
	if len(b) != 0 {
		t.Errorf("zero int64 should not be serialized, got %x", b)
	}
}

func TestSizeConsistency(t *testing.T) {
	checks := []struct {
		name string
		size int
		data []byte
	}{
		{"string", sizeString(1, "hello"), appendString(nil, 1, "hello")},
		{"int32", sizeInt32(2, 300), appendInt32(nil, 2, 300)},
		{"uint64", sizeUint64Field(1, 123456), appendUint64Field(nil, 1, 123456)},
		{"int64", sizeInt64Field(3, 999), appendInt64Field(nil, 3, 999)},
		{"bool", sizeBool(2, true), appendBool(nil, 2, true)},
		{"bytes", sizeRawBytes(5, []byte{1, 2, 3}), appendRawBytes(nil, 5, []byte{1, 2, 3})},
		{"empty_string", sizeString(1, ""), appendString(nil, 1, "")},
		{"zero_int32", sizeInt32(1, 0), appendInt32(nil, 1, 0)},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if c.size != len(c.data) {
				t.Errorf("size=%d but actual len=%d, data=%x", c.size, len(c.data), c.data)
			}
		})
	}
}
