//go:build !tinygo

package streamhttp

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Binary frame protocol for streamhttp downlink.
//
// Frame layout:
//   [1 byte type][4 bytes payload length, big-endian uint32][payload]
//
// Frame types:
//   0x01 DATA:  payload = [8 bytes seq_id, big-endian uint64][raw data]
//   0x02 PING:  length=0, no payload
//   0x03 CLOSE: length=0, no payload

const (
	frameTypeData  byte = 0x01
	frameTypePing  byte = 0x02
	frameTypeClose byte = 0x03

	frameHeaderSize = 5 // 1 type + 4 length
	seqIDSize       = 8 // uint64
)

func writeDataFrame(w io.Writer, seqID uint64, data []byte) error {
	payloadLen := seqIDSize + len(data)
	hdr := make([]byte, frameHeaderSize+seqIDSize)
	hdr[0] = frameTypeData
	binary.BigEndian.PutUint32(hdr[1:5], uint32(payloadLen))
	binary.BigEndian.PutUint64(hdr[5:13], seqID)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func writePingFrame(w io.Writer) error {
	hdr := [frameHeaderSize]byte{frameTypePing}
	// length bytes remain 0
	_, err := w.Write(hdr[:])
	return err
}

func writeCloseFrame(w io.Writer) error {
	hdr := [frameHeaderSize]byte{frameTypeClose}
	_, err := w.Write(hdr[:])
	return err
}

// readFrame reads a single frame from r.
// Returns frameType, seqID (only valid for DATA), data, and error.
func readFrame(r io.Reader) (ft byte, seqID uint64, data []byte, err error) {
	var hdr [frameHeaderSize]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return
	}

	ft = hdr[0]
	payloadLen := binary.BigEndian.Uint32(hdr[1:5])

	switch ft {
	case frameTypePing, frameTypeClose:
		if payloadLen != 0 {
			err = fmt.Errorf("streamhttp: %s frame with non-zero length %d", frameTypeName(ft), payloadLen)
		}
		return

	case frameTypeData:
		if payloadLen < seqIDSize {
			err = fmt.Errorf("streamhttp: DATA frame payload too short: %d", payloadLen)
			return
		}
		var seqBuf [seqIDSize]byte
		if _, err = io.ReadFull(r, seqBuf[:]); err != nil {
			return
		}
		seqID = binary.BigEndian.Uint64(seqBuf[:])

		dataLen := payloadLen - seqIDSize
		if dataLen > 0 {
			data = make([]byte, dataLen)
			if _, err = io.ReadFull(r, data); err != nil {
				return
			}
		}
		return

	default:
		err = fmt.Errorf("streamhttp: unknown frame type 0x%02x", ft)
		return
	}
}

func frameTypeName(ft byte) string {
	switch ft {
	case frameTypeData:
		return "DATA"
	case frameTypePing:
		return "PING"
	case frameTypeClose:
		return "CLOSE"
	default:
		return fmt.Sprintf("0x%02x", ft)
	}
}
