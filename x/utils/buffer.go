package utils

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)


// ErrBufferFull is returned when a non-blocking Put cannot enqueue the packet.
var ErrBufferFull = errors.New("buffer full")

// Buffer 是一个线程安全的带大小限制的缓冲区
type Buffer struct {
	buf    *bytes.Buffer
	mu     sync.RWMutex
	cond   *sync.Cond
	maxLen int
	closed bool
}

// NewBuffer 创建一个新的Buffer
func NewBuffer(maxLen int) *Buffer {
	b := &Buffer{
		buf:    bytes.NewBuffer(nil),
		maxLen: maxLen,
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Write 写入数据,如果缓冲区满则阻塞等待
func (b *Buffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := len(p)
	written := 0

	for remaining > 0 {
		if b.closed {
			return written, io.ErrClosedPipe
		}

		// 计算当前可写入的空间
		availSpace := b.maxLen - b.buf.Len()
		if availSpace <= 0 {
			// 如果没有可用空间，等待
			b.cond.Wait() // Wait会自动释放锁并在返回时重新获取
			continue
		}

		// 确定本次写入的长度
		writeLen := remaining
		if writeLen > availSpace {
			writeLen = availSpace
		}

		n, _ = b.buf.Write(p[written : written+writeLen])

		written += n
		remaining -= n

		b.cond.Signal()
	}

	return written, nil
}

// Read 读取数据,如果没有数据则返回EOF
func (b *Buffer) Read(p []byte) (n int, err error) {
	b.mu.Lock()
	if b.buf.Len() == 0 {
		b.mu.Unlock()
		return 0, io.EOF
	}
	n, err = b.buf.Read(p)
	if n > 0 {
		b.cond.Signal()
	}
	b.mu.Unlock()
	return
}

// ReadAtLeast 读取数据，如果缓冲区为空则阻塞等待，直到有数据可读
func (b *Buffer) ReadAtLeast(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 如果没有数据，等待直到有数据
	for b.buf.Len() == 0 {
		if b.closed {
			return 0, io.ErrClosedPipe
		}
		b.cond.Wait()
	}

	n, err = b.buf.Read(p)
	if n > 0 {
		b.cond.Signal()
	}
	return
}

// Close 关闭缓冲区
func (b *Buffer) Close() error {
	b.mu.Lock()
	b.closed = true
	// 不 Reset，允许消费者读取剩余数据
	b.cond.Broadcast()
	b.mu.Unlock()
	return nil
}

// Size 返回当前缓冲区长度
func (b *Buffer) Size() int {
	b.mu.RLock()
	defer b.mu.RUnlock() // 这里可以用defer，因为没有Wait操作
	return b.buf.Len()
}

// Cap 返回缓冲区容量
func (b *Buffer) Cap() int {
	return b.maxLen
}

// ChannelBuffer is a packet-boundary-preserving buffer backed by a channel.
// Unlike Buffer (byte-stream), each Put/Get operates on a whole packet.
//
// Race-safety: the underlying Go channel is NEVER closed. Close() only sets an
// atomic flag. This eliminates the send-on-closed-channel panic that would
// otherwise race between concurrent Put() and Close() calls.
type ChannelBuffer struct {
	ch     chan []byte
	closed int32 // atomic: 0=open, 1=closed
}

// NewChannel creates a ChannelBuffer that preserves packet boundaries.
// Each Put stores one complete []byte; each Get returns one complete []byte.
func NewChannel(size int) *ChannelBuffer {
	if size <= 0 {
		size = 1
	}
	return &ChannelBuffer{
		ch: make(chan []byte, size),
	}
}

// Get returns the next packet, or (nil, nil) if empty. Non-blocking.
// After Close, remaining packets are drained before returning ErrClosedPipe.
func (ch *ChannelBuffer) Get() ([]byte, error) {
	select {
	case data := <-ch.ch:
		return data, nil
	default:
		if atomic.LoadInt32(&ch.closed) != 0 {
			return nil, io.ErrClosedPipe
		}
		return nil, nil
	}
}

// Put enqueues a packet. Non-blocking; returns ErrBufferFull when capacity is reached.
func (ch *ChannelBuffer) Put(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if atomic.LoadInt32(&ch.closed) != 0 {
		return io.ErrClosedPipe
	}
	select {
	case ch.ch <- data:
		return nil
	default:
		return ErrBufferFull
	}
}

// Close marks the buffer as closed. Remaining packets can still be drained via Get.
// The underlying Go channel is intentionally NOT closed to avoid send-on-closed-channel
// panics from concurrent Put() calls.
func (ch *ChannelBuffer) Close() error {
	atomic.StoreInt32(&ch.closed, 1)
	return nil
}

// Len returns the number of packets currently buffered.
func (ch *ChannelBuffer) Len() int {
	return len(ch.ch)
}
