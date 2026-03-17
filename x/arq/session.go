// ARQSession wraps the ARQ protocol into a standard net.Conn interface.
//
// Architecture (two goroutines per session):
//
//   - updateLoop:  independent ticker driving the ARQ state machine
//     (flush / cleanup). Never blocked by application-layer I/O.
//   - backgroundLoop: data I/O only — reads from the underlying
//     PacketConn (client mode) or polls arq.Recv (listener mode),
//     then delivers reassembled data to the read buffer.
//
// All blocking operations (Read / Write) are woken via channels
// (dataReady, done) instead of polling, so idle sessions consume
// zero CPU — critical for the 3s–600s send-interval scenarios this
// protocol targets.
package arq

import (
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/chainreactors/rem/x/utils"
)

// timeoutError 实现net.Error接口
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

// ARQSession 简化的ARQ会话，专注于低频通信
type ARQSession struct {
	conn       net.PacketConn // 底层PacketConn
	remoteAddr net.Addr       // 远程地址
	arq        *ARQ           // ARQ协议核心
	readBuffer *utils.Buffer  // 读缓冲区

	// 配置参数
	defaultTimeout time.Duration // 默认超时时间
	readFromConn   bool          // true=独占conn自行读取, false=由listener的monitor分发数据

	// 生命周期回调
	onClose func() // Close()时调用，用于通知listener清理

	// 原子操作控制
	closed int32 // 0=open, 1=closed
	rdNano int64 // 读超时纳秒时间戳
	wdNano int64 // 写超时纳秒时间戳

	// dataReady signals Read() that new data was written to readBuffer
	dataReady chan struct{}
	// inputNotify signals backgroundLoop (listener mode) that arq.Input() was called
	inputNotify chan struct{}
	// done is closed when the session closes, waking all blocked operations
	done chan struct{}
}

// NewARQSession 创建基于PacketConn的ARQ会话（独占模式，自行从conn读取数据）
func NewARQSession(conn net.PacketConn, remoteAddr net.Addr, mtu int, timeout time.Duration) *ARQSession {
	return NewARQSessionWithConfig(conn, remoteAddr, ARQConfig{
		MTU:     mtu,
		Timeout: int(timeout.Milliseconds()),
	})
}

// NewARQSessionWithConfig 创建基于PacketConn的ARQ会话，支持完整配置
func NewARQSessionWithConfig(conn net.PacketConn, remoteAddr net.Addr, cfg ARQConfig) *ARQSession {
	return newARQSessionWithConfig(conn, remoteAddr, cfg, true)
}

// newARQSession 内部构造函数（兼容旧调用）
func newARQSession(conn net.PacketConn, remoteAddr net.Addr, mtu int, timeout time.Duration, readFromConn bool) *ARQSession {
	return newARQSessionWithConfig(conn, remoteAddr, ARQConfig{
		MTU:     mtu,
		Timeout: int(timeout.Milliseconds()),
	}, readFromConn)
}

// newARQSessionWithConfig 内部构造函数
// readFromConn: true=独占conn自行读取（客户端）, false=由listener的monitor分发（服务端）
func newARQSessionWithConfig(conn net.PacketConn, remoteAddr net.Addr, cfg ARQConfig, readFromConn bool) *ARQSession {
	if cfg.MTU <= 0 {
		cfg.MTU = ARQ_MTU
	}

	// readBuffer 容量: MTU * WND_SIZE，但上限 16MB 避免大 MTU 浪费内存
	readBufSize := cfg.MTU * ARQ_WND_SIZE
	const maxReadBufSize = 16 * 1024 * 1024 // 16MB
	if readBufSize > maxReadBufSize {
		readBufSize = maxReadBufSize
	}

	sess := &ARQSession{
		conn:           conn,
		remoteAddr:     remoteAddr,
		readBuffer:     utils.NewBuffer(readBufSize),
		defaultTimeout: time.Duration(cfg.Timeout) * time.Millisecond,
		readFromConn:   readFromConn,
		dataReady:      make(chan struct{}, 1),
		inputNotify:    make(chan struct{}, 1),
		done:           make(chan struct{}),
	}

	// 创建ARQ协议实例
	sess.arq = NewARQWithConfig(1, func(data []byte) {
		sess.conn.WriteTo(data, sess.remoteAddr)
	}, cfg)

	// 启动后台处理: updateLoop独立运行ARQ状态机，不受readBuffer阻塞影响
	go sess.updateLoop()
	go sess.backgroundLoop()

	return sess
}

// Read 实现net.Conn接口
func (s *ARQSession) Read(b []byte) (n int, err error) {
	for {
		if atomic.LoadInt32(&s.closed) != 0 {
			return 0, io.EOF
		}

		// 检查读超时
		if d := atomic.LoadInt64(&s.rdNano); d > 0 && time.Now().UnixNano() > d {
			return 0, &timeoutError{}
		}

		// 尝试从缓冲区读取
		n, err = s.readBuffer.Read(b)
		if err != io.EOF {
			return n, err
		}

		// buffer为空，等待通知或超时
		d := atomic.LoadInt64(&s.rdNano)
		if d > 0 {
			remaining := time.Duration(d - time.Now().UnixNano())
			if remaining <= 0 {
				return 0, &timeoutError{}
			}
			timer := time.NewTimer(remaining)
			select {
			case <-s.dataReady:
				timer.Stop()
			case <-timer.C:
			case <-s.done:
			}
		} else {
			// 无超时：等待数据通知或关闭信号
			select {
			case <-s.dataReady:
			case <-s.done:
			}
		}
	}
}

// Write 实现net.Conn接口
func (s *ARQSession) Write(b []byte) (n int, err error) {
	if atomic.LoadInt32(&s.closed) != 0 {
		return 0, io.ErrClosedPipe
	}

	// 检查写超时
	deadline := atomic.LoadInt64(&s.wdNano)
	if deadline > 0 && time.Now().UnixNano() > deadline {
		return 0, &timeoutError{}
	}

	// Send buffer backpressure: block when send queue is full (like TCP)
	for s.arq.WaitSnd() >= ARQ_WND_SIZE*4 {
		if atomic.LoadInt32(&s.closed) != 0 {
			return 0, io.ErrClosedPipe
		}
		d := atomic.LoadInt64(&s.wdNano)
		if d > 0 && time.Now().UnixNano() > d {
			return 0, &timeoutError{}
		}
		select {
		case <-s.done:
			return 0, io.ErrClosedPipe
		case <-time.After(5 * time.Millisecond):
		}
	}

	s.arq.Send(b)
	return len(b), nil
}

// Close 关闭会话
func (s *ARQSession) Close() error {
	if atomic.CompareAndSwapInt32(&s.closed, 0, 1) {
		close(s.done)       // wake all blocked Read/Write immediately
		s.readBuffer.Close()
		s.signalDataReady()
		if s.onClose != nil {
			s.onClose()
		}
		if s.readFromConn {
			s.conn.Close()
		}
	}
	return nil
}

// LocalAddr 返回本地地址
func (s *ARQSession) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

// RemoteAddr 返回远程地址
func (s *ARQSession) RemoteAddr() net.Addr {
	return s.remoteAddr
}

// SetDeadline 设置读写超时
func (s *ARQSession) SetDeadline(t time.Time) error {
	nano := int64(0)
	if !t.IsZero() {
		nano = t.UnixNano()
	}
	atomic.StoreInt64(&s.rdNano, nano)
	atomic.StoreInt64(&s.wdNano, nano)
	s.signalDataReady() // wake blocked Read() to re-check deadline
	return nil
}

// SetReadDeadline 设置读超时
func (s *ARQSession) SetReadDeadline(t time.Time) error {
	nano := int64(0)
	if !t.IsZero() {
		nano = t.UnixNano()
	}
	atomic.StoreInt64(&s.rdNano, nano)
	s.signalDataReady() // wake blocked Read() to re-check deadline
	return nil
}

// SetWriteDeadline 设置写超时
func (s *ARQSession) SetWriteDeadline(t time.Time) error {
	nano := int64(0)
	if !t.IsZero() {
		nano = t.UnixNano()
	}
	atomic.StoreInt64(&s.wdNano, nano)
	return nil
}

// isTimeoutErr reports whether the error is a timeout
func isTimeoutErr(err error) bool {
	if ne, ok := err.(net.Error); ok {
		return ne.Timeout()
	}
	return false
}

// signalDataReady sends a non-blocking signal to wake Read()
func (s *ARQSession) signalDataReady() {
	select {
	case s.dataReady <- struct{}{}:
	default:
	}
}

// NotifyInput wakes the backgroundLoop (listener mode) after arq.Input() is called.
// Called by the listener's packetInput to replace polling with event-driven delivery.
func (s *ARQSession) NotifyInput() {
	select {
	case s.inputNotify <- struct{}{}:
	default:
	}
}

// updateLoop 独立运行ARQ状态机(flush/cleanup)，不受readBuffer阻塞影响
func (s *ARQSession) updateLoop() {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		<-ticker.C
		if atomic.LoadInt32(&s.closed) != 0 {
			return
		}
		s.arq.Update()
	}
}

// backgroundLoop 负责数据I/O: 从conn读取数据并交付到readBuffer
func (s *ARQSession) backgroundLoop() {
	var buf []byte
	if s.readFromConn {
		buf = make([]byte, s.arq.mtu)
	}

	for {
		if atomic.LoadInt32(&s.closed) != 0 {
			return
		}

		if !s.readFromConn {
			// 由listener管理：等待Input通知后检查ARQ是否有待交付数据
			if data := s.arq.Recv(); len(data) > 0 {
				if _, err := s.readBuffer.Write(data); err != nil {
					return
				}
				s.signalDataReady()
			} else {
				select {
				case <-s.inputNotify:
				case <-s.done:
					return
				}
			}
			continue
		}

		// 独占模式：自行从conn读取数据
		s.conn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
		n, addr, err := s.conn.ReadFrom(buf)
		if err == nil && n > 0 && addr.String() == s.remoteAddr.String() {
			s.arq.Input(buf[:n])
		} else if err != nil && !isTimeoutErr(err) {
			// Permanent error (conn closed by peer) — close session
			s.Close()
			return
		}

		// 交付接收到的数据
		if data := s.arq.Recv(); len(data) > 0 {
			if _, err := s.readBuffer.Write(data); err != nil {
				return
			}
			s.signalDataReady()
		}
	}
}
