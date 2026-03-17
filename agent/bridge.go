package agent

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/message"
	_ "github.com/chainreactors/rem/protocol/tunnel/tcp"
	"github.com/chainreactors/rem/x/utils"
)

// NewBridgeWithConn creates a bridge from an accepted inbound connection.
// Sends BridgeOpen on stream 0, then the caller opens a yamux data stream.
func NewBridgeWithConn(agent *Agent, conn net.Conn, control *message.Control) (*Bridge, error) {
	// Use shared bridge ID counter to avoid ID collisions between parent and forked agents.
	counter := &agent.connIndex
	if agent.sharedBridgeID != nil {
		counter = agent.sharedBridgeID
	}
	id := atomic.AddUint64(counter, 1)
	ctx, cancel := context.WithCancel(agent.ctx)
	bridge := &Bridge{
		id:     id,
		remote: conn,
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

	// Send BridgeOpen on stream 0
	agent.Send(&message.BridgeOpen{
		ID:          bridge.id,
		Source:      bridge.source,
		Destination: bridge.destination,
	})

	// Open yamux data stream from ConnHub-selected conn and write bridge ID header.
	stream, _, err := agent.openBridgeStream(bridge.id)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("yamux open: %w", err)
	}
	if err := writeStreamID(stream, bridge.id); err != nil {
		stream.Close()
		cancel()
		return nil, fmt.Errorf("write bridge ID: %w", err)
	}
	bridge.stream = stream

	go func() {
		select {
		case <-bridge.ctx.Done():
			atomic.AddInt64(&agent.connCount, -1)
		case <-agent.ctx.Done():
			bridge.Close()
		}
	}()

	return bridge, nil
}

// Bridge represents a tunneled connection. With yamux, the bridge simply
// holds a yamux data stream and an optional remote (outbound) conn.
// Data flows directly: remote <-> yamux stream (via JoinWithError).
type Bridge struct {
	id          uint64
	source      string
	destination string
	ctx         context.Context
	cancel      context.CancelFunc
	stream      net.Conn // yamux data stream
	remote      net.Conn // external/outbound conn (set by NewBridgeWithConn or Handler)
	closed      atomic.Bool
	agent       *Agent
}

func (b *Bridge) Handler(agent *Agent) {
	if agent.Outbound != nil {
		go func() {
			outbound, err := agent.Outbound.Handle(b.stream, agent.Conn)
			if err != nil {
				b.Log("outbound", logs.DebugLevel, "error %s", err.Error())
				b.Close()
				return
			}
			in, out, errors := cio.JoinWithError(b.stream, outbound)
			b.Log("outbound", logs.DebugLevel, "in: %d, out: %d, errors: %v", in, out, errors)
			b.Close()
		}()
	}
	if agent.Inbound != nil {
		go func() {
			inbound, err := agent.Inbound.Relay(b.remote, b.stream)
			if err != nil {
				b.Log("inbound", logs.DebugLevel, "error %s", err.Error())
				b.Close()
				return
			}
			in, out, errors := cio.JoinWithError(b.stream, inbound)
			b.Log("inbound", logs.DebugLevel, "in: %d, out: %d, errors: %v", in, out, errors)
			b.Close()
		}()
	}
	// If neither inbound nor outbound (e.g. direct remote), join stream <-> remote
	if agent.Outbound == nil && agent.Inbound == nil && b.remote != nil {
		go func() {
			in, out, errors := cio.JoinWithError(b.stream, b.remote)
			b.Log("direct", logs.DebugLevel, "in: %d, out: %d, errors: %v", in, out, errors)
			b.Close()
		}()
	}
}

func (b *Bridge) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	b.Log("close", logs.DebugLevel, "bridge %d closing", b.id)
	b.agent.releaseBridgeRoute(b.id)
	if b.stream != nil {
		b.stream.Close()
	}
	if b.remote != nil {
		b.remote.Close()
	}
	// Send BridgeClose on stream 0
	b.agent.Send(&message.BridgeClose{ID: b.id})
	b.cancel()
	return nil
}

func (b *Bridge) Log(part string, level logs.Level, msg string, s ...interface{}) {
	utils.Log.FLogf(b.agent.log, level, "[bridge.%d.%s] %s", b.id, part, fmt.Sprintf(msg, s...))
}
