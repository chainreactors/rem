//go:build !tinygo

package agent

import (
	"math/rand"
	"testing"
)

func newTestManagedConn(id string) *managedConn {
	m := &managedConn{id: id, label: id}
	m.healthy.Store(true)
	return m
}

func newTestConnHub(algo string, ids ...string) *ConnHub {
	h := NewConnHub(algo)
	h.conns = map[string]*managedConn{}
	for _, id := range ids {
		h.conns[id] = newTestManagedConn(id)
	}
	if len(ids) > 0 {
		h.preferredID = ids[0]
	}
	return h
}

func TestConnHubPickRoundRobin(t *testing.T) {
	h := newTestConnHub(ConnBalanceRoundRobin, "a", "b", "c")
	got := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		c := h.pick(nil)
		if c == nil {
			t.Fatalf("pick returned nil")
		}
		got = append(got, c.id)
	}
	want := []string{"a", "b", "c", "a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round-robin pick mismatch at %d: got=%v want=%v", i, got, want)
		}
	}
}

func TestConnHubPickFallback(t *testing.T) {
	h := newTestConnHub(ConnBalanceFallback, "a", "b", "c")
	for i := 0; i < 5; i++ {
		c := h.pick(nil)
		if c == nil || c.id != "a" {
			t.Fatalf("fallback should choose preferred a, got=%v", c)
		}
	}

	h.conns["a"].healthy.Store(false)
	c := h.pick(nil)
	if c == nil {
		t.Fatalf("fallback after preferred unhealthy should still pick conn")
	}
	if c.id == "a" {
		t.Fatalf("fallback should degrade away from unhealthy preferred conn")
	}
}

func TestConnHubPickRandom(t *testing.T) {
	h := newTestConnHub(ConnBalanceRandom, "a", "b", "c")
	h.rng = rand.New(rand.NewSource(7))

	seen := map[string]int{}
	for i := 0; i < 128; i++ {
		c := h.pick(nil)
		if c == nil {
			t.Fatalf("random pick returned nil")
		}
		seen[c.id]++
	}
	for _, id := range []string{"a", "b", "c"} {
		if seen[id] == 0 {
			t.Fatalf("random pick never selected %s: %+v", id, seen)
		}
	}
}

func TestConnHubReleaseBridge(t *testing.T) {
	h := newTestConnHub(ConnBalanceFallback, "a")
	h.bridgeRoute.Store(uint64(10), "a")
	h.conns["a"].active.Store(3)

	h.ReleaseBridge(10)
	if h.conns["a"].active.Load() != 2 {
		t.Fatalf("ReleaseBridge should decrement active, got=%d", h.conns["a"].active.Load())
	}
}
