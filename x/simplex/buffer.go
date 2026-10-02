package simplex

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/chainreactors/rem/x/arq"
	"github.com/chainreactors/rem/x/utils"
)

// SimplexBuffer stores outbound packets in priority queues and inbound packets
// in a packet-boundary-preserving ChannelBuffer.
type SimplexBuffer struct {
	recvBuf *utils.ChannelBuffer // 接收：保持包边界的 packet buffer
	addr    *SimplexAddr

	closeOnce sync.Once

	queueMu    sync.Mutex
	closed     bool
	ctrlQueue  []*SimplexPacket
	dataQueue  []*SimplexPacket
	ctrlCap    int
	dataCap    int
	queueReady chan struct{}
}

var (
	defaultSimplexCtrlChannelCapacity = 64
	defaultSimplexSendChannelCapacity = 512
	defaultSimplexRecvChannelCapacity = 128
)

func NewSimplexBuffer(addr *SimplexAddr) *SimplexBuffer {
	sendCap := defaultSimplexSendChannelCapacity
	ctrlCap := defaultSimplexCtrlChannelCapacity
	recvCap := defaultSimplexRecvChannelCapacity

	if addr != nil {
		wndSize := addr.ARQConfig().WndSize
		if wndSize <= 0 {
			wndSize = arq.ARQ_WND_SIZE
		}
		if c := wndSize * 4; c > sendCap {
			sendCap = c
		}
		if c := wndSize; c > ctrlCap {
			ctrlCap = c
		}
		if c := wndSize * 2; c > recvCap {
			recvCap = c
		}
	}

	return &SimplexBuffer{
		recvBuf:    utils.NewChannel(recvCap),
		addr:       addr,
		ctrlCap:    ctrlCap,
		dataCap:    sendCap,
		queueReady: make(chan struct{}, 1),
	}
}

func (b *SimplexBuffer) Addr() *SimplexAddr {
	return b.addr
}

// RecvPut 写入一个接收到的完整包（保持包边界）
func (b *SimplexBuffer) RecvPut(data []byte) error {
	return b.recvBuf.PutWait(context.Background(), data)
}

// RecvGet 读取一个完整的接收包（保持包边界）
func (b *SimplexBuffer) RecvGet() ([]byte, error) {
	return b.recvBuf.Get()
}

func (b *SimplexBuffer) RecvGetWait(ctx context.Context) ([]byte, error) {
	return b.recvBuf.GetWait(ctx)
}

// PutPacket: 按类型投放到不同通道
func (b *SimplexBuffer) PutPacket(pkt *SimplexPacket) error {
	_, err := b.putPacketWait(context.Background(), pkt)
	return err
}

func (b *SimplexBuffer) signalQueueReady() {
	select {
	case b.queueReady <- struct{}{}:
	default:
	}
}

// SendReady returns a channel that is signaled when new packets are enqueued.
// The send goroutine can select on this to wake up immediately instead of
// waiting for the next polling tick.
func (b *SimplexBuffer) SendReady() <-chan struct{} {
	return b.queueReady
}

func (b *SimplexBuffer) putPacketWait(ctx context.Context, pkt *SimplexPacket) (bool, error) {
	if pkt == nil {
		return true, nil
	}

	for {
		b.queueMu.Lock()
		if b.closed {
			b.queueMu.Unlock()
			return false, io.ErrClosedPipe
		}
		if b.enqueuePacketLocked(pkt) {
			b.queueMu.Unlock()
			return true, nil
		}
		b.queueMu.Unlock()

		if ctx != nil {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(simplexQueueBackpressurePollInterval):
			}
		} else {
			time.Sleep(simplexQueueBackpressurePollInterval)
		}
	}
}

func (b *SimplexBuffer) PutPackets(packets *SimplexPackets) error {
	for _, packet := range packets.Packets {
		if _, err := b.putPacketWait(context.Background(), packet); err != nil {
			return err
		}
	}
	return nil
}

// GetPacket: 优先从ctrlChannel取
func (b *SimplexBuffer) GetPacket() (*SimplexPacket, error) {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()

	if b.closed {
		return nil, io.ErrClosedPipe
	}
	if len(b.ctrlQueue) > 0 {
		packet := b.ctrlQueue[0]
		b.ctrlQueue = b.ctrlQueue[1:]
		return packet, nil
	}
	if len(b.dataQueue) > 0 {
		packet := b.dataQueue[0]
		b.dataQueue = b.dataQueue[1:]
		return packet, nil
	}
	return nil, nil
}

func (b *SimplexBuffer) GetPackets() (*SimplexPackets, error) {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()

	if b.closed {
		return nil, io.ErrClosedPipe
	}

	packets := NewSimplexPackets()
	b.appendFromQueueLocked(&b.ctrlQueue, packets)
	b.appendFromQueueLocked(&b.dataQueue, packets)
	return packets, nil
}

func (b *SimplexBuffer) WaitUntilQueued(ctx context.Context) error {
	for {
		b.queueMu.Lock()
		if b.closed {
			b.queueMu.Unlock()
			return io.ErrClosedPipe
		}
		if len(b.ctrlQueue) > 0 || len(b.dataQueue) > 0 {
			b.queueMu.Unlock()
			return nil
		}
		b.queueMu.Unlock()

		if ctx == nil {
			<-b.queueReady
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.queueReady:
		}
	}
}

func (b *SimplexBuffer) GetControlPackets() (*SimplexPackets, error) {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()

	if b.closed {
		return nil, io.ErrClosedPipe
	}

	packets := NewSimplexPackets()
	b.appendFromQueueLocked(&b.ctrlQueue, packets)
	return packets, nil
}

// GetAllDataPackets drains all queued data packets up to the per-tick budget.
// The budget is derived from SimplexConfig (MaxBodySize × ItemsPerCycle) and
// limits how much data the transport layer sends per polling cycle.
func (b *SimplexBuffer) GetAllDataPackets() (*SimplexPackets, error) {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()

	if b.closed {
		return nil, io.ErrClosedPipe
	}

	packets := NewSimplexPackets()
	budget := b.addr.maxBodySize
	if b.addr != nil {
		if sc := b.addr.SimplexConfig(); sc.DataBudget() > 0 {
			budget = sc.DataBudget()
		}
	}
	for len(b.dataQueue) > 0 {
		pkt := b.dataQueue[0]
		if packets.Size()+pkt.Size() > budget {
			break
		}
		packets.Append(pkt)
		b.dataQueue = b.dataQueue[1:]
	}
	return packets, nil
}

// Close 关闭 SimplexBuffer，释放所有内部 channel 资源。
// 关闭后 PutPacket/GetPacket 返回 io.ErrClosedPipe。
func (b *SimplexBuffer) Close() error {
	b.closeOnce.Do(func() {
		b.queueMu.Lock()
		b.closed = true
		b.ctrlQueue = nil
		b.dataQueue = nil
		b.queueMu.Unlock()
		b.signalQueueReady()
		b.recvBuf.Close()
	})
	return nil
}

func (b *SimplexBuffer) ControlQueueSize() int {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	return len(b.ctrlQueue)
}

func (b *SimplexBuffer) DataQueueSize() int {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	return len(b.dataQueue)
}

func (b *SimplexBuffer) enqueuePacketLocked(pkt *SimplexPacket) bool {
	switch pkt.PacketType {
	case SimplexPacketTypeCTRL:
		if len(b.ctrlQueue) >= b.ctrlCap {
			return false
		}
		b.ctrlQueue = append(b.ctrlQueue, pkt)
	default:
		if len(b.dataQueue) >= b.dataCap {
			return false
		}
		b.dataQueue = append(b.dataQueue, pkt)
	}
	b.signalQueueReady()
	return true
}

func (b *SimplexBuffer) appendFromQueueLocked(queue *[]*SimplexPacket, packets *SimplexPackets) {
	for len(*queue) > 0 {
		pkt := (*queue)[0]
		if packets.Size()+pkt.Size() > b.addr.maxBodySize {
			break
		}
		packets.Append(pkt)
		*queue = (*queue)[1:]
	}
}

// AsymBuffer 非对称通信缓冲区，用于DNS、HTTP等只允许客户端主动发送的协议
// 特点：客户端只能发送请求并接收响应，服务端只能接收请求并发送响应
type AsymBuffer struct {
	readBuf    *SimplexBuffer // 接收数据的缓冲区
	writeBuf   *SimplexBuffer // 发送数据的缓冲区
	addr       *SimplexAddr   // 地址信息
	activityMu sync.RWMutex
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

// Close 关闭缓冲区，释放内部 SimplexBuffer 资源。
func (buf *AsymBuffer) Close() error {
	buf.readBuf.Close()
	buf.writeBuf.Close()
	return nil
}

// Touch updates the last active timestamp.
func (buf *AsymBuffer) Touch() {
	buf.activityMu.Lock()
	defer buf.activityMu.Unlock()
	buf.lastActive = time.Now()
}

// LastActive returns the last active timestamp.
func (buf *AsymBuffer) LastActive() time.Time {
	buf.activityMu.RLock()
	defer buf.activityMu.RUnlock()
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
