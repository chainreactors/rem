package simplex

import (
	"fmt"
	"sync"
)

// PeekableChannel 支持Peek操作的Channel
type PeekableChannel struct {
	ch           chan *SimplexPacket
	peekedPacket *SimplexPacket // 临时保存peek的数据包
	closed       bool
	mu           sync.Mutex
}

func NewPeekableChannel(size int) *PeekableChannel {
	return &PeekableChannel{
		ch: make(chan *SimplexPacket, size),
	}
}

func (pc *PeekableChannel) Put(packet *SimplexPacket) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.closed {
		return fmt.Errorf("channel closed")
	}

	select {
	case pc.ch <- packet:
		return nil
	default:
		return fmt.Errorf("channel full")
	}
}

func (pc *PeekableChannel) Get() (*SimplexPacket, error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.closed {
		return nil, fmt.Errorf("channel closed")
	}

	// 如果有peeked的数据包，先返回它
	if pc.peekedPacket != nil {
		packet := pc.peekedPacket
		pc.peekedPacket = nil
		return packet, nil
	}

	select {
	case packet := <-pc.ch:
		return packet, nil
	default:
		return nil, nil
	}
}

func (pc *PeekableChannel) Peek() (*SimplexPacket, error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.closed {
		return nil, fmt.Errorf("channel closed")
	}

	// 如果已经有peeked的数据包，直接返回
	if pc.peekedPacket != nil {
		return pc.peekedPacket, nil
	}

	// 从channel中取出数据包并临时保存
	select {
	case packet := <-pc.ch:
		pc.peekedPacket = packet
		return packet, nil
	default:
		return nil, nil
	}
}

func (pc *PeekableChannel) Close() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if !pc.closed {
		pc.closed = true
		close(pc.ch)
	}
	return nil
}

func (pc *PeekableChannel) Size() int {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	size := len(pc.ch)
	if pc.peekedPacket != nil {
		size++
	}
	return size
}
