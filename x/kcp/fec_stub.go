//go:build !kcp_fec

package kcp

import "encoding/binary"

const (
	fecHeaderSize      = 6
	fecHeaderSizePlus2 = fecHeaderSize + 2
	typeData           = 0xf1
	typeParity         = 0xf2
)

type fecPacket []byte

func (packet fecPacket) seqid() uint32 { return binary.LittleEndian.Uint32(packet) }
func (packet fecPacket) flag() uint16  { return binary.LittleEndian.Uint16(packet[4:]) }
func (packet fecPacket) data() []byte  { return packet[6:] }

type fecDecoder struct{}

func newFECDecoder(dataShards, parityShards int) *fecDecoder {
	if dataShards <= 0 || parityShards <= 0 {
		return nil
	}
	return &fecDecoder{}
}

func (decoder *fecDecoder) decode(in fecPacket) (recovered [][]byte) {
	return nil
}

func (decoder *fecDecoder) release() {}

type fecEncoder struct {
	headerOffset  int
	payloadOffset int
	next          uint32
	paws          uint32
}

func newFECEncoder(dataShards, parityShards, offset int) *fecEncoder {
	if dataShards <= 0 || parityShards <= 0 {
		return nil
	}

	shardSize := dataShards + parityShards
	return &fecEncoder{
		headerOffset:  offset,
		payloadOffset: offset + fecHeaderSize,
		paws:          0xffffffff / uint32(shardSize) * uint32(shardSize),
	}
}

func (encoder *fecEncoder) encode(buffer []byte, rto uint32) (parity [][]byte) {
	if len(buffer) < encoder.payloadOffset+2 {
		return nil
	}

	encoder.markData(buffer[encoder.headerOffset:])
	binary.LittleEndian.PutUint16(buffer[encoder.payloadOffset:], uint16(len(buffer[encoder.payloadOffset:])))
	return nil
}

func (encoder *fecEncoder) markData(data []byte) {
	if len(data) < fecHeaderSize {
		return
	}
	binary.LittleEndian.PutUint32(data, encoder.next)
	binary.LittleEndian.PutUint16(data[4:], typeData)
	encoder.next = (encoder.next + 1) % encoder.paws
}

func (encoder *fecEncoder) markParity(data []byte) {
	if len(data) < fecHeaderSize {
		return
	}
	binary.LittleEndian.PutUint32(data, encoder.next)
	binary.LittleEndian.PutUint16(data[4:], typeParity)
	encoder.next = (encoder.next + 1) % encoder.paws
}
