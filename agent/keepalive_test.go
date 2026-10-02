//go:build !tinygo

package agent

import (
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
	"github.com/chainreactors/rem/x/yamux"
)

// newKeepaliveTestPair creates a CLIENT + SERVER agent pair connected via
// net.Pipe + yamux, with control streams properly wired.
func newKeepaliveTestPair(t *testing.T) (client, server *Agent, cleanup func()) {
	t.Helper()

	c1, c2 := net.Pipe()

	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = false
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.StreamOpenTimeout = 10 * time.Second

	yClient, err := yamux.Client(c1, cfg)
	if err != nil {
		t.Fatalf("yamux client: %v", err)
	}
	yServer, err := yamux.Server(c2, cfg)
	if err != nil {
		t.Fatalf("yamux server: %v", err)
	}

	clientAgent, err := NewAgent(&Config{
		Alias: "ka-client-" + utils.RandomString(4),
		Type:  core.CLIENT,
		URLs:  &core.URLs{ConsoleURL: &core.URL{URL: &url.URL{Scheme: "tcp"}}},
	})
	if err != nil {
		t.Fatalf("NewAgent client: %v", err)
	}
	clientAgent.Conn = c1
	clientAgent.session = yClient
	clientAgent.connHub = NewConnHub("")

	serverAgent, err := NewAgent(&Config{
		Alias: "ka-server-" + utils.RandomString(4),
		Type:  core.SERVER,
		URLs:  &core.URLs{ConsoleURL: &core.URL{URL: &url.URL{Scheme: "tcp"}}},
	})
	if err != nil {
		t.Fatalf("NewAgent server: %v", err)
	}
	serverAgent.Conn = c2
	serverAgent.session = yServer
	serverAgent.connHub = NewConnHub("")

	// Open control streams via yamux — client opens, server accepts.
	// AddClientConn and AddServerConn handle this internally and also
	// start the background control-stream reader.
	_, err = clientAgent.connHub.AddClientConn("ctrl-0", "tcp", c1, yClient)
	if err != nil {
		t.Fatalf("AddClientConn: %v", err)
	}
	_, err = serverAgent.connHub.AddServerConn("ctrl-0", "tcp", c2, yServer)
	if err != nil {
		t.Fatalf("AddServerConn: %v", err)
	}

	cleanup = func() {
		clientAgent.Close(nil)
		serverAgent.Close(nil)
		Agents.Delete(clientAgent.ID)
		Agents.Delete(serverAgent.ID)
	}

	return clientAgent, serverAgent, cleanup
}

// TestKeepalive_NonceAcceptsCurrent verifies that a Pong matching the most
// recently sent nonce resets the missedPongs counter (no timeout).
func TestKeepalive_NonceAcceptsCurrent(t *testing.T) {
	oldInterval := KeepaliveInterval
	oldMax := KeepaliveMaxMissed
	KeepaliveInterval = 100 * time.Millisecond
	KeepaliveMaxMissed = 5
	defer func() {
		KeepaliveInterval = oldInterval
		KeepaliveMaxMissed = oldMax
	}()

	client, server, cleanup := newKeepaliveTestPair(t)
	defer cleanup()

	// Server side: read Ping and echo the nonce back as Pong
	go func() {
		inbox := server.connHub.ControlInbox()
		errs := server.connHub.ControlErrors()
		for {
			select {
			case <-server.ctx.Done():
				return
			case <-errs:
				return
			case msg := <-inbox:
				if ping, ok := msg.(*message.Ping); ok {
					_ = server.Send(&message.Pong{Pong: ping.Ping})
				}
			}
		}
	}()

	// Run client handleMessage — should NOT timeout since server echoes nonces
	errCh := make(chan error, 1)
	go func() { errCh <- client.handleMessage() }()

	// Let a few keepalive cycles pass (5 × 100ms = 500ms)
	time.Sleep(600 * time.Millisecond)

	// Kill the agent to exit handleMessage gracefully
	client.Close(nil)

	err := <-errCh
	if err != nil {
		t.Fatalf("handleMessage should not timeout with matching nonces, got: %v", err)
	}
}

// TestKeepalive_StalePongIgnored verifies that Pong messages with non-matching
// nonces are ignored and the agent eventually times out.
func TestKeepalive_StalePongIgnored(t *testing.T) {
	oldInterval := KeepaliveInterval
	oldMax := KeepaliveMaxMissed
	KeepaliveInterval = 50 * time.Millisecond
	KeepaliveMaxMissed = 3
	defer func() {
		KeepaliveInterval = oldInterval
		KeepaliveMaxMissed = oldMax
	}()

	client, server, cleanup := newKeepaliveTestPair(t)
	defer cleanup()

	// Server side: always respond with a FIXED string instead of echoing nonce.
	// This simulates the old Pong{Pong: "pong"} or cached stale Pongs.
	go func() {
		inbox := server.connHub.ControlInbox()
		errs := server.connHub.ControlErrors()
		for {
			select {
			case <-server.ctx.Done():
				return
			case <-errs:
				return
			case msg := <-inbox:
				if _, ok := msg.(*message.Ping); ok {
					_ = server.Send(&message.Pong{Pong: "stale-fixed-pong"})
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- client.handleMessage() }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected keepalive timeout error, got nil")
		}
		t.Logf("OK: stale pong correctly ignored, got: %v", err)
	case <-time.After(5 * time.Second):
		client.Close(nil)
		t.Fatal("timed out waiting for keepalive timeout")
	}
}

// TestKeepalive_TimeoutWithNoPong verifies that when no Pong is received at
// all, the agent times out after KeepaliveMaxMissed intervals.
func TestKeepalive_TimeoutWithNoPong(t *testing.T) {
	oldInterval := KeepaliveInterval
	oldMax := KeepaliveMaxMissed
	KeepaliveInterval = 50 * time.Millisecond
	KeepaliveMaxMissed = 3
	defer func() {
		KeepaliveInterval = oldInterval
		KeepaliveMaxMissed = oldMax
	}()

	client, _, cleanup := newKeepaliveTestPair(t)
	defer cleanup()

	// Don't run any server-side echo — no Pong will arrive
	errCh := make(chan error, 1)
	go func() { errCh <- client.handleMessage() }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected keepalive timeout error, got nil")
		}
		t.Logf("OK: no-pong timeout: %v", err)
	case <-time.After(5 * time.Second):
		client.Close(nil)
		t.Fatal("timed out waiting for keepalive timeout")
	}
}

// TestKeepalive_PreviousNonceRejected verifies that a Pong matching the
// previous nonce is rejected. Only the latest nonce is accepted, so stale
// pongs cached by high-latency transports cannot mask a dead peer.
func TestKeepalive_PreviousNonceRejected(t *testing.T) {
	oldInterval := KeepaliveInterval
	oldMax := KeepaliveMaxMissed
	KeepaliveInterval = 100 * time.Millisecond
	KeepaliveMaxMissed = 5
	defer func() {
		KeepaliveInterval = oldInterval
		KeepaliveMaxMissed = oldMax
	}()

	client, server, cleanup := newKeepaliveTestPair(t)
	defer cleanup()

	// Server side: delay the Pong by one keepalive interval.
	// Responds with the PREVIOUS ping's nonce, simulating one-cycle-late delivery.
	var mu sync.Mutex
	var prevPing string

	go func() {
		inbox := server.connHub.ControlInbox()
		errs := server.connHub.ControlErrors()
		for {
			select {
			case <-server.ctx.Done():
				return
			case <-errs:
				return
			case msg := <-inbox:
				if ping, ok := msg.(*message.Ping); ok {
					mu.Lock()
					if prevPing != "" {
						_ = server.Send(&message.Pong{Pong: prevPing})
					}
					prevPing = ping.Ping
					mu.Unlock()
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- client.handleMessage() }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected keepalive timeout (previous nonce should be rejected)")
		}
		t.Logf("OK: previous nonce rejected, got: %v", err)
	case <-time.After(5 * time.Second):
		client.Close(nil)
		t.Fatal("timed out waiting for keepalive timeout")
	}
}

// TestKeepalive_ServerTimesOutWithoutPong verifies that SERVER type agents
// also perform keepalive checking.
func TestKeepalive_ServerTimesOutWithoutPong(t *testing.T) {
	oldInterval := KeepaliveInterval
	oldMax := KeepaliveMaxMissed
	KeepaliveInterval = 50 * time.Millisecond
	KeepaliveMaxMissed = 2
	defer func() {
		KeepaliveInterval = oldInterval
		KeepaliveMaxMissed = oldMax
	}()

	_, server, cleanup := newKeepaliveTestPair(t)
	defer cleanup()

	errCh := make(chan error, 1)
	go func() { errCh <- server.handleMessage() }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected keepalive timeout error, got nil")
		}
		t.Logf("OK: server no-pong timeout: %v", err)
	case <-time.After(5 * time.Second):
		server.Close(nil)
		t.Fatal("timed out waiting for server keepalive timeout")
	}
}

// TestKeepalive_TwoIntervalOldNonceRejected verifies that a Pong echoing
// a nonce from 2+ intervals ago is rejected.
func TestKeepalive_TwoIntervalOldNonceRejected(t *testing.T) {
	oldInterval := KeepaliveInterval
	oldMax := KeepaliveMaxMissed
	KeepaliveInterval = 50 * time.Millisecond
	KeepaliveMaxMissed = 4
	defer func() {
		KeepaliveInterval = oldInterval
		KeepaliveMaxMissed = oldMax
	}()

	client, server, cleanup := newKeepaliveTestPair(t)
	defer cleanup()

	// Server side: delay the Pong by TWO intervals (respond with the nonce
	// from 2 pings ago). This should NOT be accepted.
	var mu sync.Mutex
	var nonces []string

	go func() {
		inbox := server.connHub.ControlInbox()
		errs := server.connHub.ControlErrors()
		for {
			select {
			case <-server.ctx.Done():
				return
			case <-errs:
				return
			case msg := <-inbox:
				if ping, ok := msg.(*message.Ping); ok {
					mu.Lock()
					nonces = append(nonces, ping.Ping)
					// Respond with the nonce from 2 pings ago
					if len(nonces) >= 3 {
						_ = server.Send(&message.Pong{Pong: nonces[len(nonces)-3]})
					}
					mu.Unlock()
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- client.handleMessage() }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected keepalive timeout (2-interval-old nonce should be rejected)")
		}
		t.Logf("OK: 2-interval-old nonce rejected, got: %v", err)
	case <-time.After(5 * time.Second):
		client.Close(nil)
		t.Fatal("timed out waiting for keepalive timeout")
	}
}
