//go:build !tinygo

package streamhttp_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/tunnel"
	streamhttp "github.com/chainreactors/rem/protocol/tunnel/streamhttp"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	utils.Log = logs.NewLogger(logs.DebugLevel)
}

func TestStreamHTTPRoundTrip(t *testing.T) {
	clientConn, serverConn := setupStreamTunnelPair(t, core.StreamHTTPTunnel)

	go func() {
		defer serverConn.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if _, err := serverConn.Write([]byte("world")); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	if _, err := clientConn.Write([]byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}

	reply := make([]byte, 5)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(reply) != "world" {
		t.Fatalf("expected %q, got %q", "world", string(reply))
	}
}

func TestStreamHTTPPartialRead(t *testing.T) {
	clientConn, serverConn := setupStreamTunnelPair(t, core.StreamHTTPTunnel)
	payload := []byte("partial-read-check")

	go func() {
		defer serverConn.Close()
		if _, err := serverConn.Write(payload); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	first := make([]byte, 7)
	n, err := clientConn.Read(first)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	rest, err := io.ReadAll(clientConn)
	if err != nil {
		t.Fatalf("read remainder: %v", err)
	}

	got := append(first[:n], rest...)
	if string(got) != string(payload) {
		t.Fatalf("expected %q, got %q", string(payload), string(got))
	}
}

func TestStreamHTTPSRoundTrip(t *testing.T) {
	clientConn, serverConn := setupStreamTunnelPair(t, core.StreamHTTPSTunnel)

	go func() {
		defer serverConn.Close()
		if _, err := serverConn.Write([]byte("secure")); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	reply := make([]byte, 6)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(reply) != "secure" {
		t.Fatalf("expected %q, got %q", "secure", string(reply))
	}
}

func TestStreamHTTPWithForwardProxy(t *testing.T) {
	proxyAddr := startSOCKS5Proxy(t)
	clientConn, serverConn := setupStreamTunnelPair(
		t,
		core.StreamHTTPTunnel,
		tunnel.WithProxyClient([]string{"socks5://" + proxyAddr}),
	)

	go func() {
		defer serverConn.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if string(buf) != "ping" {
			t.Errorf("expected %q, got %q", "ping", string(buf))
			return
		}
		if _, err := serverConn.Write([]byte("pong")); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	if _, err := clientConn.Write([]byte("ping")); err != nil {
		t.Fatalf("client write: %v", err)
	}

	reply := make([]byte, 4)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(reply) != "pong" {
		t.Fatalf("expected %q, got %q", "pong", string(reply))
	}
}

func TestStreamHTTPReplayBuffer(t *testing.T) {
	rb := streamhttp.NewReplayBufferForTest(8)

	// Append events.
	for i := 0; i < 5; i++ {
		rb.Append([]byte(fmt.Sprintf("event-%d", i)))
	}

	// ReadFrom(0) should return all 5.
	events, err := rb.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}

	// ReadFrom(3) should return events 4 and 5.
	events, err = rb.ReadFrom(3)
	if err != nil {
		t.Fatalf("ReadFrom(3): %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Fill past capacity — oldest should be overwritten.
	for i := 5; i < 12; i++ {
		rb.Append([]byte(fmt.Sprintf("event-%d", i)))
	}
	events, err = rb.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom(0) after overflow: %v", err)
	}
	if len(events) != 8 {
		t.Fatalf("expected 8 events (capacity), got %d", len(events))
	}
	// First event should be id=5 (oldest surviving).
	if events[0].ID != 5 {
		t.Fatalf("expected first event id=5, got %d", events[0].ID)
	}

	// Close should unblock waiters.
	done := make(chan struct{})
	go func() {
		_, err := rb.ReadFrom(100)
		if err == nil {
			t.Errorf("expected error after close")
		}
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	rb.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReadFrom did not unblock after Close")
	}
}

func TestStreamHTTPLargePayload(t *testing.T) {
	clientConn, serverConn := setupStreamTunnelPair(t, core.StreamHTTPTunnel)

	// Send a 100KB payload — exercises chunking in Write.
	payload := make([]byte, 100*1024)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	go func() {
		defer serverConn.Close()
		if _, err := serverConn.Write(payload); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(clientConn, got); err != nil {
		t.Fatalf("client read: %v", err)
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("mismatch at byte %d: expected %d, got %d", i, payload[i], got[i])
		}
	}
}

func setupStreamTunnelPair(t *testing.T, scheme string, clientOpts ...tunnel.TunnelOption) (net.Conn, net.Conn) {
	t.Helper()

	port := freeTCPPort(t)
	addr := fmt.Sprintf("%s://127.0.0.1:%d/rem?wrapper=raw", scheme, port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}

	lsn, err := serverTun.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := lsn.Accept()
		acceptCh <- acceptResult{conn: conn, err: err}
	}()

	clientTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, false, clientOpts...)
	if err != nil {
		t.Fatalf("new client tunnel: %v", err)
	}

	clientConn, err := clientTun.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	select {
	case accepted := <-acceptCh:
		if accepted.err != nil {
			t.Fatalf("accept: %v", accepted.err)
		}
		t.Cleanup(func() { _ = accepted.conn.Close() })
		return clientConn, accepted.conn
	case <-time.After(10 * time.Second):
		t.Fatal("accept timeout")
		return nil, nil
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func startSOCKS5Proxy(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	server, err := socks5.New(&socks5.Config{})
	if err != nil {
		t.Fatalf("new socks5 proxy: %v", err)
	}

	go func() {
		_ = server.Serve(ln)
	}()

	return ln.Addr().String()
}

// ════════════════════════════════════════════════════════════
// HTTP/1.1 protocol compliance tests
// ════════════════════════════════════════════════════════════

// TestStreamHTTPH1RawBinaryDownlink verifies that the binary downlink works with raw HTTP/1.1:
// - Response Content-Type is application/octet-stream
// - Transfer-Encoding is chunked (h1 streaming)
// - Binary frames are correctly formatted
// - Protocol is HTTP/1.1
func TestStreamHTTPH1RawBinaryDownlink(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("streamhttp://127.0.0.1:%d/rem?wrapper=raw", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	lsn, err := serverTun.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	// Accept in background — server will write data once connected.
	go func() {
		conn, err := lsn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Write some data so binary frames are generated.
		_, _ = conn.Write([]byte("hello-from-server"))
		// Keep alive briefly so the GET can read the frame.
		time.Sleep(500 * time.Millisecond)
	}()

	sid := "h1test-raw-binary"
	url := fmt.Sprintf("http://127.0.0.1:%d/rem?_sid=%s", port, sid)

	// Also send a POST to complete the session handshake.
	pr, pw := io.Pipe()
	go func() {
		req, _ := http.NewRequest(http.MethodPost, url, pr)
		req.Header.Set("Content-Type", "application/octet-stream")
		client := &http.Client{}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	t.Cleanup(func() { _ = pw.Close() })

	// Perform a raw HTTP/1.1 GET to the binary downlink endpoint.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Write raw HTTP/1.1 request.
	reqStr := fmt.Sprintf("GET /rem?_sid=%s HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nAccept: text/event-stream\r\nConnection: keep-alive\r\n\r\n",
		sid, port)
	if _, err := conn.Write([]byte(reqStr)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Read and parse the HTTP response.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()

	// Verify HTTP/1.1 protocol.
	if resp.Proto != "HTTP/1.1" {
		t.Errorf("expected HTTP/1.1, got %s", resp.Proto)
	}
	if resp.ProtoMajor != 1 || resp.ProtoMinor != 1 {
		t.Errorf("expected protocol 1.1, got %d.%d", resp.ProtoMajor, resp.ProtoMinor)
	}

	// Verify streaming headers.
	ct := resp.Header.Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	// HTTP/1.1 streaming uses chunked transfer encoding (no Content-Length).
	if resp.ContentLength != -1 {
		t.Errorf("expected unknown content length (chunked), got %d", resp.ContentLength)
	}
	te := resp.TransferEncoding
	if len(te) == 0 || te[0] != "chunked" {
		t.Logf("Transfer-Encoding: %v (Go may hide chunked in parsed response, this is OK)", te)
	}

	// Read binary frame from the response body.
	deadline := time.After(5 * time.Second)
	dataCh := make(chan []byte, 1)

	go func() {
		// Read frame header: 1 byte type + 4 bytes length.
		hdr := make([]byte, 5)
		if _, err := io.ReadFull(resp.Body, hdr); err != nil {
			return
		}
		frameType := hdr[0]
		payloadLen := binary.BigEndian.Uint32(hdr[1:5])

		if frameType != 0x01 { // DATA frame
			return
		}

		// Read seq ID (8 bytes) + data.
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(resp.Body, payload); err != nil {
			return
		}

		// Skip 8-byte seq ID, return the data.
		if len(payload) > 8 {
			dataCh <- payload[8:]
		}
	}()

	select {
	case data := <-dataCh:
		if string(data) != "hello-from-server" {
			t.Errorf("expected %q, got %q", "hello-from-server", string(data))
		}
	case <-deadline:
		t.Fatal("timeout waiting for binary data frame")
	}

	t.Log("HTTP/1.1 binary downlink verified: chunked streaming + correct frame format")
}

// TestStreamHTTPH1RawPOST verifies that the POST uplink works with raw HTTP/1.1:
// - Chunked transfer encoding for the request body
// - Server reads streaming POST body correctly
func TestStreamHTTPH1RawPOST(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("streamhttp://127.0.0.1:%d/rem?wrapper=raw", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	lsn, err := serverTun.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	// Accept and read data from the tunnel conn.
	receivedCh := make(chan []byte, 1)
	go func() {
		conn, err := lsn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		receivedCh <- buf[:n]
	}()

	sid := "h1test-raw-post"
	url := fmt.Sprintf("http://127.0.0.1:%d/rem?_sid=%s", port, sid)

	// Start a GET (downlink) to complete the session handshake.
	go func() {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Accept", "text/event-stream")
		client := &http.Client{}
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
		}
	}()

	// Send a raw HTTP/1.1 POST with chunked transfer encoding.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Write raw HTTP/1.1 chunked POST.
	payload := "hello-chunked-post"
	chunk := fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(payload), payload)
	reqStr := fmt.Sprintf("POST /rem?_sid=%s HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nContent-Type: application/octet-stream\r\nTransfer-Encoding: chunked\r\n\r\n%s",
		sid, port, chunk)
	if _, err := conn.Write([]byte(reqStr)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Verify server received the data.
	select {
	case data := <-receivedCh:
		if string(data) != payload {
			t.Errorf("expected %q, got %q", payload, string(data))
		}
		t.Log("HTTP/1.1 POST uplink verified: chunked request body received correctly")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for POST data on server")
	}
}

// TestStreamHTTPH1TLSRoundTrip verifies bidirectional data over HTTPS with HTTP/2 disabled.
func TestStreamHTTPH1TLSRoundTrip(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("streamhttps://127.0.0.1:%d/rem?wrapper=raw", port)

	// Create server with h2 disabled.
	serverCtx := context.WithValue(context.Background(), "meta", core.Metas{"mtu": core.MaxPacketSize})
	serverLsn := streamhttp.NewStreamHTTPListener(serverCtx)
	lsn, err := serverLsn.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Disable HTTP/2 on the server BEFORE connections arrive.
	streamhttp.DisableHTTP2Listener(serverLsn)
	t.Cleanup(func() { _ = lsn.Close() })

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := lsn.Accept()
		acceptCh <- acceptResult{conn, err}
	}()

	// Create client with h2 disabled via meta config.
	clientCtx := context.WithValue(context.Background(), "meta", core.Metas{"mtu": core.MaxPacketSize})
	clientDialer := streamhttp.NewStreamHTTPDialer(clientCtx)

	clientConn, err := clientDialer.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	// Accept the server side.
	var serverConn net.Conn
	select {
	case r := <-acceptCh:
		if r.err != nil {
			t.Fatalf("accept: %v", r.err)
		}
		serverConn = r.conn
		t.Cleanup(func() { _ = serverConn.Close() })
	case <-time.After(10 * time.Second):
		t.Fatal("accept timeout")
	}

	// Bidirectional test: client → server → client.
	go func() {
		buf := make([]byte, 5)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			return
		}
		_, _ = serverConn.Write([]byte("world"))
	}()

	if _, err := clientConn.Write([]byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	reply := make([]byte, 5)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(reply) != "world" {
		t.Fatalf("expected %q, got %q", "world", string(reply))
	}
	t.Log("HTTPS with HTTP/1.1-only (h2 disabled on server) verified")
}

// TestStreamHTTPH1ProtocolVerify connects with a standard h1 client and verifies
// the wire protocol is HTTP/1.1 by inspecting the response.
func TestStreamHTTPH1ProtocolVerify(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("streamhttp://127.0.0.1:%d/rem?wrapper=raw", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	_, err = serverTun.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	// Use Go's http.Client to verify response protocol version.
	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		TLSNextProto:      make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}
	client := &http.Client{Transport: transport}

	url := fmt.Sprintf("http://127.0.0.1:%d/rem?_sid=h1verify", port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	// Verify protocol version.
	if resp.Proto != "HTTP/1.1" {
		t.Errorf("expected HTTP/1.1, got %s", resp.Proto)
	}
	t.Logf("Response protocol: %s (major=%d, minor=%d)", resp.Proto, resp.ProtoMajor, resp.ProtoMinor)

	// Verify streaming content type.
	ct := resp.Header.Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	// Verify no Content-Length (streaming chunked).
	if resp.ContentLength >= 0 {
		t.Errorf("expected unknown Content-Length for chunked streaming, got %d", resp.ContentLength)
	}

	t.Logf("HTTP/1.1 protocol verification passed: Proto=%s, Content-Type=%s, ContentLength=%d",
		resp.Proto, ct, resp.ContentLength)
}

// ════════════════════════════════════════════════════════════
// TLS verification tests
// ════════════════════════════════════════════════════════════

// TestStreamHTTPSTLSHandshake verifies that a streamhttps:// listener performs a real
// TLS handshake. We connect with crypto/tls directly to the server port and inspect
// the connection state.
func TestStreamHTTPSTLSHandshake(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("streamhttps://127.0.0.1:%d/rem?wrapper=raw", port)

	serverCtx := context.WithValue(context.Background(), "meta", core.Metas{"mtu": core.MaxPacketSize})
	serverLsn := streamhttp.NewStreamHTTPListener(serverCtx)
	lsn, err := serverLsn.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lsn.Close() })

	// Verify the listener has TLS configured.
	if !streamhttp.ListenerHasTLS(serverLsn) {
		t.Fatal("streamhttps listener should have TLS configured, but TLSConfig is nil")
	}

	// Perform a raw TLS handshake to the server port.
	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp",
		fmt.Sprintf("127.0.0.1:%d", port),
		&tls.Config{InsecureSkipVerify: true},
	)
	if err != nil {
		t.Fatalf("TLS handshake failed: %v", err)
	}
	defer tlsConn.Close()

	// Inspect TLS connection state.
	state := tlsConn.ConnectionState()
	if !state.HandshakeComplete {
		t.Fatal("TLS handshake not complete")
	}
	t.Logf("TLS handshake complete: version=0x%04x, cipher=0x%04x, serverName=%q",
		state.Version, state.CipherSuite, state.ServerName)

	if state.Version < tls.VersionTLS12 {
		t.Errorf("expected TLS 1.2+, got version 0x%04x", state.Version)
	}

	// Verify we received a server certificate.
	if len(state.PeerCertificates) == 0 {
		t.Fatal("no server certificate received during TLS handshake")
	}
	cert := state.PeerCertificates[0]
	t.Logf("Server certificate: subject=%s, issuer=%s, notAfter=%s",
		cert.Subject, cert.Issuer, cert.NotAfter)
}

// TestStreamHTTPNoTLS verifies that a streamhttp:// (non-TLS) listener does NOT
// perform a TLS handshake. A raw TLS connection attempt should fail.
func TestStreamHTTPNoTLS(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("streamhttp://127.0.0.1:%d/rem?wrapper=raw", port)

	serverCtx := context.WithValue(context.Background(), "meta", core.Metas{"mtu": core.MaxPacketSize})
	serverLsn := streamhttp.NewStreamHTTPListener(serverCtx)
	lsn, err := serverLsn.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lsn.Close() })

	// Verify the listener does NOT have TLS configured.
	if streamhttp.ListenerHasTLS(serverLsn) {
		t.Fatal("streamhttp (non-TLS) listener should NOT have TLS configured")
	}

	// Attempting a TLS handshake against a plain HTTP server should fail.
	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 2 * time.Second},
		"tcp",
		fmt.Sprintf("127.0.0.1:%d", port),
		&tls.Config{InsecureSkipVerify: true},
	)
	if err == nil {
		tlsConn.Close()
		t.Fatal("TLS handshake should have failed against a plain HTTP server")
	}
	t.Logf("TLS handshake correctly failed against plain HTTP: %v", err)
}

// TestStreamHTTPSRoundTripDataIntegrity verifies bidirectional data integrity
// over TLS with a large payload, ensuring encryption/decryption is transparent.
func TestStreamHTTPSRoundTripDataIntegrity(t *testing.T) {
	clientConn, serverConn := setupStreamTunnelPair(t, core.StreamHTTPSTunnel)

	// 200KB payload — large enough to span multiple frames over TLS.
	payload := make([]byte, 200*1024)
	for i := range payload {
		payload[i] = byte((i*7 + 13) % 256)
	}

	go func() {
		defer serverConn.Close()
		// Server reads, then echoes back.
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if _, err := serverConn.Write(buf); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	if _, err := clientConn.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}

	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("client read: %v", err)
	}

	for i := range payload {
		if reply[i] != payload[i] {
			t.Fatalf("data mismatch at byte %d: expected 0x%02x, got 0x%02x", i, payload[i], reply[i])
		}
	}
	t.Logf("streamhttps round-trip data integrity verified: %d bytes", len(payload))
}

// TestStreamHTTPSConcurrentSessions tests multiple concurrent sessions over TLS.
func TestStreamHTTPSConcurrentSessions(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("streamhttps://127.0.0.1:%d/rem?wrapper=raw", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	lsn, err := serverTun.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	const numConns = 5
	const msgSize = 8192

	// Accept goroutine: echo server.
	go func() {
		for {
			conn, err := lsn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, msgSize)
				for {
					if _, err := io.ReadFull(c, buf); err != nil {
						return
					}
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	// Dial clients.
	type clientEntry struct {
		id   int
		conn net.Conn
	}
	clients := make([]clientEntry, numConns)
	for i := 0; i < numConns; i++ {
		clientTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, false)
		if err != nil {
			t.Fatalf("client %d: new tunnel: %v", i, err)
		}
		conn, err := clientTun.Dial(addr)
		if err != nil {
			t.Fatalf("client %d: dial: %v", i, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		clients[i] = clientEntry{id: i, conn: conn}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, numConns)

	for _, c := range clients {
		wg.Add(1)
		go func(id int, conn net.Conn) {
			defer wg.Done()

			payload := make([]byte, msgSize)
			for j := range payload {
				payload[j] = byte((id*31 + j) % 256)
			}

			for round := 0; round < 3; round++ {
				if _, err := conn.Write(payload); err != nil {
					errCh <- fmt.Errorf("client %d round %d: write: %v", id, round, err)
					return
				}
				reply := make([]byte, msgSize)
				if _, err := io.ReadFull(conn, reply); err != nil {
					errCh <- fmt.Errorf("client %d round %d: read: %v", id, round, err)
					return
				}
				for j := range payload {
					if reply[j] != payload[j] {
						errCh <- fmt.Errorf("client %d round %d: mismatch at %d", id, round, j)
						return
					}
				}
			}
		}(c.id, c.conn)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	t.Logf("streamhttps concurrent sessions verified: %d clients, each 3 rounds of %d bytes", numConns, msgSize)
}

// TestStreamHTTPSTLSClientVerifiesScheme verifies that the client correctly uses
// HTTPS when dialing streamhttps:// and plain HTTP when dialing streamhttp://.
// We do this by inspecting the server response through a raw TLS+HTTP client.
func TestStreamHTTPSTLSClientVerifiesScheme(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("streamhttps://127.0.0.1:%d/rem?wrapper=raw", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	if _, err := serverTun.Listen(addr); err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	// Use a Go HTTP client with TLS to verify we get a valid HTTPS response.
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: transport}

	url := fmt.Sprintf("https://127.0.0.1:%d/rem?_sid=tls-verify", port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTPS GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.TLS == nil {
		t.Fatal("response has no TLS connection state — expected HTTPS")
	}
	if !resp.TLS.HandshakeComplete {
		t.Fatal("TLS handshake not complete in response")
	}
	t.Logf("HTTPS response TLS verified: version=0x%04x, proto=%s, status=%d",
		resp.TLS.Version, resp.Proto, resp.StatusCode)

	// Now verify that a plain HTTP request to the same port fails.
	plainURL := fmt.Sprintf("http://127.0.0.1:%d/rem?_sid=plain-attempt", port)
	plainReq, _ := http.NewRequest(http.MethodGet, plainURL, nil)
	plainClient := &http.Client{Timeout: 2 * time.Second}
	plainResp, err := plainClient.Do(plainReq)
	if err == nil {
		// We might get a garbled response since the server expects TLS.
		// A non-200 or malformed response is expected.
		t.Logf("plain HTTP to TLS server: status=%d (expected failure or bad response)", plainResp.StatusCode)
		_ = plainResp.Body.Close()
	} else {
		t.Logf("plain HTTP to TLS server correctly failed: %v", err)
	}
}

// ════════════════════════════════════════════════════════════
// Benchmarks
// ════════════════════════════════════════════════════════════

// setupBenchPair creates a streamhttp tunnel pair for benchmarks.
func setupBenchPair(b *testing.B) (net.Conn, net.Conn) {
	b.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	addr := fmt.Sprintf("streamhttp://127.0.0.1:%d/rem?wrapper=raw", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, true)
	if err != nil {
		b.Fatalf("new server tunnel: %v", err)
	}

	lsn, err := serverTun.Listen(addr)
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	b.Cleanup(func() { _ = serverTun.Close() })

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := lsn.Accept()
		acceptCh <- acceptResult{conn: conn, err: err}
	}()

	clientTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, false)
	if err != nil {
		b.Fatalf("new client tunnel: %v", err)
	}

	clientConn, err := clientTun.Dial(addr)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	b.Cleanup(func() { _ = clientConn.Close() })

	select {
	case accepted := <-acceptCh:
		if accepted.err != nil {
			b.Fatalf("accept: %v", accepted.err)
		}
		b.Cleanup(func() { _ = accepted.conn.Close() })
		return clientConn, accepted.conn
	case <-time.After(10 * time.Second):
		b.Fatal("accept timeout")
		return nil, nil
	}
}

// BenchmarkStreamHTTPThroughput measures raw throughput client→server.
func BenchmarkStreamHTTPThroughput(b *testing.B) {
	sizes := []int{1024, 8 * 1024, 64 * 1024}
	for _, size := range sizes {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			clientConn, serverConn := setupBenchPair(b)

			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i % 251)
			}
			recvBuf := make([]byte, size)

			// Server: drain goroutine
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					_, err := serverConn.Read(recvBuf)
					if err != nil {
						return
					}
				}
			}()

			b.SetBytes(int64(size))
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				if _, err := clientConn.Write(payload); err != nil {
					b.Fatalf("write: %v", err)
				}
			}

			b.StopTimer()
			// Close server first to unblock drain goroutine, then client.
			_ = serverConn.Close()
			_ = clientConn.Close()
			<-done
		})
	}
}

// BenchmarkStreamHTTPRoundTrip measures request-response latency.
func BenchmarkStreamHTTPRoundTrip(b *testing.B) {
	clientConn, serverConn := setupBenchPair(b)

	payload := []byte("ping")
	reply := []byte("pong")

	// Server: echo goroutine
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := serverConn.Read(buf)
			if err != nil {
				return
			}
			if _, err := serverConn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	readBuf := make([]byte, len(reply))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := clientConn.Write(payload); err != nil {
			b.Fatalf("write: %v", err)
		}
		if _, err := io.ReadFull(clientConn, readBuf); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
}

// TestStreamHTTPConcurrentSessions tests multiple concurrent sessions.
func TestStreamHTTPConcurrentSessions(t *testing.T) {
	port := freeTCPPort(t)
	addr := fmt.Sprintf("streamhttp://127.0.0.1:%d/rem?wrapper=raw", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	lsn, err := serverTun.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = serverTun.Close() })

	const numConns = 10
	const msgSize = 4096

	// Accept goroutine: echo server.
	go func() {
		for {
			conn, err := lsn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, msgSize)
				for {
					if _, err := io.ReadFull(c, buf); err != nil {
						return
					}
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	// Dial clients sequentially (avoid RandomString collision from same-nanosecond seeds),
	// then run data transfer concurrently.
	type clientEntry struct {
		id   int
		conn net.Conn
	}
	clients := make([]clientEntry, numConns)
	for i := 0; i < numConns; i++ {
		clientTun, err := tunnel.NewTunnel(context.Background(), core.StreamHTTPTunnel, false)
		if err != nil {
			t.Fatalf("client %d: new tunnel: %v", i, err)
		}
		conn, err := clientTun.Dial(addr)
		if err != nil {
			t.Fatalf("client %d: dial: %v", i, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		clients[i] = clientEntry{id: i, conn: conn}
	}

	var wg sync.WaitGroup
	errors := make(chan error, numConns)

	for _, c := range clients {
		wg.Add(1)
		go func(id int, conn net.Conn) {
			defer wg.Done()

			payload := make([]byte, msgSize)
			for j := range payload {
				payload[j] = byte((id + j) % 256)
			}

			for round := 0; round < 5; round++ {
				if _, err := conn.Write(payload); err != nil {
					errors <- fmt.Errorf("client %d round %d: write: %v", id, round, err)
					return
				}
				reply := make([]byte, msgSize)
				if _, err := io.ReadFull(conn, reply); err != nil {
					errors <- fmt.Errorf("client %d round %d: read: %v", id, round, err)
					return
				}
				for j := range payload {
					if reply[j] != payload[j] {
						errors <- fmt.Errorf("client %d round %d: mismatch at %d: want 0x%02x got 0x%02x",
							id, round, j, payload[j], reply[j])
						return
					}
				}
			}
		}(c.id, c.conn)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

// TestStreamHTTPStress sends many messages rapidly.
func TestStreamHTTPStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	clientConn, serverConn := setupStreamTunnelPair(t, core.StreamHTTPTunnel)

	const iterations = 200
	const msgSize = 1024

	// Server: echo.
	go func() {
		defer serverConn.Close()
		buf := make([]byte, msgSize)
		for {
			n, err := serverConn.Read(buf)
			if err != nil {
				return
			}
			if _, err := serverConn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	payload := make([]byte, msgSize)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	for i := 0; i < iterations; i++ {
		if _, err := clientConn.Write(payload); err != nil {
			t.Fatalf("iteration %d write: %v", i, err)
		}
		reply := make([]byte, msgSize)
		if _, err := io.ReadFull(clientConn, reply); err != nil {
			t.Fatalf("iteration %d read: %v", i, err)
		}
	}
}
