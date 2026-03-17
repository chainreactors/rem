//go:build !tinygo

package agent

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
)

func resetAgentsState() {
	Agents.Map = &sync.Map{}
}

func newTestAgent(t *testing.T, id, typ, redirect string) *Agent {
	t.Helper()
	a, err := NewAgent(&Config{Alias: id, Type: typ, Redirect: redirect})
	if err != nil {
		t.Fatalf("NewAgent(%s): %v", id, err)
	}
	return a
}

func TestRouteControl_ForwardsByDestinationAlias(t *testing.T) {
	resetAgentsState()

	src := newTestAgent(t, "src", core.SERVER, "")

	dst := newTestAgent(t, "dst", core.SERVER, "")
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()
	dst.connHub = NewConnHub("")
	controlConn := &managedConn{
		id:      "redirect-ctrl",
		label:   "redirect-ctrl",
		control: writer,
	}
	controlConn.healthy.Store(true)
	dst.connHub.conns["redirect-ctrl"] = controlConn
	Agents.Add(dst)

	done := make(chan *message.Control, 1)
	go func() {
		msg, err := cio.ReadAndAssertMsg(reader, message.ControlMsg)
		if err != nil {
			done <- nil
			return
		}
		done <- msg.(*message.Control)
	}()

	ctrl := &message.Control{Source: "src", Destination: "dst"}
	if !src.routeControl(ctrl) {
		t.Fatal("expected routeControl to route")
	}
	if src.Type != core.Redirect {
		t.Fatalf("expected source type to switch to redirect, got %s", src.Type)
	}

	select {
	case got := <-done:
		if got == nil {
			t.Fatal("failed to read forwarded control")
		}
		if got.Source != "src" || got.Destination != "dst" {
			t.Fatalf("unexpected forwarded control: source=%q destination=%q", got.Source, got.Destination)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forwarded control")
	}
}

func TestRouteControl_DestinationAliasMatchesSelf(t *testing.T) {
	resetAgentsState()

	a := newTestAgent(t, "internal", core.SERVER, "")
	ctrl := &message.Control{Source: "client-b", Destination: "internal"}
	if a.routeControl(ctrl) {
		t.Fatal("expected routeControl not to route when destination matches alias")
	}
	if a.Type != core.SERVER {
		t.Fatalf("expected type to remain server, got %s", a.Type)
	}
}
