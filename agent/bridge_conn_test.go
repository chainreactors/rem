//go:build !tinygo

package agent

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/rem/internal/netconntest"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
	"github.com/chainreactors/rem/x/yamux"
)

type bridgeTestOutbound struct {
	conn  net.Conn
	ready chan struct{}
	once  sync.Once
}

func (o *bridgeTestOutbound) Name() string {
	return "bridge-test"
}

func (o *bridgeTestOutbound) Handle(io.ReadWriteCloser, net.Conn) (net.Conn, error) {
	o.once.Do(func() {
		close(o.ready)
	})
	return o.conn, nil
}

func (o *bridgeTestOutbound) ToClash() *utils.Proxies {
	return nil
}

func newBridgeTestAgentBase(t *testing.T, id, typ string, conn net.Conn) *Agent {
	t.Helper()

	consoleURL, err := core.NewConsoleURL("tcp://127.0.0.1:0")
	if err != nil {
		t.Fatalf("new console URL: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Agent{
		Config: &Config{
			Alias: id,
			Type:  typ,
			URLs: &core.URLs{
				ConsoleURL: consoleURL,
			},
		},
		ID:       id,
		Conn:     conn,
		ctx:      ctx,
		canceler: cancel,
		log:      utils.NewRingLogWriter(32),
		connHub:  NewConnHub(""),
	}
}

func newBridgeTestPair(t *testing.T) (*Agent, *Agent, func()) {
	t.Helper()

	clientRaw, serverRaw := net.Pipe()
	client := newBridgeTestAgentBase(t, "bridge-client", core.CLIENT, clientRaw)
	server := newBridgeTestAgentBase(t, "bridge-server", core.SERVER, serverRaw)

	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = false
	cfg.ConnectionWriteTimeout = 5 * time.Second
	cfg.StreamOpenTimeout = 5 * time.Second

	var err error
	client.session, err = yamux.Client(clientRaw, cfg)
	if err != nil {
		t.Fatalf("yamux client: %v", err)
	}
	server.session, err = yamux.Server(serverRaw, cfg)
	if err != nil {
		t.Fatalf("yamux server: %v", err)
	}

	client.sessionConnID = "client-0"
	server.sessionConnID = "server-0"

	if _, err = client.connHub.AddClientConn(client.sessionConnID, "test-client", clientRaw, client.session); err != nil {
		t.Fatalf("add client conn: %v", err)
	}
	if _, err = server.connHub.AddServerConn(server.sessionConnID, "test-server", serverRaw, server.session); err != nil {
		t.Fatalf("add server conn: %v", err)
	}

	clientCtrlDone := startBridgeControlLoop(client)
	serverCtrlDone := startBridgeControlLoop(server)
	clientAcceptDone := startBridgeAcceptLoop(client)
	serverAcceptDone := startBridgeAcceptLoop(server)

	stop := func() {
		client.Close(nil)
		server.Close(nil)
		<-clientCtrlDone
		<-serverCtrlDone
		<-clientAcceptDone
		<-serverAcceptDone
	}

	return client, server, stop
}

func startBridgeAcceptLoop(agent *Agent) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.acceptStreamsForSession(agent.sessionConnID, agent.session)
	}()
	return done
}

func startBridgeControlLoop(agent *Agent) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-agent.ctx.Done():
				return
			case <-agent.connHub.ControlErrors():
				return
			case msg := <-agent.connHub.ControlInbox():
				if msg == nil {
					continue
				}
				switch m := msg.(type) {
				case *message.BridgeOpen:
					agent.handleBridgeOpen(m)
				case *message.BridgeClose:
					if bridge, err := agent.getBridge(m.ID); err == nil {
						if !bridge.closed.Load() {
							bridge.Close()
						}
						agent.bridgeMap.Delete(bridge.id)
					} else {
						agent.releaseBridgeRoute(m.ID)
					}
				}
			}
		}
	}()
	return done
}

func TestBridge_NetConn(t *testing.T) {
	netconntest.TestConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		client, server, stopAgents := newBridgeTestPair(t)

		rightApp, rightBridge := net.Pipe()
		outbound := &bridgeTestOutbound{
			conn:  rightBridge,
			ready: make(chan struct{}),
		}
		server.Outbound = outbound

		leftApp, leftBridge := net.Pipe()
		bridge, err := NewBridgeWithConn(client, leftBridge, &message.Control{
			Source:      client.ID,
			Destination: server.ID,
		})
		if err != nil {
			_ = leftApp.Close()
			_ = rightApp.Close()
			stopAgents()
			return nil, nil, nil, err
		}
		client.saveBridge(bridge)

		select {
		case <-outbound.ready:
		case <-time.After(2 * time.Second):
			_ = leftApp.Close()
			_ = rightApp.Close()
			stopAgents()
			return nil, nil, nil, context.DeadlineExceeded
		}

		stop = func() {
			_ = leftApp.Close()
			_ = rightApp.Close()
			stopAgents()
		}
		return leftApp, rightApp, stop, nil
	})
}
