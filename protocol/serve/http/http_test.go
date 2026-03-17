package http

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chainreactors/rem/internal/servetest"
	"github.com/chainreactors/rem/protocol/core"
)

func TestHandleConnectDoesNotCloseClientStreamOnSuccess(t *testing.T) {
	req, err := stdhttp.ReadRequest(bufio.NewReader(strings.NewReader(
		"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
	)))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}

	peer, remote := net.Pipe()
	defer remote.Close()

	var gotNetwork, gotAddress string
	hp := &HTTPProxy{
		PluginOption: core.NewPluginOption(map[string]string{}, core.OutboundPlugin, core.HTTPServe),
		dial: core.NewProxyDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
			gotNetwork = network
			gotAddress = address
			return peer, nil
		}),
	}

	stream := servetest.NewReadWriteCloser(nil)
	conn, err := hp.handleConnect(req, stream)
	if err != nil {
		t.Fatalf("handleConnect returned error: %v", err)
	}
	defer conn.Close()

	if stream.CloseCount != 0 {
		t.Fatalf("stream unexpectedly closed %d times", stream.CloseCount)
	}
	if gotNetwork != "tcp" || gotAddress != "example.com:443" {
		t.Fatalf("dial target mismatch: got %s %s", gotNetwork, gotAddress)
	}
	if !strings.Contains(stream.String(), "200 OK") {
		t.Fatalf("missing success response: %q", stream.String())
	}
}

func TestHandleSupportsPOSTRequests(t *testing.T) {
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if r.Method != stdhttp.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if string(body) != "ping" {
			t.Fatalf("unexpected request body: %q", string(body))
		}
		w.WriteHeader(stdhttp.StatusCreated)
		_, _ = w.Write([]byte("pong"))
	}))
	defer server.Close()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	hp := &HTTPProxy{
		PluginOption: core.NewPluginOption(map[string]string{}, core.OutboundPlugin, core.HTTPServe),
		httpClient:   server.Client(),
	}

	reqText := "POST " + server.URL + "/submit HTTP/1.1\r\nHost: " + strings.TrimPrefix(server.URL, "http://") + "\r\nContent-Length: 4\r\n\r\nping"
	stream := servetest.NewReadWriteCloserString(reqText)

	conn, err := hp.Handle(stream, serverConn)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	_ = conn.Close()

	response := stream.String()
	if !strings.Contains(response, "201 Created") {
		t.Fatalf("missing response status: %q", response)
	}
	if !strings.HasSuffix(response, "pong") {
		t.Fatalf("missing response body: %q", response)
	}
}

func TestHandleConnectRejectsUnauthenticatedClient(t *testing.T) {
	req, err := stdhttp.ReadRequest(bufio.NewReader(strings.NewReader(
		"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
	)))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}

	stream := servetest.NewReadWriteCloser(nil)
	hp := &HTTPProxy{
		PluginOption: core.NewPluginOption(map[string]string{
			"username": "alice",
			"password": "secret",
		}, core.OutboundPlugin, core.HTTPServe),
	}

	_, err = hp.handleConnect(req, stream)
	if err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("expected auth failed error, got %v", err)
	}
	if stream.CloseCount == 0 {
		t.Fatal("expected stream to be closed")
	}
	if !strings.Contains(stream.String(), "407") {
		t.Fatalf("missing 407 response: %q", stream.String())
	}
}

func TestHandleConnectRejectsInvalidHost(t *testing.T) {
	req := &stdhttp.Request{Host: "bad host"}

	stream := servetest.NewReadWriteCloser(nil)
	hp := &HTTPProxy{
		PluginOption: core.NewPluginOption(map[string]string{}, core.OutboundPlugin, core.HTTPServe),
		dial: core.NewProxyDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
			t.Fatalf("unexpected dial: %s %s", network, address)
			return nil, nil
		}),
	}

	_, err := hp.handleConnect(req, stream)
	if err == nil || !strings.Contains(err.Error(), "host invalid") {
		t.Fatalf("expected host invalid error, got %v", err)
	}
	if stream.CloseCount == 0 {
		t.Fatal("expected stream to be closed")
	}
	if !strings.Contains(stream.String(), "400") {
		t.Fatalf("missing 400 response: %q", stream.String())
	}
}

func TestHandleConnectRequiresDialer(t *testing.T) {
	req, err := stdhttp.ReadRequest(bufio.NewReader(strings.NewReader(
		"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
	)))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}

	stream := servetest.NewReadWriteCloser(nil)
	hp := &HTTPProxy{
		PluginOption: core.NewPluginOption(map[string]string{}, core.OutboundPlugin, core.HTTPServe),
	}

	_, err = hp.handleConnect(req, stream)
	if err == nil || !strings.Contains(err.Error(), "dialer not configured") {
		t.Fatalf("expected dialer error, got %v", err)
	}
	if stream.CloseCount == 0 {
		t.Fatal("expected stream to be closed")
	}
}

func TestHandleConnectPropagatesDialFailure(t *testing.T) {
	req, err := stdhttp.ReadRequest(bufio.NewReader(strings.NewReader(
		"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
	)))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}

	stream := servetest.NewReadWriteCloser(nil)
	dialErr := errors.New("dial failed")
	hp := &HTTPProxy{
		PluginOption: core.NewPluginOption(map[string]string{}, core.OutboundPlugin, core.HTTPServe),
		dial: core.NewProxyDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, dialErr
		}),
	}

	_, err = hp.handleConnect(req, stream)
	if !errors.Is(err, dialErr) {
		t.Fatalf("expected dial error, got %v", err)
	}
	if stream.CloseCount == 0 {
		t.Fatal("expected stream to be closed")
	}
	if !strings.Contains(stream.String(), "400") {
		t.Fatalf("missing 400 response: %q", stream.String())
	}
}

func TestRelayRejectsNonConnectMethod(t *testing.T) {
	conn := servetest.NewConnString("GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")

	hp := &HTTPProxy{
		PluginOption: core.NewPluginOption(map[string]string{}, core.OutboundPlugin, core.HTTPServe),
	}

	_, err := hp.Relay(conn, servetest.NewReadWriteCloser(nil))
	if err == nil || !strings.Contains(err.Error(), "unsupported method") {
		t.Fatalf("expected unsupported method error, got %v", err)
	}
	if conn.CloseCount == 0 {
		t.Fatal("expected connection to be closed")
	}
}
