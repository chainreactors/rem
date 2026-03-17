package cio

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}

type rwcDiscard struct {
	r io.Reader
	w io.Writer
}

func (c *rwcDiscard) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *rwcDiscard) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *rwcDiscard) Close() error                { return nil }

func benchBridgeOpenMsg() *message.BridgeOpen {
	return &message.BridgeOpen{
		ID:          42,
		Source:      "bench-src",
		Destination: "bench-dst",
	}
}

func BenchmarkGetPutBuf(b *testing.B) {
	for _, size := range []int{64, 256, 1024, 2048, 5120, 16384, 32768} {
		b.Run("size="+itoa(size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				buf := GetBuf(size)
				PutBuf(buf)
			}
		})
	}
}

func BenchmarkGetPutBufParallel(b *testing.B) {
	const size = 1024
	b.ReportAllocs()
	b.SetBytes(size)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := GetBuf(size)
			PutBuf(buf)
		}
	})
}

func BenchmarkPackBridgeOpen(b *testing.B) {
	msg := benchBridgeOpenMsg()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		packed, err := pack(msg)
		if err != nil {
			b.Fatal(err)
		}
		PutBuf(packed)
	}
}

func BenchmarkWriteMsg(b *testing.B) {
	msg := benchBridgeOpenMsg()
	first, err := pack(msg)
	if err != nil {
		b.Fatal(err)
	}
	frameSize := len(first)
	PutBuf(first)

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, frameSize)
		for i := 0; i < b.N; i++ {
			if _, err := io.ReadFull(c2, buf); err != nil {
				readErr <- err
				return
			}
		}
		readErr <- nil
	}()

	b.ReportAllocs()
	b.SetBytes(int64(frameSize))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := WriteMsg(c1, msg); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if err := <-readErr; err != nil {
		b.Fatal(err)
	}
}

func BenchmarkReadMsg(b *testing.B) {
	msg := benchBridgeOpenMsg()
	frame, err := pack(msg)
	if err != nil {
		b.Fatal(err)
	}
	defer PutBuf(frame)

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	writeErr := make(chan error, 1)
	go func() {
		for i := 0; i < b.N; i++ {
			if _, err := c2.Write(frame); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ReadMsg(c1); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if err := <-writeErr; err != nil {
		b.Fatal(err)
	}
}

func BenchmarkJoinWithError(b *testing.B) {
	payload := bytes.Repeat([]byte("x"), 32*1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload) * 2))
	for i := 0; i < b.N; i++ {
		c1 := &rwcDiscard{r: bytes.NewReader(payload), w: io.Discard}
		c2 := &rwcDiscard{r: bytes.NewReader(payload), w: io.Discard}
		_, _, _ = JoinWithError(c1, c2)
	}
}

func BenchmarkBufferRoundTrip(b *testing.B) {
	for _, size := range []int{128, 1024, 4096} {
		b.Run("size="+itoa(size), func(b *testing.B) {
			buf := NewBuffer(1 << 20)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = io.Copy(io.Discard, buf)
			}()

			payload := bytes.Repeat([]byte("a"), size)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := buf.Write(payload); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			_ = buf.Close()
			wg.Wait()
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var a [20]byte
	i := len(a)
	for n > 0 {
		i--
		a[i] = byte('0' + n%10)
		n /= 10
	}
	return string(a[i:])
}
