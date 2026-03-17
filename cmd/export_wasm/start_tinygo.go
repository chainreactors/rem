//go:build tinygo

package main

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"unsafe"
)

const (
	rpcOpRemDial     = 1
	rpcOpMemoryDial  = 2
	rpcOpMemoryRead  = 3
	rpcOpMemoryWrite = 4
	rpcOpMemoryClose = 5
	rpcOpCleanup     = 6
	rpcOpShutdown    = 7
)

const maxRPCPayload = 16 * 1024 * 1024

func runStartMainLoop() {
	ensureInit()

	reader := bufio.NewReaderSize(os.Stdin, 256*1024)
	writer := bufio.NewWriterSize(os.Stdout, 256*1024)
	var scratch []byte

	for {
		op, err := reader.ReadByte()
		if err != nil {
			return
		}

		switch op {
		case rpcOpRemDial:
			cmdline, err := readString(reader)
			if err != nil {
				return
			}
			agentID, errCode := remDialStart(cmdline)
			if err := writeI32(writer, errCode); err != nil {
				return
			}
			if errCode == 0 {
				if err := writeString(writer, agentID); err != nil {
					return
				}
			}
		case rpcOpMemoryDial:
			memhandle, err := readString(reader)
			if err != nil {
				return
			}
			dst, err := readString(reader)
			if err != nil {
				return
			}
			handle, errCode := memoryDialStart(memhandle, dst)
			if err := writeI32(writer, errCode); err != nil {
				return
			}
			if err := writeI32(writer, handle); err != nil {
				return
			}
		case rpcOpMemoryRead:
			handle, err := readI32(reader)
			if err != nil {
				return
			}
			size, err := readI32(reader)
			if err != nil {
				return
			}
			if size < 0 || size > maxRPCPayload {
				if err := writeI32(writer, ErrArgsParseFailed); err != nil {
					return
				}
				if err := writeI32(writer, 0); err != nil {
					return
				}
				break
			}
			need := int(size)
			if cap(scratch) < need {
				scratch = make([]byte, need)
			}
			buf := scratch[:need]
			n, errCode := memoryReadStart(handle, buf)
			if err := writeI32(writer, errCode); err != nil {
				return
			}
			if err := writeI32(writer, n); err != nil {
				return
			}
			if errCode == 0 && n > 0 {
				if _, err := writer.Write(buf[:n]); err != nil {
					return
				}
			}
		case rpcOpMemoryWrite:
			handle, err := readI32(reader)
			if err != nil {
				return
			}
			size, err := readI32(reader)
			if err != nil {
				return
			}
			if size < 0 || size > maxRPCPayload {
				if err := writeI32(writer, ErrArgsParseFailed); err != nil {
					return
				}
				if err := writeI32(writer, 0); err != nil {
					return
				}
				break
			}
			need := int(size)
			if cap(scratch) < need {
				scratch = make([]byte, need)
			}
			buf := scratch[:need]
			if _, err := io.ReadFull(reader, buf); err != nil {
				return
			}
			n, errCode := memoryWriteStart(handle, buf)
			if err := writeI32(writer, errCode); err != nil {
				return
			}
			if err := writeI32(writer, n); err != nil {
				return
			}
		case rpcOpMemoryClose:
			handle, err := readI32(reader)
			if err != nil {
				return
			}
			errCode := memoryCloseStart(handle)
			if err := writeI32(writer, errCode); err != nil {
				return
			}
		case rpcOpCleanup:
			cleanupStart()
			if err := writeI32(writer, 0); err != nil {
				return
			}
		case rpcOpShutdown:
			cleanupStart()
			if err := writeI32(writer, 0); err != nil {
				return
			}
			if err := writer.Flush(); err != nil {
				return
			}
			return
		default:
			if err := writeI32(writer, ErrArgsParseFailed); err != nil {
				return
			}
		}

		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func remDialStart(cmdline string) (string, int32) {
	ensureInit()
	cmdPtr := cstring(cmdline)
	retPtr, errCode := RemDial(cmdPtr)
	FreeCString(cmdPtr)
	if errCode != 0 {
		return "", errCode
	}
	if retPtr == nil {
		return "", ErrDialFailed
	}
	agentID := gostring(retPtr)
	FreeCString(retPtr)
	if agentID == "" {
		return "", ErrDialFailed
	}
	return agentID, 0
}

func memoryDialStart(memhandle string, dst string) (int32, int32) {
	ensureInit()
	if dst == "" {
		return 0, ErrArgsParseFailed
	}
	if memhandle == "" {
		memhandle = "memory"
	}
	memPtr := cstring(memhandle)
	dstPtr := cstring(dst)
	handle, errCode := MemoryDial(memPtr, dstPtr)
	FreeCString(memPtr)
	FreeCString(dstPtr)
	return handle, errCode
}

func memoryReadStart(chandle int32, buf []byte) (int32, int32) {
	if len(buf) == 0 {
		return 0, 0
	}
	return MemoryRead(chandle, unsafe.Pointer(&buf[0]), int32(len(buf)))
}

func memoryWriteStart(chandle int32, buf []byte) (int32, int32) {
	if len(buf) == 0 {
		return 0, 0
	}
	return MemoryWrite(chandle, unsafe.Pointer(&buf[0]), int32(len(buf)))
}

func memoryCloseStart(chandle int32) int32 {
	return MemoryClose(chandle)
}

func cleanupStart() {
	CleanupAgent()
}

func readI32(r *bufio.Reader) (int32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(b[:])), nil
}

func writeI32(w *bufio.Writer, v int32) error {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(v))
	_, err := w.Write(b[:])
	return err
}

func readString(r *bufio.Reader) (string, error) {
	n, err := readI32(r)
	if err != nil {
		return "", err
	}
	if n < 0 || n > maxRPCPayload {
		return "", io.ErrUnexpectedEOF
	}
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, int(n))
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func writeString(w *bufio.Writer, s string) error {
	if err := writeI32(w, int32(len(s))); err != nil {
		return err
	}
	if len(s) == 0 {
		return nil
	}
	_, err := w.WriteString(s)
	return err
}
