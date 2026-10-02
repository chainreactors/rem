package runner

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
)

func ownershipConsole(t *testing.T) *Console {
	t.Helper()
	c, err := NewConsoleWithCMD("-s tcp://127.0.0.1:0/?wrapper=raw --no-sub")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func ownershipAgent(t *testing.T, name string) (*agent.Agent, net.Conn) {
	t.Helper()
	a, err := agent.NewAgent(&agent.Config{Alias: name, Type: core.CLIENT})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	right, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	left, err := listener.Accept()
	if err != nil {
		_ = right.Close()
		t.Fatal(err)
	}
	a.Conn = left
	if err := agent.Agents.Add(a); err != nil {
		_ = left.Close()
		_ = right.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(nil); _ = right.Close(); agent.Agents.CompareAndDelete(a.ID, a) })
	return a, right
}

func TestConsoleCloseDoesNotCloseAnotherConsoleAgent(t *testing.T) {
	first := ownershipConsole(t)
	second := ownershipConsole(t)
	foreign, peer := ownershipAgent(t, "console-close-other-owner")
	if err := second.registerAgent(foreign); err != nil {
		t.Fatal(err)
	}
	owned, ownedPeer := ownershipAgent(t, "console-close-own-owner")
	if err := first.registerAgent(owned); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	_, err := peer.Read(make([]byte, 1))
	if err == io.EOF {
		t.Fatal("closing one console closed another console's transport")
	}
	if err, ok := err.(net.Error); !ok || !err.Timeout() {
		t.Fatalf("foreign transport is not live: %v", err)
	}
	_ = ownedPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := ownedPeer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("owned transport was not closed: %v", err)
	}
}

func TestConsoleRejectsForeignAgentForkAndAttach(t *testing.T) {
	first := ownershipConsole(t)
	second := ownershipConsole(t)
	foreign, _ := ownershipAgent(t, "console-control-other-owner")
	if err := second.registerAgent(foreign); err != nil {
		t.Fatal(err)
	}
	if _, ok := first.Agent(foreign.ID); ok {
		t.Fatal("foreign agent is exposed by another console")
	}
	if _, err := first.Fork(foreign.ID, []string{"-l", "socks5://127.0.0.1:0"}); err == nil {
		t.Fatal("foreign fork was admitted")
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if err := first.attachConnToAgent(foreign.ID, "foreign-channel", left); err == nil {
		t.Fatal("foreign channel attach was admitted")
	}
}

func TestConsoleOwnerIdentityPreservesClosedGeneration(t *testing.T) {
	c := ownershipConsole(t)
	a, _ := ownershipAgent(t, "console-owner-closed-generation")
	if err := c.registerAgent(a); err != nil {
		t.Fatal(err)
	}
	a.Close(nil)
	if !c.owns(a) {
		t.Fatal("closed connection lost its original console owner before replacement")
	}
	if _, ok := c.Agent(a.ID); ok {
		t.Fatal("closed agent remained available for a new control operation")
	}
}

func TestClosedConsoleRejectsLateAgentRegistration(t *testing.T) {
	c := ownershipConsole(t)
	a, _ := ownershipAgent(t, "console-owner-late-registration")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.registerAgent(a); err == nil {
		t.Fatal("closed console admitted a late connection")
	}
}

func ownershipFork(t *testing.T, c *Console, root *agent.Agent, name string) *agent.Agent {
	t.Helper()
	root.URLs = &core.URLs{ConsoleURL: c.ConsoleURL.Copy()}
	child, err := root.Fork(&message.Control{
		Source: name,
		Local:  "tcp://127.0.0.1:0",
		Remote: "tcp://127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Close(nil); agent.Agents.CompareAndDelete(child.ID, child) })
	return child
}

func TestConsoleLateChildRegistrationCannotReplaceCurrentRoot(t *testing.T) {
	c := ownershipConsole(t)
	old, _ := ownershipAgent(t, "console-replaced-generation")
	if err := c.registerAgent(old); err != nil {
		t.Fatal(err)
	}
	child := ownershipFork(t, c, old, "console-old-generation-child")
	old.Close(nil)
	agent.Agents.CompareAndDelete(old.ID, old)
	replacement, peer := ownershipAgent(t, old.ID)
	if err := c.registerAgent(replacement); err != nil {
		t.Fatal(err)
	}
	if err := c.registerAgent(child); err == nil {
		t.Error("closed old-generation child was accepted after root replacement")
	}
	if _, ok := agent.Agents.Get(child.ID); ok {
		t.Error("rejected child was added to the global agent registry")
	}
	// Console.Fork closes a rejected child. Its shared transport belongs to the
	// retired root; cleanup must leave the replacement generation untouched.
	child.Close(nil)
	if current, ok := c.Agent(replacement.ID); !ok || current != replacement {
		t.Error("late child overwrote ownership of the live replacement")
	}
	_ = peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := peer.Read(make([]byte, 1)); err == io.EOF {
		t.Error("rejected child cleanup closed the replacement transport")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Errorf("replacement transport is not live: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !replacement.IsClosed() {
		t.Error("Console.Close leaked the live replacement transport")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("Console.Close did not close the replacement transport: %v", err)
	}
}

func TestConsoleRejectsForeignRootChildBeforeGlobalRegistration(t *testing.T) {
	c := ownershipConsole(t)
	other := ownershipConsole(t)
	root, _ := ownershipAgent(t, "console-child-foreign-root")
	if err := other.registerAgent(root); err != nil {
		t.Fatal(err)
	}
	child := ownershipFork(t, other, root, "console-child-foreign-root-child")
	if err := c.registerAgent(child); err == nil {
		t.Fatal("console admitted a child of another console's root")
	}
	if _, ok := agent.Agents.Get(child.ID); ok {
		t.Fatal("rejected foreign child was added to the global registry")
	}
	if err := other.registerAgent(child); err != nil {
		t.Fatalf("current owner could not register its live child: %v", err)
	}
	if current, ok := other.Agent(root.ID); !ok || current != root {
		t.Fatal("child registration changed root ownership")
	}
}

func TestConsoleRejectsClosedRootBeforeGlobalRegistration(t *testing.T) {
	c := ownershipConsole(t)
	root, _ := ownershipAgent(t, "console-closed-root-registration")
	root.Close(nil)
	agent.Agents.CompareAndDelete(root.ID, root)
	if err := c.registerAgent(root); err == nil {
		t.Fatal("closed root was registered")
	}
	if _, ok := agent.Agents.Get(root.ID); ok {
		t.Fatal("closed root was added to the global registry")
	}
}

func TestConsoleRejectsLateForkOfClosedRoot(t *testing.T) {
	c := ownershipConsole(t)
	root, _ := ownershipAgent(t, "console-root-closed-before-fork")
	if err := c.registerAgent(root); err != nil {
		t.Fatal(err)
	}
	root.Close(nil)
	root.URLs = &core.URLs{ConsoleURL: c.ConsoleURL.Copy()}
	child, err := root.Fork(&message.Control{
		Source: "console-child-after-root-close",
		Local:  "tcp://127.0.0.1:0",
		Remote: "tcp://127.0.0.1:0",
	})
	if err == nil || child != nil {
		if child != nil {
			child.Close(nil)
		}
		t.Fatal("closed root created a new child")
	}
	if _, ok := agent.Agents.Get("console-child-after-root-close"); ok {
		t.Error("late child was added to the global registry")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleRejectsClosedChildUnderLiveRoot(t *testing.T) {
	c := ownershipConsole(t)
	root, _ := ownershipAgent(t, "console-live-root-closed-child")
	if err := c.registerAgent(root); err != nil {
		t.Fatal(err)
	}
	child := ownershipFork(t, c, root, "console-closed-child-under-live-root")
	child.Close(nil)
	if err := c.registerAgent(child); err == nil {
		t.Fatal("closed child was registered under its live root")
	}
	if agent.Agents.Exist(child.ID) {
		t.Fatal("closed child was added to the global registry")
	}
	if current, ok := c.Agent(root.ID); !ok || current != root {
		t.Fatal("rejecting the closed child changed its live root's ownership")
	}
}

func TestConsoleChildRegistrationRequiresCurrentRootPointer(t *testing.T) {
	c := ownershipConsole(t)
	old, _ := ownershipAgent(t, "console-stale-live-root")
	if err := c.registerAgent(old); err != nil {
		t.Fatal(err)
	}
	child := ownershipFork(t, c, old, "console-stale-live-root-child")
	agent.Agents.CompareAndDelete(old.ID, old)
	replacement, _ := ownershipAgent(t, old.ID)
	if err := c.registerAgent(child); err == nil {
		t.Fatal("child was registered while the registry pointed at another root")
	}
	if agent.Agents.Exist(child.ID) {
		t.Fatal("rejected child was added to the global registry")
	}
	if err := c.registerAgent(replacement); err != nil {
		t.Fatal(err)
	}
	if err := c.registerAgent(child); err == nil {
		t.Fatal("old live child was registered after its root was replaced")
	}
	if current, ok := c.Agent(replacement.ID); !ok || current != replacement {
		t.Fatal("old live child changed replacement ownership")
	}
}

func TestConsoleRejectedDuplicateForkPreservesOriginalChild(t *testing.T) {
	c := ownershipConsole(t)
	root, _ := ownershipAgent(t, "console-duplicate-fork-root")
	if err := c.registerAgent(root); err != nil {
		t.Fatal(err)
	}
	child := ownershipFork(t, c, root, "console-duplicate-fork-child")
	if err := c.registerAgent(child); err != nil {
		t.Fatal(err)
	}
	duplicate, err := root.Fork(&message.Control{
		Source: child.ID,
		Local:  "tcp://127.0.0.1:0",
		Remote: "tcp://127.0.0.1:0",
	})
	if err == nil || duplicate != nil {
		if duplicate != nil {
			duplicate.Close(nil)
		}
		t.Fatal("duplicate fork was admitted")
	}
	if current, ok := c.Agent(child.ID); !ok || current != child {
		t.Fatal("duplicate fork changed the original child")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !child.IsClosed() || agent.Agents.Exist(child.ID) {
		t.Fatal("duplicate fork displaced original ownership and leaked its resources")
	}
}
