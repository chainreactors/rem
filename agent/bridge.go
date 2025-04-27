package agent

import (
	"context"
	"fmt"
	"github.com/chainreactors/rem/protocol/core"
	"net"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
	"google.golang.org/protobuf/proto"
)

// NewBridgeWithConn user <-> console 交互
// conn client <-> outbound
func NewBridgeWithConn(agent *Agent, conn net.Conn, control *message.Control) (*Bridge, error) {
	agent.connIndex++
	ctx, cancel := context.WithCancel(agent.ctx)
	bridge := &Bridge{
		id:     agent.connIndex, // user 发起的端口为唯一id
		remote: cio.NewLimitedConn(conn),
		buf:    cio.NewBufferContext(ctx, core.MaxPacketSize),
		sendCh: agent.sendCh,
		ctx:    ctx,
		cancel: cancel,
		agent:  agent,
	}
	if agent.Redirect == control.Destination {
		bridge.destination = control.Destination
		bridge.source = control.Source
	} else {
		bridge.destination = control.Source
		bridge.source = control.Destination
	}

	bridge.send(&message.ConnStart{
		ID:          bridge.id,
		Source:      bridge.source,
		Destination: bridge.destination,
	})

	// 监听stopCh, 链接结束则在连接池中-1
	go func() {
		select {
		case <-bridge.ctx.Done():
			agent.connCount--
		case <-agent.ctx.Done():
			bridge.Close()
		}
	}()

	return bridge, nil
}

// NewBridgeWithMsg client <-> console
func NewBridgeWithMsg(agent *Agent, msg *message.ConnStart) (*Bridge, error) {
	// 通过connstart建立的bridge
	ctx, cancel := context.WithCancel(agent.ctx)
	bridge := &Bridge{
		id:          msg.ID,
		sendCh:      agent.sendCh,
		ctx:         ctx,
		cancel:      cancel,
		destination: msg.Source,
		source:      agent.ID,
		buf:         cio.NewBufferContext(ctx, core.MaxPacketSize),
		agent:       agent,
	}

	// stop
	go func() {
		select {
		case <-bridge.ctx.Done():
			agent.connCount--
		case <-agent.ctx.Done():
			bridge.Close()
		}
	}()

	return bridge, nil
}

// Bridge
type Bridge struct {
	id          uint64
	source      string
	destination string
	ctx         context.Context
	cancel      context.CancelFunc
	sendCh      *cio.Channel
	buf         *cio.Buffer // from rem
	remote      *cio.LimitedConn
	recvSum     int64
	sendSum     int64
	closed      bool
	agent       *Agent
}

func (b *Bridge) Handler(agent *Agent) {
	if agent.Outbound != nil {
		go func() {
			outbound, err := agent.Outbound.Handle(b, agent.Conn)
			if err != nil {
				b.Log("outbound", logs.DebugLevel, "error %s", err.Error())
				return
			}
			b.remote = cio.NewLimitedConn(outbound)
			go b.monitor()
			in, out, errors := cio.JoinWithError(b, b.remote)
			b.Log("outbound", logs.DebugLevel, "in: %d, out: %d, errors: %v", in, out, errors)
		}()
	}
	if agent.Inbound != nil {
		go func() {
			inbound, err := agent.Inbound.Relay(b.remote, b)
			if err != nil {
				b.Log("inbound", logs.DebugLevel, "error %s", err.Error())
				b.Close()
				return
			}
			go b.monitor()
			in, out, errors := cio.JoinWithError(b, inbound)
			b.Log("inbound", logs.DebugLevel, "in: %d bytes, out: %d bytes, errors: %v", in, out, errors)
		}()
	}
}

// Read:  从rem读数据write到remote
func (b *Bridge) Read(p []byte) (int, error) {
	select {
	case <-b.ctx.Done():
		return 0, fmt.Errorf("bridge closed when read")
	default:
	}

	n, err := b.buf.Read(p)
	if n != 0 {
		b.Log("read", logs.DebugLevel, "read %d bytes", n)
	}
	return n, err
}

// Write: 从remote读数据write到rem
func (b *Bridge) Write(p []byte) (int, error) {
	select {
	case <-b.ctx.Done():
		return 0, fmt.Errorf("bridge closed when write")
	default:
	}

	length := len(p)
	data := make([]byte, length)
	copy(data, p)
	var err error
	err = b.send(&message.Packet{ID: b.id, Data: data})
	if err != nil {
		return 0, err
	}
	b.sendSum += int64(length)
	b.Log("write", logs.DebugLevel, "write %d bytes", len(p))
	return len(p), nil
}

func (b *Bridge) send(msg proto.Message) error {
	//if b.destination == b.source {
	//	return b.sendCh.Send(msg)
	//} else {
	//	switch m := msg.(type) {
	//	case *message.Packet:
	//		return b.sendCh.Send(&message.Redirect{Destination: b.destination, Source: b.source, Msg: &message.Redirect_Packet{Packet: m}})
	//	case *message.ConnStart:
	//		return b.sendCh.Send(&message.Redirect{Destination: b.destination, Source: b.source, Msg: &message.Redirect_Start{Start: m}})
	//	case *message.ConnEnd:
	//		return b.sendCh.Send(&message.Redirect{Destination: b.destination, Source: b.source, Msg: &message.Redirect_End{End: m}})
	//	default:
	//		return nil
	//	}
	//}
	return b.sendCh.Send(b.id, message.Wrap(b.source, b.destination, msg))
}

func (b *Bridge) monitor() {
	for !b.closed {
		time.Sleep(monitorInterval * time.Second)
		b.Log("monitor", logs.DebugLevel, "%d, read: %d/%d bytes; write: %d/%d", b.id,
			b.remote.ReadCount, b.sendSum,
			b.remote.WriteCount, b.recvSum)
	}
}

func (b *Bridge) Close() error {
	if b.closed {
		return nil
	}
	if b.remote != nil {
		// safe close
	safeClose:
		for {
			select {
			case <-b.ctx.Done():
				return nil
			case <-time.After(60 * time.Second):
				b.Log("close", logs.DebugLevel, "timeout waiting for close, : %d, read: %d, expect: %d", b.buf.Size(), b.buf.WriteCount, b.recvSum)
				break safeClose
			default:
				// 安全关闭的要求必须为 remote conn 已被关闭, 读取的数据均成功发送, 缓冲区的数据均成功发送
				if size := b.buf.Size(); size != 0 {
					b.Log("close", logs.DebugLevel, "waiting for buf write to remote conn %d", size)
					time.Sleep(100 * time.Millisecond)
					continue
				} else if b.recvSum != b.buf.WriteCount {
					b.Log("close", logs.DebugLevel, "waiting for data write to remote conn %d != %d", b.recvSum, b.buf.WriteCount)
					time.Sleep(100 * time.Millisecond)
					continue
				} else if size := b.sendCh.GetPendingCount(b.id); size != 0 {
					b.Log("close", logs.DebugLevel, "waiting for send to peer %d packets", b.sendCh.GetPendingCount(b.id))
					time.Sleep(100 * time.Millisecond)
					continue
				} else {
					b.Log("finish", logs.DebugLevel, "read: %d/%d; write: %d/%d bytes, buf: %d",
						b.sendSum, b.remote.ReadCount,
						b.recvSum, b.buf.ReadCount,
						b.buf.Size())
					break safeClose
				}
			}
		}
		b.remote.Close()
	}
	b.closed = true
	b.send(&message.ConnEnd{ID: b.id})
	b.buf.Close()
	b.cancel()
	return nil
}

func (b *Bridge) Log(part string, level logs.Level, msg string, s ...interface{}) {
	utils.Log.FLogf(b.agent.log, level, "[bridge.%d.%s] %s", b.id, part, fmt.Sprintf(msg, s...))
}
