package socks

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	xsocks5 "github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}

func writeConnectHandshake(conn net.Conn) error {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x1F, 0x90}); err != nil {
		return err
	}
	return nil
}

type memoryAddr string

func (a memoryAddr) Network() string { return "memory" }
func (a memoryAddr) String() string  { return string(a) }

type memoryConn struct {
	r *bytes.Reader
	w bytes.Buffer
}

func newMemoryConn(handshake []byte) *memoryConn {
	b := make([]byte, len(handshake))
	copy(b, handshake)
	return &memoryConn{r: bytes.NewReader(b)}
}

func (c *memoryConn) Read(p []byte) (int, error)         { return c.r.Read(p) }
func (c *memoryConn) Write(p []byte) (int, error)        { return c.w.Write(p) }
func (c *memoryConn) Close() error                       { return nil }
func (c *memoryConn) LocalAddr() net.Addr                { return memoryAddr("memory-local") }
func (c *memoryConn) RemoteAddr() net.Addr               { return memoryAddr("memory-remote") }
func (c *memoryConn) SetDeadline(t time.Time) error      { _ = t; return nil }
func (c *memoryConn) SetReadDeadline(t time.Time) error  { _ = t; return nil }
func (c *memoryConn) SetWriteDeadline(t time.Time) error { _ = t; return nil }

func BenchmarkSocks5ParseConn(b *testing.B) {
	server, err := NewSocks5Server("", "", nil)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		serverConn, clientConn := net.Pipe()
		errCh := make(chan error, 1)
		go func() {
			defer clientConn.Close()
			errCh <- writeConnectHandshake(clientConn)
		}()

		if _, err := server.ParseConn(context.Background(), serverConn); err != nil {
			serverConn.Close()
			b.Fatal(err)
		}
		serverConn.Close()
		if err := <-errCh; err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSocks5ParseConnMemory(b *testing.B) {
	server, err := NewSocks5Server("", "", nil)
	if err != nil {
		b.Fatal(err)
	}

	handshake := []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x1F, 0x90}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		conn := newMemoryConn(handshake)
		if _, err := server.ParseConn(context.Background(), conn); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSocks5PluginRelay(b *testing.B) {
	inbound, err := NewSocksInbound(nil)
	if err != nil {
		b.Fatal(err)
	}
	sp := inbound.(*Socks5Plugin)

	reply, err := xsocks5.BuildReply(xsocks5.SuccessReply, &xsocks5.AddrSpec{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 1080,
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		clientConn, relayConn := net.Pipe()
		bridgeLocal, bridgeRemote := net.Pipe()

		clientErr := make(chan error, 1)
		bridgeErr := make(chan error, 1)

		go func() {
			defer relayConn.Close()
			if err := writeConnectHandshake(relayConn); err != nil {
				clientErr <- err
				return
			}
			resp := make([]byte, len(reply))
			_, err := io.ReadFull(relayConn, resp)
			clientErr <- err
		}()

		go func() {
			defer bridgeRemote.Close()
			buf := make([]byte, 13) // relay auth + connect request (ipv4)
			if _, err := io.ReadFull(bridgeRemote, buf); err != nil {
				bridgeErr <- err
				return
			}
			_, err := bridgeRemote.Write(reply)
			bridgeErr <- err
		}()

		if _, err := sp.Relay(clientConn, bridgeLocal); err != nil {
			clientConn.Close()
			bridgeLocal.Close()
			b.Fatal(err)
		}

		clientConn.Close()
		bridgeLocal.Close()
		if err := <-clientErr; err != nil {
			b.Fatal(err)
		}
		if err := <-bridgeErr; err != nil {
			b.Fatal(err)
		}
	}
}
