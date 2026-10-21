//go:build !tinygo

package runner

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/core"
	_ "github.com/chainreactors/rem/protocol/serve/socks"
	_ "github.com/chainreactors/rem/protocol/tunnel/tcp"
)

// dialWithAlias runs the client side of the FFI RemDial flow: build a fresh
// console from the command line and dial the server console with a fixed
// agent alias.
func dialWithAlias(t *testing.T, link, alias string) (*Console, *agent.Agent, error) {
	t.Helper()
	client, err := NewConsoleWithCMD(fmt.Sprintf("-c %s -l socks5://127.0.0.1:0 -a %s --no-sub", link, alias))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	a, err := client.Dial(client.ConsoleURL)
	return client, a, err
}

func runHandler(t *testing.T, a *agent.Agent) {
	t.Helper()
	go func() { _ = a.Handler() }()
	if !a.WaitInit(10 * time.Second) {
		t.Fatalf("agent %s failed to initialize", a.ID)
	}
	t.Cleanup(func() { a.Close(nil) })
}

// TestSecondDialWithLiveAliasIsRefusedWithoutPanic reproduces the production
// second-rem_dial scenario: a second dial in the same process while the
// first agent is still registered under the same alias. Before the console
// lifecycle fixes, the ignored agent.NewAgent error panicked with a nil
// pointer dereference in newAgent and killed the whole process. The second
// dial must instead fail with a controlled error and leave the first
// channel untouched.
func TestSecondDialWithLiveAliasIsRefusedWithoutPanic(t *testing.T) {
	// The first rem_dial's channel: an incumbent agent registered under the
	// alias and still alive. The refusal happens in newAgent before any
	// network I/O, so no server is needed.
	incumbent, err := agent.NewAgent(&agent.Config{Alias: "redial-live-alias", Type: core.CLIENT})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Agents.Add(incumbent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { incumbent.Close(nil); agent.Agents.CompareAndDelete(incumbent.ID, incumbent) })

	client, err := NewConsoleWithCMD("-c tcp://nonenonenonenone@127.0.0.1:1/?wrapper=raw -l socks5://127.0.0.1:0 -a redial-live-alias --no-sub")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	a, err := client.Dial(client.ConsoleURL)
	if err == nil {
		if a != nil {
			a.Close(nil)
		}
		t.Fatal("second dial with a live alias should have been refused")
	}
	if a != nil {
		t.Fatal("refused dial returned a non-nil agent")
	}
	if incumbent.IsClosed() {
		t.Fatal("refused second dial killed the incumbent agent")
	}
	if current, ok := agent.Agents.Get(incumbent.ID); !ok || current != incumbent {
		t.Fatal("incumbent agent lost its registry entry after refused dial")
	}
}

// TestDialWithAliasReestablishesAfterClose covers the other half of the
// second-dial contract: once the first agent is closed (handler exit or
// explicit cleanup), a re-dial with the same alias must succeed and fully
// re-initialize.
//
// The server console runs in a subprocess because the agent registry is
// process-global: an in-process server would register its server-side mirror
// agent under the same alias as the client agent and the two would collide.
func TestDialWithAliasReestablishesAfterClose(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	startHelper(t, fmt.Sprintf("-s tcp://redialkey@127.0.0.1:%d/?wrapper=raw --no-sub", port))
	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", port), 10*time.Second)
	link := fmt.Sprintf("tcp://redialkey@127.0.0.1:%d/?wrapper=raw", port)

	_, first, err := dialWithAlias(t, link, "redial-reestablish-alias")
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	runHandler(t, first)
	first.Close(nil)

	_, second, err := dialWithAlias(t, link, "redial-reestablish-alias")
	if err != nil {
		t.Fatalf("re-dial after close: %v", err)
	}
	runHandler(t, second)
	if second.ID != first.ID {
		t.Fatalf("re-dialed agent ID = %q, want the alias %q", second.ID, first.ID)
	}
	if current, ok := agent.Agents.Get(second.ID); !ok || current != second {
		t.Fatal("re-dialed agent is not the registered one")
	}
}
