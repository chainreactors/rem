//go:build !tinygo

package agent

import (
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/core"
)

// TestWaitInitReturnsTrueAfterInit covers the success path: the handler
// finishes synchronous init and waiting callers unblock.
func TestWaitInitReturnsTrueAfterInit(t *testing.T) {
	a, err := NewAgent(&Config{Alias: "waitinit-success", Type: core.CLIENT})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(nil) })

	go func() {
		time.Sleep(100 * time.Millisecond)
		a.Init = true
	}()
	if !a.WaitInit(5 * time.Second) {
		t.Fatal("WaitInit should report true once Init is set")
	}
}

// TestWaitInitReturnsFalseWhenAgentClosesBeforeInit is the regression for
// the wedged RemDial wait: a HandlerInit failure closes the agent without
// ever setting Init, and callers waiting on Init alone would block forever.
func TestWaitInitReturnsFalseWhenAgentClosesBeforeInit(t *testing.T) {
	a, err := NewAgent(&Config{Alias: "waitinit-closed", Type: core.CLIENT})
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		a.Close(nil)
	}()
	start := time.Now()
	if a.WaitInit(5 * time.Second) {
		t.Fatal("WaitInit should report false for an agent closed before Init")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("WaitInit blocked %v after the agent closed", elapsed)
	}
}

// TestWaitInitTimeout is the backstop: an agent that neither initializes
// nor closes fails the wait after the timeout instead of blocking forever.
func TestWaitInitTimeout(t *testing.T) {
	a, err := NewAgent(&Config{Alias: "waitinit-timeout", Type: core.CLIENT})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(nil) })

	start := time.Now()
	if a.WaitInit(300 * time.Millisecond) {
		t.Fatal("WaitInit should report false on timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("WaitInit ignored the timeout, blocked %v", elapsed)
	}
}
