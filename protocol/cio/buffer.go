package cio

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
)

var (
	bufPool32k sync.Pool
	bufPool16k sync.Pool
	bufPool5k  sync.Pool
	bufPool2k  sync.Pool
	bufPool1k  sync.Pool
	bufPool    sync.Pool
)

func GetBuf(size int) []byte {
	var x interface{}
	if size >= 32*1024 {
		x = bufPool32k.Get()
	} else if size >= 16*1024 {
		x = bufPool16k.Get()
	} else if size >= 5*1024 {
		x = bufPool5k.Get()
	} else if size >= 2*1024 {
		x = bufPool2k.Get()
	} else if size >= 1*1024 {
		x = bufPool1k.Get()
	} else {
		x = bufPool.Get()
	}
	if x == nil {
		return make([]byte, size)
	}
	buf := x.([]byte)
	if cap(buf) < size {
		return make([]byte, size)
	}
	return buf[:size]
}

func PutBuf(buf []byte) {
	size := cap(buf)
	if size >= 32*1024 {
		bufPool32k.Put(buf)
	} else if size >= 16*1024 {
		bufPool16k.Put(buf)
	} else if size >= 5*1024 {
		bufPool5k.Put(buf)
	} else if size >= 2*1024 {
		bufPool2k.Put(buf)
	} else if size >= 1*1024 {
		bufPool1k.Put(buf)
	} else {
		bufPool.Put(buf)
	}
}

// Buffer is a non-thread-safe buffer with pipe
type Buffer struct {
	WriteCount int64
	ReadCount  int64
	dataCh     chan []byte
	pipeR      *io.PipeReader
	pipeW      *io.PipeWriter
	ctx        context.Context
	cancel     context.CancelFunc
	cur        int64
	max        int64
	chunkSize  int
	closed     int32 // 0 表示 false, 1 表示 true
}

func NewBuffer(maxBufSize int64) *Buffer {
	return NewBufferContext(context.Background(), maxBufSize)
}

func NewBufferContext(ctx context.Context, maxBufSize int64) *Buffer {
	pipeR, pipeW := io.Pipe()
	ctx, cancel := context.WithCancel(ctx)
	bp := &Buffer{
		dataCh:    make(chan []byte, 100),
		pipeR:     pipeR,
		pipeW:     pipeW,
		ctx:       ctx,
		cancel:    cancel,
		max:       maxBufSize,
		chunkSize: int(maxBufSize / 100),
	}
	go bp.processLoop()
	return bp
}

func (bp *Buffer) processLoop() {
	defer bp.pipeW.Close()

	for {
		select {
		case <-bp.ctx.Done():
			return
		case data := <-bp.dataCh:
			n, err := bp.pipeW.Write(data)
			if n > 0 {
				bp.cur -= int64(n)
			}
			PutBuf(data)
			if err != nil {
				continue
			}
		}
	}
}

func (bp *Buffer) Write(p []byte) (n int, err error) {
	if atomic.LoadInt32(&bp.closed) == 1 {
		return 0, io.ErrClosedPipe
	}

	bp.WriteCount += int64(len(p))

	if len(p) > bp.chunkSize {
		n, err = bp.writeChunks(p)
		if err != nil {
			return n, err
		}
		return n, nil
	} else {
		data := GetBuf(len(p))
		copy(data, p)

		err = bp.writeOne(data)
		if err != nil {
			PutBuf(data)
			return 0, err
		}

		bp.cur += int64(len(p))
		return len(p), nil
	}
}

func (bp *Buffer) writeChunks(p []byte) (n int, err error) {
	total := len(p)
	for i := 0; i < total; i += bp.chunkSize {
		end := i + bp.chunkSize
		if end > total {
			end = total
		}

		chunk := GetBuf(end - i)
		copy(chunk, p[i:end])

		if err := bp.writeOne(chunk); err != nil {
			PutBuf(chunk)
			return i, err
		}
		bp.cur += int64(end - i)
	}
	return total, nil
}

func (bp *Buffer) writeOne(p []byte) error {
	select {
	case bp.dataCh <- p:
		return nil
	case <-bp.ctx.Done():
		return io.ErrClosedPipe
	}
}

func (bp *Buffer) Percentage() float32 {
	return float32(bp.cur) / float32(bp.max)
}

func (bp *Buffer) Read(p []byte) (n int, err error) {
	if atomic.LoadInt32(&bp.closed) == 1 {
		return 0, io.ErrClosedPipe
	}
	n, err = bp.pipeR.Read(p)
	if n > 0 {
		bp.ReadCount += int64(n)
	}
	return
}

func (bp *Buffer) Close() error {
	if atomic.CompareAndSwapInt32(&bp.closed, 0, 1) {
		close(bp.dataCh)
		bp.cancel()
	}
	return nil
}

func (bp *Buffer) Size() int64 {
	return bp.cur
}
