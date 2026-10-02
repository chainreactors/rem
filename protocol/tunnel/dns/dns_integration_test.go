//go:build dns && !tinygo

package dns

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/tunnel"
)

func TestDNSTunnelConnects(t *testing.T) {
	port := freeDNSUDPPort(t)
	rawURL := fmt.Sprintf("dns://127.0.0.1:%d/?wrapper=raw&domain=rem.test.local&interval=20", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.DNSTunnel, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	defer serverTun.Close()
	if _, err := serverTun.Listen(rawURL); err != nil {
		t.Fatalf("server listen: %v", err)
	}

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := serverTun.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	clientTun, err := tunnel.NewTunnel(context.Background(), core.DNSTunnel, false)
	if err != nil {
		t.Fatalf("new client tunnel: %v", err)
	}
	defer clientTun.Close()
	clientConn, err := clientTun.Dial(rawURL)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("client deadline: %v", err)
	}
	if _, err := clientConn.Write([]byte("ping")); err != nil {
		t.Fatalf("client write: %v", err)
	}

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case err := <-acceptErr:
		t.Fatalf("accept error: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for DNS tunnel accept")
	}
	defer serverConn.Close()

	if err := serverConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("server deadline: %v", err)
	}

	buf := make([]byte, 4)
	if _, err := serverConn.Read(buf); err != nil {
		t.Fatalf("server read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("server read %q, want ping", string(buf))
	}
}

func TestDNSTunnelSequentialMessages(t *testing.T) {
	port := freeDNSUDPPort(t)
	rawURL := fmt.Sprintf("dns://127.0.0.1:%d/?wrapper=raw&domain=rem.test.local&interval=20", port)

	serverTun, err := tunnel.NewTunnel(context.Background(), core.DNSTunnel, true)
	if err != nil {
		t.Fatalf("new server tunnel: %v", err)
	}
	defer serverTun.Close()
	if _, err := serverTun.Listen(rawURL); err != nil {
		t.Fatalf("server listen: %v", err)
	}

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := serverTun.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	clientTun, err := tunnel.NewTunnel(context.Background(), core.DNSTunnel, false)
	if err != nil {
		t.Fatalf("new client tunnel: %v", err)
	}
	defer clientTun.Close()
	clientConn, err := clientTun.Dial(rawURL)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("client deadline: %v", err)
	}

	first := []byte("login")
	if _, err := clientConn.Write(first); err != nil {
		t.Fatalf("client write first: %v", err)
	}

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case err := <-acceptErr:
		t.Fatalf("accept error: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for DNS tunnel accept")
	}
	defer serverConn.Close()
	if err := serverConn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("server deadline: %v", err)
	}

	readExact(t, serverConn, first, "server first")
	if _, err := serverConn.Write([]byte("ack")); err != nil {
		t.Fatalf("server write ack: %v", err)
	}
	readExact(t, clientConn, []byte("ack"), "client ack")

	second := make([]byte, 512)
	for i := range second {
		second[i] = byte('a' + i%26)
	}
	if _, err := clientConn.Write(second); err != nil {
		t.Fatalf("client write second: %v", err)
	}
	readExact(t, serverConn, second, "server second")
}

func readExact(t *testing.T, conn net.Conn, want []byte, label string) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("%s read: %v", label, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s read mismatch: got %q want %q", label, string(got), string(want))
	}
}

func freeDNSUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}
