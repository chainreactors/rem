package runner

import (
	"net"
	"net/url"
	"testing"

	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/harness/netconn"
	"github.com/chainreactors/rem/protocol/core"
)

func TestNewConsole(t *testing.T) {
	u, err := url.Parse("rem+tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw")
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	proxy, err := proxyclient.NewClient(u)
	if err != nil {
		t.Fatalf("proxyclient.NewClient: %v", err)
	}
	if proxy == nil {
		t.Fatal("expected non-nil proxy dialer")
	}
}

func TestClient(t *testing.T) {
	t.Skip("manual integration test")
}

func TestRemProxy(t *testing.T) {
	t.Skip("manual integration test")
}

func TestRemHTTPProxy(t *testing.T) {
	t.Skip("manual integration test")
}

func TestServer(t *testing.T) {
	t.Skip("manual integration test")
}

// ---------------------------------------------------------------------------
// Merged from console_client_id_test.go
// ---------------------------------------------------------------------------

func TestPrepareClientConsoleURLInjectsOSSClientID(t *testing.T) {
	addr, err := core.NewConsoleURL("simplex+oss://bucket.example.com/rem?ak=test-ak&sk=test-sk&mode=file&wrapper=raw")
	if err != nil {
		t.Fatalf("NewConsoleURL: %v", err)
	}

	got := prepareClientConsoleURL(addr, "agent-123")
	if got.GetQuery("client_id") != "agent-123" {
		t.Fatalf("client_id = %q, want %q", got.GetQuery("client_id"), "agent-123")
	}
	if got.GetQuery("ak") != "test-ak" {
		t.Fatalf("access key changed: got %q", got.GetQuery("ak"))
	}
	if addr.GetQuery("client_id") != "" {
		t.Fatalf("original URL should remain unchanged, got client_id=%q", addr.GetQuery("client_id"))
	}
}

func TestPrepareClientConsoleURLInjectsSimplexFileClientID(t *testing.T) {
	tests := []string{
		"simplex+oss://bucket.oss-cn-hangzhou.aliyuncs.com/rem-prefix/?ak=test&sk=test&mode=file&wrapper=raw",
		"simplex+oss://bucket.oss-cn-hangzhou.aliyuncs.com/session?ak=test&sk=test&mode=file&prefix=rem-prefix&wrapper=raw",
	}

	for _, raw := range tests {
		addr, err := core.NewConsoleURL(raw)
		if err != nil {
			t.Fatalf("NewConsoleURL(%q): %v", raw, err)
		}

		got := prepareClientConsoleURL(addr, "agent-123")
		if got.GetQuery("client_id") != "agent-123" {
			t.Fatalf("%s client_id = %q, want %q", addr.RawScheme, got.GetQuery("client_id"), "agent-123")
		}
		if addr.GetQuery("client_id") != "" {
			t.Fatalf("%s original URL should remain unchanged, got client_id=%q", addr.RawScheme, addr.GetQuery("client_id"))
		}
	}
}

func TestPrepareClientConsoleURLPreservesExplicitClientID(t *testing.T) {
	addr, err := core.NewConsoleURL("simplex+oss://bucket.example.com/rem?ak=test-ak&sk=test-sk&mode=file&client_id=already-set&wrapper=raw")
	if err != nil {
		t.Fatalf("NewConsoleURL: %v", err)
	}

	got := prepareClientConsoleURL(addr, "agent-123")
	if got.GetQuery("client_id") != "already-set" {
		t.Fatalf("client_id = %q, want %q", got.GetQuery("client_id"), "already-set")
	}
}

// ---------------------------------------------------------------------------
// Merged from connhub_runtime_netconn_test.go
// ---------------------------------------------------------------------------

func TestMergeHalfConn_NetConn(t *testing.T) {
	netconn.TestConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		write1, read2 := net.Pipe()
		write2, read1 := net.Pipe()

		c1 = mergeHalfConn(read1, write1)
		c2 = mergeHalfConn(read2, write2)

		stop = func() {
			_ = c1.Close()
			_ = c2.Close()
		}
		return c1, c2, stop, nil
	})
}
