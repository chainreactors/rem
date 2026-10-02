// Package agent provides conformance test suites for full agent pipelines.
//
// An agent pipeline is: Console → Tunnel → Serve → Proxy.
// The harness verifies traffic flows through the agent by connecting to an
// echo HTTP server via the agent's dial function.
//
// Four levels:
//
//   - TestProxy: basic proxy correctness (GET, POST, large payload, concurrent)
//   - TestMultiClient: one server serving multiple independent clients
//   - TestResilience: server restart, client restart, network outage, idle resume
//   - TestStress: burst traffic, large payloads
//
// Usage:
//
//	func TestMyAgent_Proxy(t *testing.T) {
//	    agent.TestProxy(t, func(t *testing.T) (*agent.Env, func(), error) {
//	        // Env.Dial can be SOCKS5, memory bridge, or any transport
//	        return &agent.Env{Dial: myDialFunc}, cleanup, nil
//	    })
//	}
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// ── Types ────────────────────────────────────────────────────

// Dial is a function that connects to a network address through the agent.
type Dial func(ctx context.Context, network, addr string) (net.Conn, error)

// Env represents a running agent environment.
type Env struct {
	// Dial connects through the agent's proxy (SOCKS5, memory bridge, etc.).
	Dial Dial
}

// MakeEnv creates an agent environment. The Dial function must be ready.
type MakeEnv func(t *testing.T) (env *Env, stop func(), err error)

// ClientEnv is one client-side proxy attached to a shared agent server.
type ClientEnv struct {
	Env
	Name string
}

// MultiClientEnv represents a single server with multiple connected clients.
type MultiClientEnv struct {
	Clients []ClientEnv
}

// MakeMultiClientEnv creates one server and clientCount clients connected to it.
type MakeMultiClientEnv func(t *testing.T, clientCount int) (env *MultiClientEnv, stop func(), err error)

// MultiClientOptions tunes TestMultiClient. Zero values select conservative defaults.
type MultiClientOptions struct {
	Clients           int
	RequestsPerClient int
	PayloadSize       int
	RequestTimeout    time.Duration
}

// ControllableEnv extends Env with lifecycle controls for resilience testing.
type ControllableEnv struct {
	Env
	StopServer  func()
	StartServer func() error
	StopClient  func()
	StartClient func() error
}

// MakeControllableEnv creates a controllable agent environment.
type MakeControllableEnv func(t *testing.T) (env *ControllableEnv, stop func(), err error)

// ── Constructors for common Dial implementations ─────────────

// SOCKS5Dial creates a Dial function that connects through a SOCKS5 proxy.
func SOCKS5Dial(socksAddr string) Dial {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, &net.Dialer{Timeout: 10 * time.Second})
		if err != nil {
			return nil, err
		}
		return dialer.Dial(network, addr)
	}
}

// TestAll runs the standard agent proxy and stress harness suites.
func TestAll(t *testing.T, makeEnv MakeEnv) {
	t.Helper()
	TestProxy(t, makeEnv)
	TestStress(t, makeEnv)
}

// TestProxyWithMultiClient runs proxy correctness plus the shared-server
// multi-client suite.
func TestProxyWithMultiClient(t *testing.T, makeEnv MakeEnv, makeMultiEnv MakeMultiClientEnv) {
	t.Helper()
	TestProxy(t, makeEnv)
	TestMultiClient(t, makeMultiEnv)
}

// TestAllWithMultiClient runs the standard suites plus the shared-server
// multi-client suite.
func TestAllWithMultiClient(t *testing.T, makeEnv MakeEnv, makeMultiEnv MakeMultiClientEnv) {
	t.Helper()
	TestAll(t, makeEnv)
	TestMultiClient(t, makeMultiEnv)
}

// ── TestMultiClient: 单 server 多 client ─────────────────────

// TestMultiClient verifies one server can serve multiple independent clients.
func TestMultiClient(t *testing.T, makeEnv MakeMultiClientEnv) {
	t.Helper()
	TestMultiClientWithOptions(t, makeEnv, MultiClientOptions{})
}

// TestMultiClientWithOptions verifies one server can serve multiple independent
// clients with caller-provided scale parameters.
func TestMultiClientWithOptions(t *testing.T, makeEnv MakeMultiClientEnv, opts MultiClientOptions) {
	t.Helper()
	opts = normalizeMultiClientOptions(opts)
	echoServer := startEchoServer(t)

	env, stop, err := makeEnv(t, opts.Clients)
	if err != nil {
		t.Fatalf("MakeMultiClientEnv: %v", err)
	}
	defer stop()
	validateMultiClientEnv(t, env, opts.Clients)

	t.Run("MultiClient_BasicHTTP", func(t *testing.T) {
		for i := range env.Clients {
			clientEnv := env.Clients[i]
			client := envHTTPClient(&clientEnv.Env)
			client.Timeout = opts.RequestTimeout
			path := fmt.Sprintf("/hello?client=%d", i)
			resp, err := client.Get(echoServer.URL + path)
			if err != nil {
				t.Fatalf("%s GET: %v", clientName(clientEnv, i), err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s status %d, body: %s", clientName(clientEnv, i), resp.StatusCode, body)
			}
			if !bytes.Contains(body, []byte(path)) {
				t.Fatalf("%s response routed incorrectly: %q", clientName(clientEnv, i), body)
			}
		}
	})

	t.Run("MultiClient_ConcurrentHTTP", func(t *testing.T) {
		var wg sync.WaitGroup
		var success, failures atomic.Int32
		for i := range env.Clients {
			clientEnv := env.Clients[i]
			for r := 0; r < opts.RequestsPerClient; r++ {
				wg.Add(1)
				go func(idx, req int, ce ClientEnv) {
					defer wg.Done()
					client := envHTTPClient(&ce.Env)
					client.Timeout = opts.RequestTimeout
					path := fmt.Sprintf("/hello?client=%d&req=%d", idx, req)
					resp, err := client.Get(echoServer.URL + path)
					if err != nil {
						t.Logf("%s req %d: %v", clientName(ce, idx), req, err)
						failures.Add(1)
						return
					}
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(path)) {
						t.Logf("%s req %d: bad response status=%d body=%q", clientName(ce, idx), req, resp.StatusCode, body)
						failures.Add(1)
						return
					}
					success.Add(1)
				}(i, r, clientEnv)
			}
		}
		wg.Wait()

		want := int32(opts.Clients * opts.RequestsPerClient)
		if success.Load() != want {
			t.Fatalf("multi-client concurrent: %d/%d success, %d failures", success.Load(), want, failures.Load())
		}
	})

	t.Run("MultiClient_PayloadIntegrity", func(t *testing.T) {
		wantHash := fmt.Sprintf("%x", sha256.Sum256(makePayload(opts.PayloadSize)))
		for i := range env.Clients {
			clientEnv := env.Clients[i]
			client := envHTTPClient(&clientEnv.Env)
			client.Timeout = opts.RequestTimeout
			resp, err := client.Get(fmt.Sprintf("%s/data?size=%d", echoServer.URL, opts.PayloadSize))
			if err != nil {
				t.Fatalf("%s data GET: %v", clientName(clientEnv, i), err)
			}
			h := sha256.New()
			n, copyErr := io.Copy(h, resp.Body)
			resp.Body.Close()
			if copyErr != nil {
				t.Fatalf("%s data read: %v", clientName(clientEnv, i), copyErr)
			}
			if int(n) != opts.PayloadSize {
				t.Fatalf("%s data size: got %d, want %d", clientName(clientEnv, i), n, opts.PayloadSize)
			}
			if gotHash := fmt.Sprintf("%x", h.Sum(nil)); gotHash != wantHash {
				t.Fatalf("%s SHA256 mismatch", clientName(clientEnv, i))
			}
		}
	})
}

func normalizeMultiClientOptions(opts MultiClientOptions) MultiClientOptions {
	if opts.Clients <= 0 {
		opts.Clients = 3
	}
	if opts.Clients < 2 {
		opts.Clients = 2
	}
	if opts.RequestsPerClient <= 0 {
		opts.RequestsPerClient = 2
	}
	if opts.PayloadSize <= 0 {
		opts.PayloadSize = 64 * 1024
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 2 * time.Minute
	}
	return opts
}

func validateMultiClientEnv(t *testing.T, env *MultiClientEnv, want int) {
	t.Helper()
	if env == nil {
		t.Fatal("MakeMultiClientEnv returned nil env")
	}
	if len(env.Clients) != want {
		t.Fatalf("client count: got %d, want %d", len(env.Clients), want)
	}
	for i, c := range env.Clients {
		if c.Dial == nil {
			t.Fatalf("client %d has nil Dial", i)
		}
	}
}

func clientName(c ClientEnv, fallback int) string {
	if c.Name != "" {
		return c.Name
	}
	return fmt.Sprintf("client-%d", fallback)
}

// ── TestProxy: 基本代理正确性 ────────────────────────────────

// TestProxy verifies basic proxy functionality.
func TestProxy(t *testing.T, makeEnv MakeEnv) {
	t.Helper()
	echoServer := startEchoServer(t)

	t.Run("BasicHTTPGet", func(t *testing.T) {
		env, stop, err := makeEnv(t)
		if err != nil {
			t.Fatalf("MakeEnv: %v", err)
		}
		defer stop()

		client := envHTTPClient(env)
		resp, err := client.Get(echoServer.URL + "/hello")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, body: %s", resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte("/hello")) {
			t.Fatalf("body missing path: %q", body)
		}
	})

	for _, size := range []int{256 * 1024, 1024 * 1024} {
		label := formatSize(size)
		t.Run("LargePayload_"+label, func(t *testing.T) {
			env, stop, err := makeEnv(t)
			if err != nil {
				t.Fatalf("MakeEnv: %v", err)
			}
			defer stop()

			client := envHTTPClient(env)
			resp, err := client.Get(fmt.Sprintf("%s/data?size=%d", echoServer.URL, size))
			if err != nil {
				t.Fatalf("GET /data: %v", err)
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)
			if len(body) != size {
				t.Fatalf("size: got %d, want %d", len(body), size)
			}
			if !bytes.Equal(body, makePayload(size)) {
				t.Fatalf("%s: data integrity failure", label)
			}
			t.Logf("%s verified OK", label)
		})
	}

	t.Run("Concurrent_10x5", func(t *testing.T) {
		env, stop, err := makeEnv(t)
		if err != nil {
			t.Fatalf("MakeEnv: %v", err)
		}
		defer stop()

		const nWorkers, reqsPerWorker = 10, 5
		var wg sync.WaitGroup
		var success, errors atomic.Int32

		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				c := envHTTPClient(env)
				for r := 0; r < reqsPerWorker; r++ {
					resp, err := c.Get(fmt.Sprintf("%s/hello?w=%d&r=%d", echoServer.URL, id, r))
					if err != nil {
						errors.Add(1)
						continue
					}
					io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						success.Add(1)
					} else {
						errors.Add(1)
					}
				}
			}(w)
		}
		wg.Wait()

		ok := success.Load()
		t.Logf("concurrent: %d/%d success", ok, nWorkers*reqsPerWorker)
		if ok < int32(nWorkers*reqsPerWorker*8/10) {
			t.Fatalf("too many failures: %d errors", errors.Load())
		}
	})

	t.Run("DataIntegrity_SHA256", func(t *testing.T) {
		env, stop, err := makeEnv(t)
		if err != nil {
			t.Fatalf("MakeEnv: %v", err)
		}
		defer stop()

		client := envHTTPClient(env)
		size := 512 * 1024
		resp, err := client.Get(fmt.Sprintf("%s/data?size=%d", echoServer.URL, size))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()

		h := sha256.New()
		n, _ := io.Copy(h, resp.Body)
		if int(n) != size {
			t.Fatalf("size: got %d, want %d", n, size)
		}
		got := fmt.Sprintf("%x", h.Sum(nil))
		want := fmt.Sprintf("%x", sha256.Sum256(makePayload(size)))
		if got != want {
			t.Fatal("SHA256 mismatch")
		}
	})
}

// ── TestResilience: 韧性验证 ─────────────────────────────────

// TestResilience verifies the agent recovers from adverse conditions.
func TestResilience(t *testing.T, makeEnv MakeControllableEnv) {
	t.Helper()
	echoServer := startEchoServer(t)

	t.Run("ServerRestart", func(t *testing.T) {
		env, stop, err := makeEnv(t)
		if err != nil {
			t.Fatalf("MakeControllableEnv: %v", err)
		}
		defer stop()

		httpGetWithRetry(t, envHTTPClient(&env.Env), echoServer.URL+"/hello", "baseline", 60*time.Second)

		env.StopServer()
		time.Sleep(2 * time.Second)
		if err := env.StartServer(); err != nil {
			t.Fatalf("StartServer: %v", err)
		}

		httpGetWithRetry(t, envHTTPClient(&env.Env), echoServer.URL+"/hello", "after-restart", 120*time.Second)
	})

	t.Run("ClientRestart", func(t *testing.T) {
		env, stop, err := makeEnv(t)
		if err != nil {
			t.Fatalf("MakeControllableEnv: %v", err)
		}
		defer stop()

		httpGetWithRetry(t, envHTTPClient(&env.Env), echoServer.URL+"/hello", "baseline", 60*time.Second)

		env.StopClient()
		time.Sleep(2 * time.Second)
		if err := env.StartClient(); err != nil {
			t.Fatalf("StartClient: %v", err)
		}

		httpGetWithRetry(t, envHTTPClient(&env.Env), echoServer.URL+"/hello", "after-restart", 120*time.Second)
	})

	t.Run("IdleResume", func(t *testing.T) {
		env, stop, err := makeEnv(t)
		if err != nil {
			t.Fatalf("MakeControllableEnv: %v", err)
		}
		defer stop()

		httpGetWithRetry(t, envHTTPClient(&env.Env), echoServer.URL+"/hello", "baseline", 60*time.Second)
		t.Log("idle: sleeping 30s...")
		time.Sleep(30 * time.Second)
		httpGetWithRetry(t, envHTTPClient(&env.Env), echoServer.URL+"/hello", "after-idle", 120*time.Second)
	})
}

// ── TestStress: 压力验证 ─────────────────────────────────────

// TestStress verifies the agent handles high load and large payloads.
func TestStress(t *testing.T, makeEnv MakeEnv) {
	t.Helper()
	echoServer := startEchoServer(t)

	t.Run("BurstTraffic", func(t *testing.T) {
		env, stop, err := makeEnv(t)
		if err != nil {
			t.Fatalf("MakeEnv: %v", err)
		}
		defer stop()

		client := envHTTPClient(env)
		for i := 0; i < 10; i++ {
			resp, err := client.Get(fmt.Sprintf("%s/hello?burst=%d", echoServer.URL, i))
			if err != nil {
				t.Logf("burst %d: %v", i, err)
				continue
			}
			io.ReadAll(resp.Body)
			resp.Body.Close()
		}
	})

	for _, size := range []int{2 * 1024 * 1024, 5 * 1024 * 1024} {
		label := formatSize(size)
		t.Run("LargePayload_"+label, func(t *testing.T) {
			env, stop, err := makeEnv(t)
			if err != nil {
				t.Fatalf("MakeEnv: %v", err)
			}
			defer stop()

			client := envHTTPClient(env)
			client.Timeout = 5 * time.Minute

			start := time.Now()
			resp, err := client.Get(fmt.Sprintf("%s/data?size=%d", echoServer.URL, size))
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()

			h := sha256.New()
			n, err := io.Copy(h, resp.Body)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("read: %v (got %d/%d in %v)", err, n, size, elapsed)
			}

			got := fmt.Sprintf("%x", h.Sum(nil))
			want := fmt.Sprintf("%x", sha256.Sum256(makePayload(size)))
			if got != want {
				t.Fatal("SHA256 mismatch")
			}
			t.Logf("%s: verified OK in %v (%.1f KB/s)", label, elapsed, float64(size)/elapsed.Seconds()/1024)
		})
	}
}

// ── echo server ──────────────────────────────────────────────

func startEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s %s", r.Method, r.URL.RequestURI())
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		io.Copy(w, r.Body)
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		size := 1024
		fmt.Sscanf(r.URL.Query().Get("size"), "%d", &size)
		data := makePayload(size)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Write(data)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// ── HTTP client helpers ──────────────────────────────────────

// envHTTPClient creates an http.Client that dials through the agent.
func envHTTPClient(env *Env) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: env.Dial,
		},
		Timeout: 60 * time.Second,
	}
}

func httpGetWithRetry(t *testing.T, client *http.Client, url, label string, deadline time.Duration) {
	t.Helper()
	dl := time.Now().Add(deadline)
	var lastErr error
	for time.Now().Before(dl) {
		client.Timeout = 10 * time.Second
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(3 * time.Second)
			continue
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Logf("[%s] HTTP GET OK", label)
			return
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("[%s] failed after %v: %v", label, deadline, lastErr)
}

func makePayload(size int) []byte {
	p := make([]byte, size)
	for i := range p {
		p[i] = byte((i*7 + 13) % 251)
	}
	return p
}

func formatSize(size int) string {
	if size >= 1024*1024 {
		return fmt.Sprintf("%dMB", size/(1024*1024))
	}
	return fmt.Sprintf("%dKB", size/1024)
}
