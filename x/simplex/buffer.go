package simplex

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/chainreactors/rem/x/utils"
)

// SimplexBuffer 使用PeekableChannel存储发送数据包，ChannelBuffer存储接收数据包
type SimplexBuffer struct {
	ctrlChannel *PeekableChannel      // 发送：控制包通道
	userChannel *PeekableChannel      // 发送：数据包通道
	recvBuf     *utils.ChannelBuffer  // 接收：保持包边界的 packet buffer
	addr        *SimplexAddr
}

func NewSimplexBuffer(addr *SimplexAddr) *SimplexBuffer {
	// 根据包大小动态调整接收通道容量：大包少 slot 控制内存
	recvCap := 128
	if addr.maxBodySize > 256*1024 {
		recvCap = 16 // >256KB 包: 最多缓存 16 个 (如 4MB*16=64MB)
	} else if addr.maxBodySize > 64*1024 {
		recvCap = 32 // >64KB 包: 最多缓存 32 个
	}

	return &SimplexBuffer{
		ctrlChannel: NewPeekableChannel(32),
		userChannel: NewPeekableChannel(32),
		recvBuf:     utils.NewChannel(recvCap),
		addr:        addr,
	}
}

func (b *SimplexBuffer) Addr() *SimplexAddr {
	return b.addr
}

// RecvPut 写入一个接收到的完整包（保持包边界）
func (b *SimplexBuffer) RecvPut(data []byte) error {
	return b.recvBuf.Put(data)
}

// RecvGet 读取一个完整的接收包（保持包边界）
func (b *SimplexBuffer) RecvGet() ([]byte, error) {
	return b.recvBuf.Get()
}

// PutPacket: 按类型投放到不同通道
func (b *SimplexBuffer) PutPacket(pkt *SimplexPacket) error {
	if pkt == nil {
		return nil
	}
	switch pkt.PacketType {
	case SimplexPacketTypeCTRL:
		return b.ctrlChannel.Put(pkt)
	case SimplexPacketTypeDATA:
		return b.userChannel.Put(pkt)
	default:
		return b.userChannel.Put(pkt)
	}
}

func (b *SimplexBuffer) PutPackets(packets *SimplexPackets) error {
	for _, packet := range packets.Packets {
		if err := b.PutPacket(packet); err != nil {
			return err
		}
	}
	return nil
}

// GetPacket: 优先从ctrlChannel取
func (b *SimplexBuffer) GetPacket() (*SimplexPacket, error) {
	packet, err := b.ctrlChannel.Get()
	if err != nil {
		return nil, err
	}
	if packet == nil {
		packet, err = b.userChannel.Get()
		if err != nil {
			return nil, err
		}
	}

	return packet, nil
}

func (b *SimplexBuffer) Peek() (*SimplexPacket, error) {
	if p, _ := b.ctrlChannel.Peek(); p != nil {
		return p, nil
	} else if p, _ := b.userChannel.Peek(); p != nil {
		return p, nil
	}
	return nil, nil
}

func (b *SimplexBuffer) GetPackets() (*SimplexPackets, error) {
	packets := NewSimplexPackets()
	for {
		p, err := b.Peek()
		if err != nil {
			return nil, err
		}
		if p == nil {
			break
		}
		if packets.Size()+p.Size() > b.addr.maxBodySize {
			break
		}
		pkt, err := b.GetPacket()
		if err != nil {
			return nil, err
		}
		if pkt != nil {
			packets.Append(pkt)
		}
	}
	return packets, nil
}

// AsymBuffer 非对称通信缓冲区，用于DNS、HTTP等只允许客户端主动发送的协议
// 特点：客户端只能发送请求并接收响应，服务端只能接收请求并发送响应
type AsymBuffer struct {
	readBuf    *SimplexBuffer // 接收数据的缓冲区
	writeBuf   *SimplexBuffer // 发送数据的缓冲区
	addr       *SimplexAddr   // 地址信息
	lastActive time.Time
}

// NewAsymBuffer 创建新的非对称缓冲区
func NewAsymBuffer(addr *SimplexAddr) *AsymBuffer {
	return &AsymBuffer{
		readBuf:    NewSimplexBuffer(addr),
		writeBuf:   NewSimplexBuffer(addr),
		addr:       addr,
		lastActive: time.Now(),
	}
}

// Close 关闭缓冲区
func (buf *AsymBuffer) Close() error {
	// SimplexBuffer 没有 Close 方法，所以不需要关闭
	return nil
}

// Touch updates the last active timestamp.
func (buf *AsymBuffer) Touch() {
	buf.lastActive = time.Now()
}

// LastActive returns the last active timestamp.
func (buf *AsymBuffer) LastActive() time.Time {
	return buf.lastActive
}

// Addr 返回地址信息
func (buf *AsymBuffer) Addr() *SimplexAddr {
	return buf.addr
}

// ReadBuf 返回读缓冲区
func (buf *AsymBuffer) ReadBuf() *SimplexBuffer {
	return buf.readBuf
}

// WriteBuf 返回写缓冲区
func (buf *AsymBuffer) WriteBuf() *SimplexBuffer {
	return buf.writeBuf
}

// AsymServerReceive iterates all AsymBuffers in a sync.Map and returns the first
// available packet from any client's ReadBuf. Shared by DNS/HTTP server Receive().
func AsymServerReceive(ctx context.Context, buffers *sync.Map) (*SimplexPacket, *SimplexAddr, error) {
	var pkt *SimplexPacket
	var addr *SimplexAddr
	var pktErr error

	buffers.Range(func(_, value interface{}) bool {
		buf := value.(*AsymBuffer)
		p, err := buf.ReadBuf().GetPacket()
		if p != nil {
			pkt = p
			addr = buf.Addr()
			pktErr = err
			return false
		}
		return true
	})

	if pkt != nil {
		return pkt, addr, pktErr
	}

	select {
	case <-ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
		return nil, nil, nil
	}
}

// AsymServerSend routes packets to the specified client's WriteBuf via LoadOrStore.
// Shared by DNS/HTTP server Send().
func AsymServerSend(ctx context.Context, buffers *sync.Map, pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	select {
	case <-ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		if pkts == nil || pkts.Size() == 0 {
			return 0, nil
		}
		value, _ := buffers.LoadOrStore(addr.id, NewAsymBuffer(addr))
		buf := value.(*AsymBuffer)
		if err := buf.WriteBuf().PutPackets(pkts); err != nil {
			return 0, err
		}
		return pkts.Size(), nil
	}
}
