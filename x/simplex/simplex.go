package simplex

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/chainreactors/logs"
)

var (
	DefaultSimplexInternal    = 100                   // 最大发包间隔(毫秒)
	DefaultSimplexMinInternal = 10 * time.Millisecond // 最小发包间隔(毫秒)
)

var simplexClientCreators = make(map[string]func(*SimplexAddr) (Simplex, error))
var simplexServerCreators = make(map[string]func(string, string) (Simplex, error))
var simplexAddrResolvers = make(map[string]func(string, string) (*SimplexAddr, error))

func RegisterSimplex(name string, client func(*SimplexAddr) (Simplex, error), server func(string, string) (Simplex, error), addrResolver func(string, string) (*SimplexAddr, error)) error {
	if _, ok := simplexClientCreators[name]; ok {
		return fmt.Errorf("simplex client [%s] is already registered", name)
	} else {
		simplexClientCreators[name] = client
	}
	if _, ok := simplexServerCreators[name]; ok {
		return fmt.Errorf("simplex server [%s] is already registered", name)
	} else {
		simplexServerCreators[name] = server
	}
	if _, ok := simplexAddrResolvers[name]; ok {
		return fmt.Errorf("simplex addr resolver [%s] is already registered", name)
	} else {
		simplexAddrResolvers[name] = addrResolver
	}
	return nil
}

type Simplex interface {
	Receive() (pkts *SimplexPacket, addr *SimplexAddr, err error)
	Send(pkts *SimplexPackets, addr *SimplexAddr) (n int, err error)
	Addr() *SimplexAddr
	Close() error
}

// GetSimplexClient 获取注册的客户端创建器
func GetSimplexClient(name string) (func(*SimplexAddr) (Simplex, error), error) {
	creator, ok := simplexClientCreators[name]
	if ok {
		return creator, nil
	}
	return nil, fmt.Errorf("simplex client [%s] is not registered", name)
}

// GetSimplexServer 获取注册的服务端创建器
func GetSimplexServer(name string) (func(string, string) (Simplex, error), error) {
	creator, ok := simplexServerCreators[name]
	if ok {
		return creator, nil
	}
	return nil, fmt.Errorf("simplex server [%s] is not registered", name)
}

func NewSimplexClient(addr *SimplexAddr) (*SimplexClient, error) {
	creator, err := GetSimplexClient(addr.Scheme)
	if err != nil {
		return nil, err
	}
	sx, err := creator(addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	// 初始化 isCtrl 函数来识别控制包（NACK）
	// SimpleARQChecker 只识别 CMD_NACK (cmd=2)
	isCtrl := func(data []byte) bool {
		if len(data) < 11 { // ARQ_OVERHEAD = 11
			return false
		}
		cmd := data[0]
		return cmd == 2 // CMD_NACK
	}

	client := &SimplexClient{
		Simplex: sx,
		buf:     NewSimplexBuffer(addr),
		ctx:     ctx,
		cancel:  cancel,
		isCtrl:  isCtrl,
	}
	go client.polling()
	return client, nil
}

type SimplexClient struct {
	Simplex
	buf    *SimplexBuffer
	ctx    context.Context
	cancel context.CancelFunc
	isCtrl func([]byte) bool
}

func (c *SimplexClient) SetIsControlPacket(f func([]byte) bool) {
	c.isCtrl = f
}

func (c *SimplexClient) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	select {
	case <-c.ctx.Done():
		return 0, nil, io.ErrClosedPipe
	default:
	}
	data, err := c.buf.RecvGet()
	if err != nil || data == nil {
		return 0, c.Addr(), err
	}
	return copy(p, data), c.Addr(), nil
}

func (c *SimplexClient) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		var packetType SimplexPacketType
		if c.isCtrl(p) {
			packetType = SimplexPacketTypeCTRL
		} else {
			packetType = SimplexPacketTypeDATA
		}
		packet := NewSimplexPacketWithMaxSize(p, packetType, c.Addr().MaxBodySize())
		err = c.buf.PutPackets(packet)
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}
}

func (c *SimplexClient) LocalAddr() net.Addr {
	return c.Addr()
}

// Close stops the polling goroutine and closes the underlying Simplex transport.
// Without cancelling c.ctx, the polling goroutine would continue sending stale
// ARQ packets to the remote after the connection drops, polluting the channel.
func (c *SimplexClient) Close() error {
	c.cancel()
	return c.Simplex.Close()
}

func (c *SimplexClient) SetDeadline(t time.Time) error      { return nil }
func (c *SimplexClient) SetReadDeadline(t time.Time) error  { return nil }
func (c *SimplexClient) SetWriteDeadline(t time.Time) error { return nil }

func (c *SimplexClient) polling() {
	interval := c.Addr().Interval()
	recvTicker := time.NewTicker(interval)
	defer recvTicker.Stop()

	sendTicker := time.NewTicker(interval)
	defer sendTicker.Stop()

	go func() {
		sendInterval := interval
		consecutiveSendFailures := 0
		const maxSendFailures = 30 // polling interval is short, need larger threshold
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-sendTicker.C:
				// 检查 interval 是否被动态修改
				if ni := c.Addr().Interval(); ni != sendInterval {
					sendInterval = ni
					sendTicker.Reset(ni)
				}
				body, _ := c.buf.GetPackets()
				if body.Size() == 0 {
					continue
				}
				if _, err := c.Send(body, c.Addr()); err != nil {
					consecutiveSendFailures++
					logs.Log.Warnf("simplex client send failed (%d/%d): %v", consecutiveSendFailures, maxSendFailures, err)
					if consecutiveSendFailures >= maxSendFailures {
						logs.Log.Errorf("simplex client %d consecutive send failures, closing", consecutiveSendFailures)
						c.cancel()
						return
					}
				} else {
					consecutiveSendFailures = 0
				}
			}
		}
	}()

	// 接收处理
	recvInterval := interval
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-recvTicker.C:
			// 检查 interval 是否被动态修改
			if ni := c.Addr().Interval(); ni != recvInterval {
				recvInterval = ni
				recvTicker.Reset(ni)
			}
			// 每次 tick 排空所有可用的 packet，而不是只读一个。
			// 对于 SharePoint 等高延迟传输，一个 SharePoint item 可能包含多个
			// SimplexPacket（多个 ARQ 段），需要全部交付给 ARQ 层才能组装完整帧。
			for {
				pkt, _, err := c.Receive()
				if err != nil || pkt == nil {
					break
				}
				if pkt.PacketType == SimplexPacketTypeRST {
					logs.Log.Infof("[Simplex] Server restart detected, closing connection")
					c.cancel()
					c.Simplex.Close()
					return
				}
				// 复制数据并放入 ChannelBuffer，保持包边界完整
				pktCopy := make([]byte, len(pkt.Data))
				copy(pktCopy, pkt.Data)
				if err := c.buf.RecvPut(pktCopy); err != nil {
					logs.Log.Warnf("simplex client recv buffer full, dropping packet")
				}
			}
		}
	}
}

// recvEntry holds a received packet with its source address.
type recvEntry struct {
	data []byte
	addr net.Addr
}

type SimplexServer struct {
	Simplex
	buffers sync.Map
	recvCh  chan recvEntry // 接收通道，保持包边界
	ctx     context.Context
	cancel  context.CancelFunc
	isCtrl  func([]byte) bool
}

func (c *SimplexServer) SetIsControlPacket(f func([]byte) bool) {
	c.isCtrl = f
}

func (c *SimplexServer) polling() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			pkt, addr, err := c.Receive()
			if err != nil || pkt == nil {
				time.Sleep(DefaultSimplexMinInternal)
				continue
			}
			// 复制数据并放入共享接收通道，保持包边界完整
			pktCopy := make([]byte, len(pkt.Data))
			copy(pktCopy, pkt.Data)
			select {
			case c.recvCh <- recvEntry{data: pktCopy, addr: addr}:
			default:
				logs.Log.Warnf("simplex server recv channel full, dropping packet")
			}
		}
	}
}

func (c *SimplexServer) GetBuffer(addr *SimplexAddr) *SimplexBuffer {
	buf, ok := c.buffers.Load(addr.ID())
	if ok {
		return buf.(*SimplexBuffer)
	}
	sbuf := NewSimplexBuffer(addr)
	c.buffers.Store(addr.ID(), sbuf)
	go func() {
		for {
			select {
			case <-c.ctx.Done():
				return
			default:
			}

			body, _ := sbuf.GetPackets()
			if body.Size() == 0 {
				time.Sleep(time.Millisecond)
				continue
			}

			if _, err := c.Send(body, addr); err != nil {
				logs.Log.Warnf("simplex server send failed: %v", err)
				continue
			}
		}
	}()
	return sbuf
}

// Server端方法实现
func (c *SimplexServer) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	select {
	case <-c.ctx.Done():
		return 0, nil, io.ErrClosedPipe
	case entry := <-c.recvCh:
		return copy(p, entry.data), entry.addr, nil
	default:
		return 0, nil, nil
	}
}

func (c *SimplexServer) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		a, ok := addr.(*SimplexAddr)
		if !ok {
			return 0, fmt.Errorf("invalid address type: %T", addr)
		}

		// 获取或创建buffer
		buf := c.GetBuffer(a)

		// 创建数据包
		var packetType SimplexPacketType
		if c.isCtrl(p) {
			packetType = SimplexPacketTypeCTRL
		} else {
			packetType = SimplexPacketTypeDATA
		}

		// 创建数据包并添加到writeChannel
		packet := NewSimplexPacketWithMaxSize(p, packetType, a.MaxBodySize())
		err = buf.PutPackets(packet)
		if err != nil {
			return 0, err
		}

		return len(p), nil
	}
}

func (c *SimplexServer) LocalAddr() net.Addr {
	return c.Addr()
}

func (c *SimplexServer) SetDeadline(t time.Time) error      { return nil }
func (c *SimplexServer) SetReadDeadline(t time.Time) error  { return nil }
func (c *SimplexServer) SetWriteDeadline(t time.Time) error { return nil }

func NewSimplexServer(network, address string) (*SimplexServer, error) {
	creator, err := GetSimplexServer(network)
	if err != nil {
		return nil, err
	}
	sx, err := creator(network, address)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())

	// 导入 SimpleARQChecker 来检查控制包
	// 该函数由 x/arq 包提供，用于识别 ARQ 协议的控制包（NACK）
	var isCtrl func([]byte) bool

	// 尝试从 arq 包获取 SimpleARQChecker
	// 这需要导入，但为了避免循环依赖，我们直接定义一个简单的实现
	// SimpleARQChecker 只识别 CMD_NACK (cmd=2)
	isCtrl = func(data []byte) bool {
		if len(data) < 11 { // ARQ_OVERHEAD = 11
			return false
		}
		cmd := data[0]
		return cmd == 2 // CMD_NACK
	}

	server := &SimplexServer{
		Simplex: sx,
		recvCh:  make(chan recvEntry, 128),
		ctx:     ctx,
		cancel:  cancel,
		isCtrl:  isCtrl,
	}
	go server.polling()
	return server, nil
}
