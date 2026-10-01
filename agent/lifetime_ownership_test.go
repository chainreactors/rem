package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	_ "github.com/chainreactors/rem/protocol/serve/raw"
)

func TestClosingForkPreservesParentTransport(t *testing.T) {
	parent, err := NewAgent(&Config{Alias: "fork-lifetime-parent", Type: core.CLIENT})
	if err != nil {
		t.Fatal(err)
	}
	child, err := NewAgent(&Config{Alias: "fork-lifetime-child", Type: core.CLIENT})
	if err != nil {
		t.Fatal(err)
	}
	left, peer := net.Pipe()
	parent.Conn = left
	child.Conn = left
	child.parent = parent
	parent.children.Store(child.ID, child)
	if err := Agents.Add(child); err != nil {
		t.Fatal(err)
	}
	defer parent.Close(nil)
	defer peer.Close()
	defer Agents.Delete(child.ID)
	child.Close(nil)
	if parent.IsClosed() {
		t.Fatal("closing a fork closed its parent")
	}
	_ = peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	_, readErr := peer.Read(make([]byte, 1))
	if timeout, ok := readErr.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("parent transport no longer live: %v", readErr)
	}
	parent.Close(nil)
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("parent transport remained open after parent close: %v", err)
	}
	if Agents.Exist(child.ID) {
		t.Fatal("parent close left the child registered")
	}
}

func lifetimeParent(t *testing.T, name string) *Agent {
	t.Helper()
	consoleURL, err := core.NewConsoleURL("tcp://127.0.0.1:34996")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := NewAgent(&Config{
		Alias: name,
		Type:  core.CLIENT,
		URLs:  &core.URLs{ConsoleURL: consoleURL},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Close(nil) })
	return parent
}

func lifetimeControl(name string) *message.Control {
	return &message.Control{
		Source: name,
		Local:  "raw://127.0.0.1:0",
		Remote: "raw://127.0.0.1:0",
	}
}

type blockingLifetimeListener struct {
	net.Listener
	entered chan<- string
	release <-chan struct{}
}

func (l *blockingLifetimeListener) Listen(string) (net.Listener, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l.Listener = listener
	l.entered <- listener.Addr().String()
	<-l.release
	return listener, nil
}

func TestForkAndParentCloseReclaimOpenedListener(t *testing.T) {
	parent := lifetimeParent(t, "fork-close-listener-parent")
	entered := make(chan string, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	core.ListenerRegister("lifetime-blocking", func(context.Context) (core.TunnelListener, error) {
		return &blockingLifetimeListener{entered: entered, release: release}, nil
	})
	control := lifetimeControl("fork-close-listener-child")
	control.InboundSide = core.SideLocal
	control.Local = "lifetime-blocking+raw://127.0.0.1:0"
	type forkResult struct {
		child *Agent
		err   error
	}
	forkDone := make(chan forkResult, 1)
	go func() {
		child, err := parent.Fork(control)
		forkDone <- forkResult{child, err}
	}()
	var address string
	select {
	case address = <-entered:
	case <-time.After(time.Second):
		t.Fatal("fork did not open its loopback listener")
	}
	closeStarted := make(chan struct{})
	closeDone := make(chan struct{})
	go func() {
		close(closeStarted)
		parent.Close(nil)
		close(closeDone)
	}()
	<-closeStarted
	select {
	case <-closeDone:
		t.Error("parent close finished before the in-flight child was published")
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	var result forkResult
	select {
	case result = <-forkDone:
	case <-time.After(time.Second):
		t.Fatal("fork and parent close deadlocked")
	}
	if result.err != nil || result.child == nil {
		t.Fatalf("in-flight fork failed: %v", result.err)
	}
	defer result.child.Close(nil)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("parent close did not finish")
	}
	if !result.child.IsClosed() {
		t.Error("parent close left its in-flight child live")
	}
	// The native message handler registers a fork after Fork returns. A close
	// in that gap must prevent publishing the retired child again.
	if err := Agents.Add(result.child); err == nil {
		t.Error("closed child was added after parent cleanup")
	}
	if Agents.Exist(result.child.ID) {
		t.Error("parent close left the child in the registry")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("parent close leaked the child's listener at %s: %v", address, err)
	}
	_ = listener.Close()
}

func TestAgentRegistryRejectsClosedConnection(t *testing.T) {
	a := lifetimeParent(t, "registry-add-after-close")
	a.Close(nil)
	if err := Agents.Add(a); err == nil {
		t.Fatal("registry admitted a closed connection")
	}
	if Agents.Exist(a.ID) {
		t.Fatal("closed connection appeared in the registry")
	}
}

func TestAgentRegistryAddRacesClose(t *testing.T) {
	for index := 0; index < 100; index++ {
		a := lifetimeParent(t, fmt.Sprintf("registry-add-close-race-%d", index))
		start := make(chan struct{})
		var done sync.WaitGroup
		done.Add(2)
		go func() {
			defer done.Done()
			<-start
			_ = Agents.Add(a)
		}()
		go func() {
			defer done.Done()
			<-start
			a.Close(nil)
		}()
		close(start)
		done.Wait()
		if !a.IsClosed() || Agents.Exist(a.ID) {
			t.Fatalf("concurrent add and close left connection %d registered", index)
		}
	}
}

func TestAgentRegistryDuplicatePreservesExistingConnection(t *testing.T) {
	first := lifetimeParent(t, "registry-duplicate-connection")
	duplicate := lifetimeParent(t, first.ID)
	if err := Agents.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := Agents.Add(duplicate); err == nil {
		t.Fatal("registry replaced a connection with another object of the same ID")
	}
	duplicate.Close(nil)
	if current, ok := Agents.Get(first.ID); !ok || current != first || first.IsClosed() {
		t.Fatal("rejected duplicate cleanup removed or closed the original connection")
	}
}

func TestForkRejectsForeignConnectionIdentity(t *testing.T) {
	parent := lifetimeParent(t, "fork-foreign-identity-parent")
	foreign := lifetimeParent(t, "fork-foreign-identity-owner")
	if err := Agents.Add(foreign); err != nil {
		t.Fatal(err)
	}
	if child, err := parent.Fork(lifetimeControl(foreign.ID)); err == nil || child != nil {
		if child != nil {
			child.Close(nil)
		}
		t.Fatal("fork reused another connection's identity")
	}
	if current, ok := Agents.Get(foreign.ID); !ok || current != foreign || foreign.IsClosed() {
		t.Fatal("rejected fork changed or closed the foreign connection")
	}
}

func TestStoppedForkIdentityCanBeReused(t *testing.T) {
	parent := lifetimeParent(t, "fork-reuse-identity-parent")
	control := lifetimeControl("fork-reuse-identity-child")
	old, err := parent.Fork(control)
	if err != nil {
		t.Fatal(err)
	}
	if err := Agents.Add(old); err != nil {
		t.Fatal(err)
	}
	old.Close(nil)
	replacement, err := parent.Fork(control)
	if err != nil {
		t.Fatalf("stopped child's identity could not be reused: %v", err)
	}
	if err := Agents.Add(replacement); err != nil {
		t.Fatal(err)
	}
	old.Close(nil)
	if current, ok := parent.children.Load(old.ID); !ok || current != replacement {
		t.Fatal("retired child cleanup removed its live replacement from the parent")
	}
	if current, ok := Agents.Get(old.ID); !ok || current != replacement || replacement.IsClosed() {
		t.Fatal("retired child cleanup removed or closed its live replacement")
	}
	parent.Close(nil)
	if !replacement.IsClosed() || Agents.Exist(replacement.ID) {
		t.Fatal("parent close leaked the replacement child")
	}
}
