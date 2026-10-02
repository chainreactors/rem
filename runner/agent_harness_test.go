//go:build !tinygo

package runner

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/agent"
	hagent "github.com/chainreactors/rem/harness/agent"
	_ "github.com/chainreactors/rem/protocol/tunnel/simplex"
)

type memoryBridgeClient struct {
	console   *Console
	agent     *agent.Agent
	memClient proxyclient.Dial
}

func (c *memoryBridgeClient) Close() error {
	if c == nil || c.console == nil {
		return nil
	}
	return c.console.Close()
}

type agentHarnessConfig struct {
	ServerURL  string
	ValidateFn func(resolvedURL string) error
	Alias      string
	SetupEnv   func(t *testing.T)
}

func startInProcessServer(t *testing.T, serverURL string, validateFn func(string) error) (resolvedURL string, closeFn func()) {
	t.Helper()

	console, err := NewConsoleWithCMD(fmt.Sprintf("--debug -s %s -i 127.0.0.1 --no-sub", serverURL))
	if err != nil {
		t.Fatalf("NewConsoleWithCMD(server): %v", err)
	}

	if err := console.Listen(console.ConsoleURL); err != nil {
		_ = console.Close()
		t.Fatalf("server listen: %v", err)
	}
	console.startPendingReaper()

	resolvedURL = console.Link()
	if resolvedURL == "" {
		_ = console.Close()
		t.Fatalf("server returned empty resolved URL")
	}
	if validateFn != nil {
		if err := validateFn(resolvedURL); err != nil {
			_ = console.Close()
			t.Fatalf("resolved URL validation: %v", err)
		}
	}

	runErr := make(chan error, 1)
	go func() {
		for !console.closed {
			age, err := console.Accept()
			if err != nil {
				if console.closed {
					runErr <- nil
					return
				}
				runErr <- err
				return
			}
			if age == nil {
				continue
			}
			go console.Handler(age)
		}
		runErr <- nil
	}()

	return resolvedURL, func() {
		_ = console.Close()
		select {
		case <-runErr:
		case <-time.After(5 * time.Second):
		}
	}
}

func startMemoryClient(t *testing.T, resolvedURL, alias string) (*memoryBridgeClient, error) {
	t.Helper()
	memoryPipe := fmt.Sprintf("agent-mem-%d", atomic.AddUint32(&testCounter, 1))
	cmd := fmt.Sprintf("-c %s -l memory+socks5://:@%s --debug", resolvedURL, memoryPipe)
	if alias != "" {
		cmd += fmt.Sprintf(" -a %s", alias)
	}

	console, err := NewConsoleWithCMD(cmd)
	if err != nil {
		return nil, fmt.Errorf("NewConsoleWithCMD: %w", err)
	}

	a, err := console.Dial(console.ConsoleURL)
	if err != nil {
		_ = console.Close()
		return nil, fmt.Errorf("Dial: %w", err)
	}

	go func() {
		err := a.Handler()
		a.Close(err)
		agent.Agents.Map.Delete(a.ID)
	}()

	deadline := time.Now().Add(180 * time.Second)
	for !a.Init && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if !a.Init {
		_ = console.Close()
		return nil, fmt.Errorf("agent did not initialize within timeout")
	}

	memURL := &url.URL{Scheme: "memory", Host: memoryPipe}
	memDial, err := proxyclient.NewClient(memURL)
	if err != nil {
		_ = console.Close()
		return nil, fmt.Errorf("proxyclient.NewClient(memory): %w", err)
	}
	return &memoryBridgeClient{
		console:   console,
		agent:     a,
		memClient: memDial,
	}, nil
}

func setupGenericAgentEnv(t *testing.T, cfg agentHarnessConfig) (*hagent.Env, func()) {
	t.Helper()

	if cfg.SetupEnv != nil {
		cfg.SetupEnv(t)
	}

	resolvedURL, closeServer := startInProcessServer(t, cfg.ServerURL, cfg.ValidateFn)

	alias := cfg.Alias
	if alias == "" {
		alias = fmt.Sprintf("harness-agent-%d", atomic.AddUint32(&testCounter, 1))
	}

	client, err := startMemoryClient(t, resolvedURL, alias)
	if err != nil {
		closeServer()
		t.Fatalf("startMemoryClient: %v", err)
	}

	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return client.memClient(ctx, network, addr)
	}

	cleanup := func() {
		client.Close()
		closeServer()
	}

	return &hagent.Env{Dial: dial}, cleanup
}

func setupGenericMultiAgentEnv(t *testing.T, cfg agentHarnessConfig, clientCount int) (*hagent.MultiClientEnv, func()) {
	t.Helper()
	if clientCount < 2 {
		clientCount = 2
	}

	if cfg.SetupEnv != nil {
		cfg.SetupEnv(t)
	}

	resolvedURL, closeServer := startInProcessServer(t, cfg.ServerURL, cfg.ValidateFn)
	clients := make([]*memoryBridgeClient, 0, clientCount)
	env := &hagent.MultiClientEnv{
		Clients: make([]hagent.ClientEnv, 0, clientCount),
	}

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			for i := len(clients) - 1; i >= 0; i-- {
				_ = clients[i].Close()
			}
			closeServer()
		})
	}

	for i := 0; i < clientCount; i++ {
		alias := cfg.Alias
		if alias == "" {
			alias = fmt.Sprintf("harness-agent-%d-%d", atomic.AddUint32(&testCounter, 1), i)
		} else {
			alias = fmt.Sprintf("%s-%d", cfg.Alias, i)
		}

		client, err := startMemoryClient(t, resolvedURL, alias)
		if err != nil {
			cleanup()
			t.Fatalf("startMemoryClient[%d]: %v", i, err)
		}
		clients = append(clients, client)

		c := client
		dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
			return c.memClient(ctx, network, addr)
		}
		env.Clients = append(env.Clients, hagent.ClientEnv{
			Env:  hagent.Env{Dial: dial},
			Name: alias,
		})
	}

	return env, cleanup
}
