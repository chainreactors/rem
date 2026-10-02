// ARQSession wraps the ARQ protocol into a standard net.Conn interface.
//
// Architecture (two goroutines per session):
//
//   - updateLoop:  independent ticker driving the ARQ state machine
//     (flush / retransmission budget checks). Never blocked by application-layer I/O.
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
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/chainreactors/rem/x/utils"
)

const readBufferDeliverChunk = 64 * 1024

// sessionTickInterval controls the ARQ state-machine tick (flush, RTO check)
// and doubles as the polling timeout for conn reads and close drain.
// Kept as a var so extreme-scenario tests can override it.
var sessionTickInterval = 10 * time.Millisecond

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
	closeDrainTimeout    time.Duration
	closeAckFlushTimeout time.Duration
	readFromConn         bool // true=独占conn自行读取, false=由listener的monitor分发数据

	// 生命周期回调
	onClose func() // Close()时调用，用于通知listener清理

	// 原子操作控制
	closed     int32 // 0=open, 1=closing/closed for user-facing ops
	finalized  int32 // 0=background loops running, 1=transport fully closed
	localClose int32 // 1=Close() initiated locally; post-close I/O returns EOF/ErrClosedPipe
	localWrote int32 // 1=application has written stream data
	remoteDone int32 // 1=peer FIN has been delivered in-order
	rdNano     int64 // 读超时纳秒时间戳
	wdNano     int64 // 写超时纳秒时间戳
	closeErr   atomic.Value
	closeRet   atomic.Value
	writeErr   atomic.Value

	// dataReady signals Read() that new data was written to readBuffer
	dataReady chan struct{}
	// inputNotify signals backgroundLoop (listener mode) that arq.Input() was called
	inputNotify chan struct{}
	// done is closed when the session closes, waking all blocked operations
	done chan struct{}
	// finalDone is closed once transport resources are fully torn down.
	finalDone chan struct{}
}

// NewARQSession 创建基于PacketConn的ARQ会话（独占模式，自行从conn读取数据）
func NewARQSession(conn net.PacketConn, remoteAddr net.Addr, mtu int, _ time.Duration) *ARQSession {
	return NewARQSessionWithConfig(conn, remoteAddr, ARQConfig{
		MTU: mtu,
	})
}

// NewARQSessionWithConfig 创建基于PacketConn的ARQ会话，支持完整配置
func NewARQSessionWithConfig(conn net.PacketConn, remoteAddr net.Addr, cfg ARQConfig) *ARQSession {
	return newARQSessionWithConfig(conn, remoteAddr, cfg, true)
}

// newARQSession 内部构造函数（兼容旧调用）
func newARQSession(conn net.PacketConn, remoteAddr net.Addr, mtu int, _ time.Duration, readFromConn bool) *ARQSession {
	return newARQSessionWithConfig(conn, remoteAddr, ARQConfig{
		MTU: mtu,
	}, readFromConn)
}

// newARQSessionWithConfig 内部构造函数
// readFromConn: true=独占conn自行读取（客户端）, false=由listener的monitor分发（服务端）
func newARQSessionWithConfig(conn net.PacketConn, remoteAddr net.Addr, cfg ARQConfig, readFromConn bool) *ARQSession {
	if cfg.MTU <= 0 {
		cfg.MTU = ARQ_MTU
	}
	if cfg.RTO == 0 {
		cfg.RTO = ARQ_RTO
	}
	if cfg.MaxRetransmissions <= 0 {
		cfg.MaxRetransmissions = ARQ_MAX_RETRANS
	}

	// readBuffer 容量: MTU * WndSize，但上限 16MB 避免大 MTU 浪费内存
	wndSize := cfg.WndSize
	if wndSize <= 0 {
		wndSize = ARQ_WND_SIZE
	}
	readBufSize := cfg.MTU * wndSize
	const maxReadBufSize = 16 * 1024 * 1024 // 16MB
	if readBufSize > maxReadBufSize {
		readBufSize = maxReadBufSize
	}

	sess := &ARQSession{
		conn:                 conn,
		remoteAddr:           remoteAddr,
		readBuffer:           utils.NewBuffer(readBufSize),
		closeDrainTimeout:    closeDrainTimeoutForConfig(cfg),
		closeAckFlushTimeout: closeAckFlushTimeoutForConfig(cfg, remoteAddr),
		readFromConn:         readFromConn,
		dataReady:            make(chan struct{}, 1),
		inputNotify:          make(chan struct{}, 1),
		done:                 make(chan struct{}),
		finalDone:            make(chan struct{}),
	}

	// 创建ARQ协议实例
	sess.arq = NewARQWithConfig(func(data []byte) {
		if _, err := sess.conn.WriteTo(data, sess.remoteAddr); err != nil {
			sess.setWriteErr(err)
		}
	}, cfg)

	// 启动后台处理: updateLoop独立运行ARQ状态机，不受readBuffer阻塞影响
	go sess.updateLoop()
	go sess.backgroundLoop()

	return sess
}

var ErrCloseDrainTimeout = errors.New("arq session close drain timeout")

func closeDrainTimeoutForConfig(cfg ARQConfig) time.Duration {
	rto := time.Duration(cfg.RTO) * time.Millisecond
	if rto <= 0 {
		rto = time.Duration(ARQ_RTO) * time.Millisecond
	}
	retrans := cfg.MaxRetransmissions
	if retrans <= 0 {
		retrans = ARQ_MAX_RETRANS
	}
	return time.Duration(retrans+1) * rto
}

func closeAckFlushTimeoutForConfig(cfg ARQConfig, remoteAddr net.Addr) time.Duration {
	rto := time.Duration(cfg.RTO) * time.Millisecond
	if rto <= 0 {
		rto = time.Duration(ARQ_RTO) * time.Millisecond
	}
	timeout := rto
	if addr, ok := remoteAddr.(interface{ Interval() time.Duration }); ok {
		if interval := addr.Interval(); interval > 0 {
			if t := 10 * interval; t > timeout {
				timeout = t
			}
		}
	}
	if drain := closeDrainTimeoutForConfig(cfg); timeout > drain {
		timeout = drain
	}
	return timeout
}

// Read 实现net.Conn接口
func (s *ARQSession) Read(b []byte) (n int, err error) {
	for {
		// 尝试从缓冲区读取
		n, err = s.readBuffer.Read(b)
		if err != io.EOF {
			return n, err
		}

		if atomic.LoadInt32(&s.closed) != 0 {
			if err := s.sessionErr(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if atomic.LoadInt32(&s.remoteDone) != 0 {
			return 0, io.EOF
		}

		// 检查读超时
		if d := atomic.LoadInt64(&s.rdNano); d > 0 && time.Now().UnixNano() > d {
			return 0, &timeoutError{}
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
		if err := s.sessionErr(); err != nil {
			return 0, err
		}
		return 0, io.ErrClosedPipe
	}
	if err := s.arq.Err(); err != nil {
		s.abort(err)
		return 0, err
	}
	if err := s.writeFailure(); err != nil {
		s.abort(err)
		return 0, err
	}

	// 检查写超时
	deadline := atomic.LoadInt64(&s.wdNano)
	if deadline > 0 && time.Now().UnixNano() > deadline {
		return 0, &timeoutError{}
	}

	// 不做 backpressure — yamux sendLoop 是单 goroutine 串行写入，
	// 任何阻塞都会卡死所有 stream（包括 window update、ping/pong）。
	// ARQ 窗口（WND_SIZE）已经是天然的 in-flight 限制；
	// snd_queue 增长只消耗内存，由应用层（yamux flow control）控制上限。

	if err := s.arq.Queue(b); err != nil {
		s.abort(err)
		return 0, err
	}
	if len(b) > 0 {
		atomic.StoreInt32(&s.localWrote, 1)
	}
	return len(b), nil
}

// Close stops new writes immediately, then waits a bounded amount of time for a
// best-effort ARQ drain.
//
// Close is intentionally not a delivery barrier:
//   - if pending data drains within closeDrainTimeout, it returns nil
//   - if ARQ delivery fails while draining, it returns that delivery error
//   - if draining does not finish in time, it returns ErrCloseDrainTimeout
//   - once Close() returns, subsequent Read/Write behave like a normal closed conn
//     (EOF / io.ErrClosedPipe) rather than surfacing close-drain bookkeeping errors
func (s *ARQSession) Close() error {
	if !s.beginClose(nil) {
		if s.arq.WaitSnd() == 0 {
			return nil
		}
		return s.waitClose(s.closeDrainTimeout, true)
	}

	s.syncRemoteDone()
	queuedFIN, err := s.arq.queueFINIfNeeded(atomic.LoadInt32(&s.localWrote) != 0)
	if err != nil {
		s.setCloseResult(err)
		s.finalClose()
		return s.closeResult()
	}
	if s.arq.WaitSnd() == 0 {
		if !queuedFIN && atomic.LoadInt32(&s.remoteDone) != 0 {
			return s.waitClose(s.closeAckFlushTimeout, false)
		}
		s.finalClose()
		return s.closeResult()
	}
	go s.drainPendingWrites()
	return s.waitClose(s.closeDrainTimeout, true)
}

func (s *ARQSession) abort(err error) error {
	if atomic.LoadInt32(&s.finalized) != 0 {
		return s.sessionErr()
	}
	s.beginClose(err)
	s.finalClose()
	return s.sessionErr()
}

// LocalAddr 返回本地地址
func (s *ARQSession) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

// RemoteAddr 返回远程地址
func (s *ARQSession) RemoteAddr() net.Addr {
	return s.remoteAddr
}

func (s *ARQSession) Stats() ARQStats {
	return s.arq.Stats()
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

func (s *ARQSession) syncRemoteDone() bool {
	if !s.arq.remoteDone() {
		return false
	}
	if atomic.CompareAndSwapInt32(&s.remoteDone, 0, 1) {
		s.signalDataReady()
		return true
	}
	return false
}

// NotifyInput wakes the backgroundLoop (listener mode) after arq.Input() is called.
// Called by the listener's packetInput to replace polling with event-driven delivery.
func (s *ARQSession) NotifyInput() {
	select {
	case s.inputNotify <- struct{}{}:
	default:
	}
}

// updateLoop 独立运行ARQ状态机(flush/失败检测)，不受readBuffer阻塞影响
func (s *ARQSession) updateLoop() {
	ticker := time.NewTicker(sessionTickInterval)
	defer ticker.Stop()

	for {
		<-ticker.C
		if atomic.LoadInt32(&s.finalized) != 0 {
			return
		}
		if err := s.writeFailure(); err != nil {
			s.handleSessionFailure(err)
			return
		}
		s.arq.Update()
		if err := s.writeFailure(); err != nil {
			s.handleSessionFailure(err)
			return
		}
		if err := s.arq.Err(); err != nil {
			s.handleSessionFailure(err)
			return
		}
	}
}

// backgroundLoop 负责数据I/O: 从conn读取数据并交付到readBuffer
func (s *ARQSession) backgroundLoop() {
	var buf []byte
	if s.readFromConn {
		buf = make([]byte, s.arq.mtu)
	}

	for {
		if atomic.LoadInt32(&s.finalized) != 0 {
			return
		}
		if err := s.writeFailure(); err != nil {
			s.handleSessionFailure(err)
			return
		}

		if !s.readFromConn {
			// 由listener管理：等待Input通知后检查ARQ是否有待交付数据
			delivered := false
			if data := s.arq.Recv(); len(data) > 0 {
				if err := s.deliverData(data); err != nil {
					return
				}
				delivered = true
			}
			if s.syncRemoteDone() {
				delivered = true
			}
			if !delivered {
				select {
				case <-s.inputNotify:
				case <-s.done:
					return
				}
			}
			continue
		}

		// 独占模式：自行从conn读取数据
		s.conn.SetReadDeadline(time.Now().Add(sessionTickInterval))
		n, addr, err := s.conn.ReadFrom(buf)
		if err == nil && n > 0 && addr.String() == s.remoteAddr.String() {
			s.arq.Input(buf[:n])
		} else if err != nil && !isTimeoutErr(err) {
			if atomic.LoadInt32(&s.finalized) != 0 {
				return
			}
			if errors.Is(err, net.ErrClosed) {
				s.handleSessionFailure(nil)
				return
			}
			s.handleSessionFailure(err)
			return
		}

		// 交付接收到的数据
		if data := s.arq.Recv(); len(data) > 0 {
			if err := s.deliverData(data); err != nil {
				return
			}
		}
		// Publish EOF only after any data made deliverable by the same Input()
		// has been copied into readBuffer. FIN can arrive out of transport order
		// and be released together with the final missing DATA segment.
		s.syncRemoteDone()
	}
}

func (s *ARQSession) deliverData(data []byte) error {
	for len(data) > 0 {
		n := readBufferDeliverChunk
		if n > len(data) {
			n = len(data)
		}
		if capacity := s.readBuffer.Cap(); capacity > 0 {
			avail := capacity - s.readBuffer.Size()
			if avail <= 0 {
				s.signalDataReady()
				avail = capacity
			}
			if n > avail {
				n = avail
			}
		}
		if n <= 0 {
			n = 1
		}
		if _, err := s.readBuffer.Write(data[:n]); err != nil {
			return err
		}
		s.signalDataReady()
		data = data[n:]
	}
	return nil
}

func (s *ARQSession) sessionErr() error {
	if v := s.closeErr.Load(); v != nil {
		return v.(error)
	}
	return nil
}

func (s *ARQSession) setCloseErr(err error) {
	if err == nil || s.sessionErr() != nil {
		return
	}
	s.closeErr.Store(err)
}

func (s *ARQSession) closeResult() error {
	if v := s.closeRet.Load(); v != nil {
		return v.(error)
	}
	return s.sessionErr()
}

func (s *ARQSession) setCloseResult(err error) {
	if err == nil || s.closeResult() != nil {
		return
	}
	s.closeRet.Store(err)
}

func (s *ARQSession) setWriteErr(err error) {
	if err == nil || s.writeFailure() != nil {
		return
	}
	s.writeErr.Store(err)
}

func (s *ARQSession) writeFailure() error {
	if v := s.writeErr.Load(); v != nil {
		return v.(error)
	}
	return nil
}

func (s *ARQSession) beginClose(err error) bool {
	if err == nil {
		atomic.StoreInt32(&s.localClose, 1)
	} else {
		s.setCloseErr(err)
	}
	if atomic.CompareAndSwapInt32(&s.closed, 0, 1) {
		close(s.done) // wake all blocked Read/Write immediately
		s.signalDataReady()
		return true
	}
	return false
}

func (s *ARQSession) finalClose() {
	if atomic.CompareAndSwapInt32(&s.finalized, 0, 1) {
		close(s.finalDone)
		s.readBuffer.Close()
		s.signalDataReady()
		if s.onClose != nil {
			s.onClose()
		}
		if s.readFromConn {
			s.conn.Close()
		}
	}
}

func (s *ARQSession) handleSessionFailure(err error) {
	if atomic.LoadInt32(&s.localClose) != 0 {
		if err != nil && s.arq.WaitSnd() != 0 {
			s.setCloseResult(err)
		}
		s.finalClose()
		return
	}
	s.abort(err)
}

// waitClose waits up to timeout for the session to finalize. If
// reportTimeout is true, ErrCloseDrainTimeout is recorded on expiry
// (used for the drain path); otherwise the timer just triggers finalClose
// (used for the ack-flush path where timeout is expected, not an error).
func (s *ARQSession) waitClose(timeout time.Duration, reportTimeout bool) error {
	if atomic.LoadInt32(&s.finalized) != 0 {
		return s.closeResult()
	}
	if timeout <= 0 {
		if reportTimeout {
			<-s.finalDone
			return s.closeResult()
		}
		s.finalClose()
		return s.closeResult()
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-s.finalDone:
		return s.closeResult()
	case <-timer.C:
		if reportTimeout {
			s.setCloseResult(ErrCloseDrainTimeout)
		}
		s.finalClose()
		return s.closeResult()
	}
}

func (s *ARQSession) drainPendingWrites() {
	ticker := time.NewTicker(sessionTickInterval)
	defer ticker.Stop()

	for {
		if err := s.arq.Err(); err != nil {
			s.setCloseResult(err)
			s.finalClose()
			return
		}
		if s.arq.WaitSnd() == 0 {
			s.finalClose()
			return
		}
		if atomic.LoadInt32(&s.finalized) != 0 {
			return
		}
		<-ticker.C
	}
}
