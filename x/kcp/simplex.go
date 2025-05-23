package kcp

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/chainreactors/logs"
)

var (
	DefaultSimplexInternal    = 100 // 最大发包间隔(毫秒)
	DefaultSimplexMinInternal = 10  // 最小发包间隔(毫秒)
)

type Simplex interface {
	Receive() (p []byte, addr *SimplexAddr, err error)

	Send(p []byte, addr *SimplexAddr) (n int, err error)

	Close() error
}

// httpBuffer 包装Buffer,添加地址信息
type simplexBuffer struct {
	readCtrlBuf *Buffer // 用于存储待读取的控制包
	readBuf     *Buffer // 用于存储待读取的用户数据
	writeBuf    *Buffer // 用于存储待写入的用户数据
	addr        *SimplexAddr
	ctrlChan    chan []byte // 仅用于WriteTo时临时存储控制包
	mu          sync.Mutex
}

func newSimplexBuffer(addr *SimplexAddr) *simplexBuffer {
	return &simplexBuffer{
		readCtrlBuf: NewBuffer(4 * 1024), // 控制包通常较小
		readBuf:     NewBuffer(addr.maxBodySize),
		writeBuf:    NewBuffer(addr.maxBodySize),
		addr:        addr,
		ctrlChan:    make(chan []byte, 128), // 控制包channel缓冲区
	}
}

// 从buffer中读取所有数据并序列化
func (b *simplexBuffer) marshal() []byte {
	var ctrlBufs [][]byte
	var ctrlTotalLen int
	for {
		select {
		case data := <-b.ctrlChan:
			ctrlBufs = append(ctrlBufs, data)
			ctrlTotalLen += len(data)
		default:
			goto DONE_CTRL
		}
	}
DONE_CTRL:

	userData := make([]byte, b.addr.maxBodySize)
	var n int
	if b.writeBuf.Size() > 0 {
		n, _ = b.writeBuf.Read(userData)
	}
	var userLen int
	if n > 0 {
		userData = userData[:n]
		userLen = n
	}

	if ctrlTotalLen == 0 && userLen == 0 {
		return nil
	}

	totalLen := 8 + ctrlTotalLen + userLen // header(8) + ctrl + user
	data := make([]byte, totalLen)

	binary.LittleEndian.PutUint32(data[0:4], uint32(ctrlTotalLen))
	offset := 4

	if ctrlTotalLen > 0 {
		for _, buf := range ctrlBufs {
			copy(data[offset:], buf)
			offset += len(buf)
		}
	}

	binary.LittleEndian.PutUint32(data[offset:offset+4], uint32(userLen))
	offset += 4

	if userLen > 0 {
		copy(data[offset:], userData)
	}

	return data
}

// 解析数据并写入buffer
func (b *simplexBuffer) parse(data []byte) error {
	if len(data) < 8 {
		return fmt.Errorf("data too short")
	}

	ctrlLen := binary.LittleEndian.Uint32(data[0:4])
	offset := 4

	if ctrlLen > 0 {
		if offset+int(ctrlLen) > len(data) {
			return fmt.Errorf("invalid ctrl data length")
		}
		if _, err := b.readCtrlBuf.Write(data[offset : offset+int(ctrlLen)]); err != nil {
			return fmt.Errorf("write ctrl data failed: %v", err)
		}
		offset += int(ctrlLen)
	}

	if offset+4 > len(data) {
		return fmt.Errorf("invalid user data length field")
	}
	dataLen := binary.LittleEndian.Uint32(data[offset : offset+4])
	offset += 4

	if dataLen > 0 {
		if offset+int(dataLen) > len(data) {
			return fmt.Errorf("invalid user data length")
		}
		if _, err := b.readBuf.Write(data[offset : offset+int(dataLen)]); err != nil {
			return fmt.Errorf("write user data failed: %v", err)
		}
	}

	return nil
}

// 实现优先读取控制包的逻辑
func (b *simplexBuffer) priorityRead(p []byte) (n int, err error) {
	for {
		if b.readCtrlBuf.Size() > 0 {
			return b.readCtrlBuf.Read(p)
		}
		n, err = b.readBuf.Read(p)
		if n == 0 {
			time.Sleep(b.addr.internal)
			continue
		} else {
			return
		}
	}
}

func ResolveSimplexAddr(network, address string) (*SimplexAddr, error) {
	var simplexAddr *SimplexAddr
	var err error
	switch network {
	case "http", "https":
		simplexAddr, err = ResolveHTTPAddr(network, address)
		if err != nil {
			return nil, err
		}
	}

	return simplexAddr, nil
}

type SimplexAddr struct {
	*url.URL
	id          string
	internal    time.Duration
	maxBodySize int
	options     map[string]interface{}
}

func (addr *SimplexAddr) Network() string {
	return addr.URL.Scheme
}

type SimplexClient struct {
	Simplex
	buffer *simplexBuffer
	ctx    context.Context
	cancel context.CancelFunc
	addr   *SimplexAddr
}

func (c *SimplexClient) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	select {
	case <-c.ctx.Done():
		return 0, nil, io.ErrClosedPipe
	default:
		n, _ = c.buffer.priorityRead(p) // 使用优先读取逻辑
		return n, c.addr, err
	}
}

func (c *SimplexClient) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		// 如果是控制包，发送到控制channel
		if isKCPControlPacket(p) {
			ctrlBuf := make([]byte, len(p))
			copy(ctrlBuf, p)
			select {
			case c.buffer.ctrlChan <- ctrlBuf:
			default:
				// channel满了，丢弃
			}
			return len(p), nil
		}

		// 写入用户数据
		return c.buffer.writeBuf.Write(p)
	}
}

func (c *SimplexClient) LocalAddr() net.Addr {
	return c.addr
}

func (c *SimplexClient) SetDeadline(t time.Time) error      { return nil }
func (c *SimplexClient) SetReadDeadline(t time.Time) error  { return nil }
func (c *SimplexClient) SetWriteDeadline(t time.Time) error { return nil }

func (c *SimplexClient) polling() {
	recvTicker := time.NewTicker(c.addr.internal)
	sendTicker := time.NewTicker(c.addr.internal)
	lastSendTime := time.Now()

	go func() {
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-sendTicker.C:
				body := c.buffer.marshal()
				c.Send(body, c.addr)
				lastSendTime = time.Now()
			default:
				// 检查是否有数据需要发送
				now := time.Now()
				if now.Sub(lastSendTime) < time.Duration(DefaultSimplexMinInternal)*time.Millisecond {
					time.Sleep(time.Duration(DefaultSimplexMinInternal) * time.Millisecond)
					continue
				}

				body := c.buffer.marshal()
				if body != nil {
					c.Send(body, c.addr)
					lastSendTime = now
				}
			}
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-recvTicker.C:
			p, _, err := c.Receive()
			if err != nil || len(p) == 0 {
				continue
			}
			err = c.buffer.parse(p)
			if err != nil {
				logs.Log.Error(err)
				continue
			}
		}
	}
}

type SimplexServer struct {
	Simplex
	buffers sync.Map
	ctx     context.Context
	cancel  context.CancelFunc
	addr    *SimplexAddr
}

func (c *SimplexServer) polling() {
	ticker := time.NewTicker(c.addr.internal)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			p, addr, err := c.Receive()
			if err != nil || len(p) == 0 {
				continue
			}
			buf := c.GetBuffer(addr)
			//buf.mu.Lock()
			err = buf.parse(p)
			//buf.mu.Unlock()
			if err != nil {
				logs.Log.Error(err)
				continue
			}
		}
	}
}

func (c *SimplexServer) GetBuffer(addr *SimplexAddr) *simplexBuffer {
	buf, ok := c.buffers.Load(addr.id)
	if ok {
		return buf.(*simplexBuffer)
	}

	sbuf := newSimplexBuffer(addr)
	c.buffers.Store(addr.id, sbuf)
	go func() {
		for {
			body := sbuf.marshal()
			if body == nil {
				continue
			}

			_, err := c.Send(body, sbuf.addr)
			if err != nil {
				return
			}
		}
	}()
	return sbuf
}

// Server端方法实现
func (c *SimplexServer) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	found := false
	c.buffers.Range(func(_, value interface{}) bool {
		buf := value.(*simplexBuffer)
		n, _ = buf.priorityRead(p) // 使用优先读取逻辑
		if n > 0 {
			found = true
			addr = buf.addr
			return false
		}
		return true
	})

	if found {
		return n, addr, err
	}

	select {
	case <-c.ctx.Done():
		return 0, nil, io.ErrClosedPipe
	default:
		return 0, nil, nil
	}
}

func (c *SimplexServer) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		httpAddr, ok := addr.(*SimplexAddr)
		if !ok {
			return 0, fmt.Errorf("invalid address type: %T", addr)
		}

		// 获取或创建buffer
		buf := c.GetBuffer(httpAddr)

		// 如果是控制包，发送到控制channel
		if isKCPControlPacket(p) {
			ctrlBuf := make([]byte, len(p))
			copy(ctrlBuf, p)
			select {
			case buf.ctrlChan <- ctrlBuf:
			default:
				// channel满了，丢弃
			}
			return len(p), nil
		}

		// 写入用户数据缓冲区
		n, err := buf.writeBuf.Write(p)
		if err != nil {
			return 0, err
		}
		return n, err
	}
}

func (c *SimplexServer) LocalAddr() net.Addr {
	return c.addr
}

func (c *SimplexServer) SetDeadline(t time.Time) error      { return nil }
func (c *SimplexServer) SetReadDeadline(t time.Time) error  { return nil }
func (c *SimplexServer) SetWriteDeadline(t time.Time) error { return nil }

// 判断是否是KCP控制包
func isKCPControlPacket(data []byte) bool {
	if len(data) < 24 { // KCP头部最小长度
		return false
	}
	cmd := data[4]              // cmd在KCP头部的第5个字节
	return cmd != IKCP_CMD_PUSH // 不是数据包就是控制包
}

func NewSimplexServer(network, address string) (*SimplexServer, error) {
	var simplex Simplex
	addr, err := ResolveSimplexAddr(network, address)
	if err != nil {
		return nil, err
	}
	switch network {
	case "http", "https":
		simplex, err = newHTTPServer(network, address)
	default:
		return nil, fmt.Errorf("unsupported network: %s", network)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &SimplexServer{
		Simplex: simplex,
		ctx:     ctx,
		cancel:  cancel,
		addr:    addr,
	}
	go server.polling()
	return server, nil
}

func NewSimplexClient(addr *SimplexAddr) (*SimplexClient, error) {
	var err error
	var simplex Simplex
	switch addr.Scheme {
	case "http", "https":
		simplex, err = newHTTPClient(addr)
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := &SimplexClient{
		Simplex: simplex,
		buffer:  newSimplexBuffer(addr),
		ctx:     ctx,
		cancel:  cancel,
		addr:    addr,
	}
	go client.polling()
	return client, nil
}
