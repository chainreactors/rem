package kcp

import (
	"context"
	"io"
)

type KCPConn struct {
	*KCPSession
	ctx    context.Context
	cancel context.CancelFunc
}

func RadicalKCPConfig(conn *KCPSession) {
	conn.SetWriteDelay(false)
	conn.SetWindowSize(1024, 1024)
	conn.SetReadBuffer(1024 * 1024)
	conn.SetWriteBuffer(1024 * 1024)
	conn.SetNoDelay(1, 100, 2, 1)
	conn.SetMtu(1400)
	conn.SetACKNoDelay(true)
}

func HTTPKCPConfig(conn *KCPSession) {
	conn.SetWriteDelay(true)
	//conn.SetWindowSize(16, 16)
	conn.SetReadBuffer(1024 * 1024)
	conn.SetWriteBuffer(1024 * 1024)
	conn.SetNoDelay(0, 100, 10, 0)
	conn.SetMtu(mtuLimit - 16*1024)
	conn.SetACKNoDelay(false)
}

func DNSKCPConfig(conn *KCPSession) {
	conn.SetWriteDelay(false)      // 禁用写延迟，立即发送数据
	conn.SetWindowSize(16, 16)     // 优化窗口大小
	conn.SetReadBuffer(16 * 1024)  // 减小读缓冲区
	conn.SetWriteBuffer(16 * 1024) // 减小写缓冲区
	conn.SetNoDelay(0, 100, 10, 0)
	conn.SetMtu(130)         // 调整MTU大小
	conn.SetACKNoDelay(true) // 启用ACK立即回复
}

// NewKCPConn 创建新的KCP连接包装器
func NewKCPConn(conn *KCPSession, confn func(*KCPSession)) *KCPConn {
	ctx, cancel := context.WithCancel(context.Background())
	confn(conn)
	return &KCPConn{
		KCPSession: conn,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Read 重写Read方法，返回可用数据而不阻塞等待填满缓冲区
func (k *KCPConn) Read(p []byte) (n int, err error) {
	select {
	case <-k.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		return k.KCPSession.Read(p)
	}
}

// Write 重写Write方法，添加超时控制
func (k *KCPConn) Write(p []byte) (n int, err error) {
	select {
	case <-k.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		return k.KCPSession.Write(p)
	}
}

// Close 关闭连接
func (k *KCPConn) Close() error {
	k.cancel()
	return k.KCPSession.Close()
}
