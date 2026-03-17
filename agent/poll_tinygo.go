//go:build tinygo

package agent

import (
	"encoding/hex"
	"fmt"
	"net"
	"sync"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
)

type pollHooks struct {
	bridgeClose func(uint64) bool
}

var pollHooksMap sync.Map // map[*Agent]*pollHooks

func (agent *Agent) SetPollHooks(bridgeClose func(uint64) bool) {
	pollHooksMap.Store(agent, &pollHooks{bridgeClose: bridgeClose})
}

func (agent *Agent) ClearPollHooks() {
	pollHooksMap.Delete(agent)
}

func (agent *Agent) runBridgeCloseHook(id uint64) bool {
	v, ok := pollHooksMap.Load(agent)
	if !ok {
		return false
	}
	h := v.(*pollHooks)
	if h.bridgeClose == nil {
		return false
	}
	return h.bridgeClose(id)
}

// PollableConn is implemented by connections that support non-blocking read checks.
type PollableConn interface {
	PollRead(timeoutMs int) bool
}

// NonBlockingAccepter is implemented by listeners that support non-blocking accept.
type NonBlockingAccepter interface {
	TryAccept() (net.Conn, error, bool)
}

// PollOnce performs one iteration of the agent event loop without goroutines.
// With yamux, we poll stream0 for control messages and accept new data streams.
func (agent *Agent) PollOnce() {
	if agent.Closed || agent.stream0 == nil {
		return
	}

	const maxPerTick = 32

	// 1. Read control messages from stream0 (non-blocking)
	if pc, ok := agent.stream0.(PollableConn); ok {
		for i := 0; i < maxPerTick; i++ {
			if !pc.PollRead(0) {
				break
			}
			msg, err := cio.ReadMsg(agent.stream0)
			if err != nil {
				agent.Log("poll.recv", logs.DebugLevel, "stream0 read error: %v", err)
				agent.Close(fmt.Errorf("poll recv error: %w", err))
				return
			}
			agent.processOneMessage(msg)
		}
	}

	// 2. Accept new connections on memory listener (non-blocking)
	if agent.listener != nil {
		if nba, ok := agent.listener.(NonBlockingAccepter); ok {
			if conn, err, accepted := nba.TryAccept(); accepted && err == nil {
				bridge, err := NewBridgeWithConn(agent, conn, agent.Controller)
				if err != nil {
					agent.Log("poll.accept", logs.ErrorLevel, "bridge create failed: %v", err)
				} else {
					agent.Log("poll.accept", logs.DebugLevel, "bridge %d created", bridge.id)
					agent.saveBridge(bridge)
				}
			}
		}
	}
}

// processOneMessage dispatches a single control message from stream0.
func (agent *Agent) processOneMessage(msg message.Message) {
	switch m := msg.(type) {
	case *message.Pong:
		agent.Log("pong", logs.DebugLevel, "pong %s", m.Pong)
	case *message.Ping:
		agent.Log("ping", logs.DebugLevel, "ping %s", m.Ping)
		agent.Send(&message.Pong{Pong: "pong"})
	case *message.BridgeOpen:
		agent.handleBridgeOpen(m)
	case *message.BridgeClose:
		if agent.runBridgeCloseHook(m.ID) {
			return
		}
		if bridge, err := agent.getBridge(m.ID); err == nil {
			if !bridge.closed.Load() {
				bridge.Log("end", logs.DebugLevel, "bridge %d closed by remote", m.ID)
				bridge.Close()
			}
			agent.bridgeMap.Delete(bridge.id)
		}
	case *message.Control:
		if agent.Type != core.CLIENT && agent.routeControl(m) {
			return
		}
		if m.Fork {
			a, err := agent.Fork(m)
			if err != nil {
				agent.Log("failed", logs.ErrorLevel, "%s", err.Error())
			} else {
				Agents.Add(a)
			}
		} else {
			if err := agent.handlerControl(m); err != nil {
				agent.Log("failed", logs.ErrorLevel, "%s", err.Error())
			}
		}
	default:
		agent.Log("message", logs.DebugLevel, "unknown msg type in poll")
	}
}

func (agent *Agent) initProxyClient() error {
	if len(agent.Proxies) > 0 {
		return fmt.Errorf("tinygo: proxy chain is not supported")
	}
	agent.client = &core.NetDialer{}
	return nil
}

func buildAuthToken(key []byte) (string, error) {
	return hex.EncodeToString(key), nil
}
