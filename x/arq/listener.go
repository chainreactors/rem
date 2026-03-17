// ARQListener multiplexes a single PacketConn into multiple ARQSessions,
// one per remote address. A background monitor goroutine reads all
// incoming packets and dispatches them to the corresponding session.
//
// Sessions created by the listener run in "listener mode" — they do
// not read from the PacketConn themselves but receive data via Input().
package arq

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	acceptBacklog = 128
)

// ARQListener 类似KCP Listener，管理多个ARQSession
type ARQListener struct {
	conn        net.PacketConn         // 底层PacketConn
	ownConn     bool                   // 是否拥有conn的所有权
	sessions    map[string]*ARQSession // 按地址管理的会话
	sessionLock sync.RWMutex           // 会话管理锁
	chAccepts   chan *ARQSession       // Accept()队列
	chClosed    chan net.Addr          // 会话关闭通知
	die         chan struct{}          // 监听器关闭信号
	dieOnce     sync.Once              // 确保只关闭一次

	// ARQ配置
	arqConfig ARQConfig

	// 原子操作控制
	rd atomic.Value // read deadline
}

// ServeConn 将PacketConn包装为ARQListener，类似KCP的ServeConn
func ServeConn(conn net.PacketConn, mtu int, timeout time.Duration) (*ARQListener, error) {
	return ServeConnWithConfig(conn, ARQConfig{
		MTU:     mtu,
		Timeout: int(timeout.Milliseconds()),
	}, false)
}

// ServeConnWithOwnership 创建ARQListener并拥有conn的所有权
func ServeConnWithOwnership(conn net.PacketConn, mtu int, timeout time.Duration) (*ARQListener, error) {
	return ServeConnWithConfig(conn, ARQConfig{
		MTU:     mtu,
		Timeout: int(timeout.Milliseconds()),
	}, true)
}

// ServeConnWithConfig 使用完整配置创建ARQListener
func ServeConnWithConfig(conn net.PacketConn, cfg ARQConfig, ownConn bool) (*ARQListener, error) {
	if cfg.MTU <= 0 {
		cfg.MTU = ARQ_MTU
	}
	if cfg.RTO == 0 {
		cfg.RTO = ARQ_RTO
	}

	l := &ARQListener{
		conn:      conn,
		ownConn:   ownConn,
		sessions:  make(map[string]*ARQSession),
		chAccepts: make(chan *ARQSession, acceptBacklog),
		chClosed:  make(chan net.Addr, acceptBacklog),
		die:       make(chan struct{}),
		arqConfig: cfg,
	}

	go l.monitor()
	return l, nil
}

// monitor 监控数据包输入和会话管理
func (l *ARQListener) monitor() {
	buf := make([]byte, l.arqConfig.MTU)

	for {
		select {
		case <-l.die:
			return
		case addr := <-l.chClosed:
			l.sessionLock.Lock()
			delete(l.sessions, addr.String())
			l.sessionLock.Unlock()
		default:
			// 设置短超时以避免阻塞
			n, addr, err := l.conn.ReadFrom(buf)
			if err != nil {
				// 超时错误是正常的，其他错误需要处理
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				// 其他错误暂时忽略，继续监听
				continue
			}

			if n > 0 {
				l.packetInput(buf[:n], addr)
			} else {
				time.Sleep(time.Millisecond)
			}
		}
	}
}

// packetInput 处理输入的数据包，类似KCP的packetInput
func (l *ARQListener) packetInput(data []byte, addr net.Addr) {
	addrStr := addr.String()

	l.sessionLock.RLock()
	session, exists := l.sessions[addrStr]
	l.sessionLock.RUnlock()

	if exists {
		// 现有会话，直接输入数据
		session.arq.Input(data)
		session.NotifyInput()
	} else {
		// 新会话，创建并加入管理
		if len(l.chAccepts) < cap(l.chAccepts) {
			session := l.createSession(addr)
			session.arq.Input(data)
			session.NotifyInput()

			l.sessionLock.Lock()
			l.sessions[addrStr] = session
			l.sessionLock.Unlock()

			select {
			case l.chAccepts <- session:
			default:
				// Accept队列满了，关闭新会话
				session.Close()
			}
		}
	}
}

// createSession 创建新的ARQSession（listener管理模式，不从conn读取）
func (l *ARQListener) createSession(remoteAddr net.Addr) *ARQSession {
	sess := newARQSessionWithConfig(l.conn, remoteAddr, l.arqConfig, false)
	sess.onClose = func() {
		select {
		case l.chClosed <- remoteAddr:
		case <-l.die:
		}
	}
	return sess
}

// Accept 实现net.Listener接口
func (l *ARQListener) Accept() (net.Conn, error) {
	var timeout time.Time
	if deadline, ok := l.rd.Load().(time.Time); ok && !deadline.IsZero() {
		timeout = deadline
	}

	select {
	case <-l.die:
		return nil, &net.OpError{Op: "accept", Net: "arq", Err: net.ErrClosed}
	case session := <-l.chAccepts:
		return session, nil
	default:
		if !timeout.IsZero() && time.Now().After(timeout) {
			return nil, &timeoutError{}
		}
		// 阻塞等待，如果设置了 deadline 则同时监听超时
		if timeout.IsZero() {
			select {
			case <-l.die:
				return nil, &net.OpError{Op: "accept", Net: "arq", Err: net.ErrClosed}
			case session := <-l.chAccepts:
				return session, nil
			}
		}
		timer := time.NewTimer(time.Until(timeout))
		defer timer.Stop()
		select {
		case <-l.die:
			return nil, &net.OpError{Op: "accept", Net: "arq", Err: net.ErrClosed}
		case session := <-l.chAccepts:
			return session, nil
		case <-timer.C:
			return nil, &timeoutError{}
		}
	}
}

// Close 关闭监听器
func (l *ARQListener) Close() error {
	var err error
	l.dieOnce.Do(func() {
		close(l.die)

		// 关闭所有会话
		l.sessionLock.Lock()
		for _, session := range l.sessions {
			session.Close()
		}
		l.sessions = make(map[string]*ARQSession)
		l.sessionLock.Unlock()

		// 如果拥有conn，则关闭它
		if l.ownConn && l.conn != nil {
			err = l.conn.Close()
		}
	})
	return err
}

// Addr 返回监听地址
func (l *ARQListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

// SetDeadline 设置Accept的超时
func (l *ARQListener) SetDeadline(t time.Time) error {
	l.rd.Store(t)
	return nil
}

// SetReadDeadline 设置Accept的读超时
func (l *ARQListener) SetReadDeadline(t time.Time) error {
	return l.SetDeadline(t)
}

// SetWriteDeadline 设置写超时（ARQListener不支持写操作）
func (l *ARQListener) SetWriteDeadline(t time.Time) error {
	return nil
}
