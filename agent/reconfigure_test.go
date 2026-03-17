//go:build !tinygo

package agent

import (
	"net"
	"sync"
	"testing"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
)

// mockConfigurableAddr is a test double that implements net.Addr and the
// configurable interface (SetOption), simulating a SimplexAddr.
type mockConfigurableAddr struct {
	mu      sync.Mutex
	options map[string]string
}

func (a *mockConfigurableAddr) Network() string { return "mock" }
func (a *mockConfigurableAddr) String() string  { return "mock://test" }

func (a *mockConfigurableAddr) SetOption(key, value string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.options == nil {
		a.options = make(map[string]string)
	}
	a.options[key] = value
}

func (a *mockConfigurableAddr) getOption(key string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.options[key]
}

// plainAddr implements net.Addr but NOT configurable.
type plainAddr struct{}

func (a *plainAddr) Network() string { return "plain" }
func (a *plainAddr) String() string  { return "plain://test" }

func newReconfigTestAgent(t *testing.T) *Agent {
	t.Helper()
	a, err := NewAgent(&Config{
		Alias: "test-" + utils.RandomString(4),
		Type:  core.CLIENT,
		URLs:  &core.URLs{},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	t.Cleanup(func() { Agents.Delete(a.ID) })
	return a
}

// TestHandleReconfigure_UpdatesTransportAddr verifies that handleReconfigure
// calls SetOption on the TransportAddr when it implements configurable.
func TestHandleReconfigure_UpdatesTransportAddr(t *testing.T) {
	a := newReconfigTestAgent(t)
	addr := &mockConfigurableAddr{}
	a.TransportAddr = addr

	a.handleReconfigure(&message.Reconfigure{
		Options: map[string]string{
			"interval":    "5000",
			"maxBodySize": "200000",
		},
	})

	if addr.getOption("interval") != "5000" {
		t.Fatalf("interval = %q, want '5000'", addr.getOption("interval"))
	}
	if addr.getOption("maxBodySize") != "200000" {
		t.Fatalf("maxBodySize = %q, want '200000'", addr.getOption("maxBodySize"))
	}
}

// TestHandleReconfigure_NilTransportAddr verifies no panic when TransportAddr is nil.
func TestHandleReconfigure_NilTransportAddr(t *testing.T) {
	a := newReconfigTestAgent(t)
	// TransportAddr is nil — should log error but not panic
	a.handleReconfigure(&message.Reconfigure{
		Options: map[string]string{"interval": "1000"},
	})
}

// TestHandleReconfigure_NonConfigurableAddr verifies no panic when TransportAddr
// does not implement configurable.
func TestHandleReconfigure_NonConfigurableAddr(t *testing.T) {
	a := newReconfigTestAgent(t)
	a.TransportAddr = &plainAddr{}
	// Should log error but not panic
	a.handleReconfigure(&message.Reconfigure{
		Options: map[string]string{"interval": "1000"},
	})
}

// TestHandleReconfigure_EmptyOptions verifies no-op when Options is empty.
func TestHandleReconfigure_EmptyOptions(t *testing.T) {
	a := newReconfigTestAgent(t)
	addr := &mockConfigurableAddr{}
	a.TransportAddr = addr
	a.handleReconfigure(&message.Reconfigure{})
	if len(addr.options) != 0 {
		t.Fatalf("expected no options set, got %v", addr.options)
	}
}

// Verify that configurable interface is satisfied by any type with SetOption.
var _ configurable = (*mockConfigurableAddr)(nil)
var _ net.Addr = (*mockConfigurableAddr)(nil)
