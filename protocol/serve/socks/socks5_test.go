package socks

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}

func TestNewSocks5ServerUserPasswordAuth(t *testing.T) {
	server, err := NewSocks5Server("alice", "secret", nil)
	if err != nil {
		t.Fatalf("NewSocks5Server returned error: %v", err)
	}

	conn := newStubConn(buildSOCKSRequest(
		socks5.ConnectCommand,
		&socks5.AddrSpec{FQDN: "example.com", Port: 443},
		&socks5.AuthContext{
			Method: socks5.UserPassAuth,
			Payload: map[string]string{
				"Username": "alice",
				"Password": "secret",
			},
		},
	))

	req, err := server.ParseRequest(conn)
	if err != nil {
		t.Fatalf("ParseRequest returned error: %v", err)
	}

	if req.AuthContext == nil || req.AuthContext.Method != socks5.UserPassAuth {
		t.Fatalf("unexpected auth context: %#v", req.AuthContext)
	}
	if req.AuthContext.Payload["Username"] != "alice" || req.AuthContext.Payload["Password"] != "secret" {
		t.Fatalf("unexpected auth payload: %#v", req.AuthContext.Payload)
	}
	if !bytes.Equal(conn.writes.Bytes(), []byte{0x05, 0x02, 0x01, 0x00}) {
		t.Fatalf("unexpected auth reply: %v", conn.writes.Bytes())
	}
}

func TestNewSocks5ServerRejectsInvalidPassword(t *testing.T) {
	server, err := NewSocks5Server("alice", "secret", nil)
	if err != nil {
		t.Fatalf("NewSocks5Server returned error: %v", err)
	}

	conn := newStubConn(buildSOCKSRequest(
		socks5.ConnectCommand,
		&socks5.AddrSpec{FQDN: "example.com", Port: 443},
		&socks5.AuthContext{
			Method: socks5.UserPassAuth,
			Payload: map[string]string{
				"Username": "alice",
				"Password": "wrong",
			},
		},
	))

	_, err = server.ParseRequest(conn)
	if err == nil || !strings.Contains(err.Error(), socks5.UserAuthFailed.Error()) {
		t.Fatalf("expected auth failure, got %v", err)
	}
	if !bytes.Equal(conn.writes.Bytes(), []byte{0x05, 0x02, 0x01, 0x01}) {
		t.Fatalf("unexpected auth failure reply: %v", conn.writes.Bytes())
	}
}

func TestHandleUnsupportedCommandReturnsCommandNotSupported(t *testing.T) {
	server, err := NewSocks5Server("", "", nil)
	if err != nil {
		t.Fatalf("NewSocks5Server returned error: %v", err)
	}

	plug := &Socks5Plugin{Server: server}
	stream := &stubReadWriteCloser{
		reader: bytes.NewReader(buildSOCKSRequest(
			socks5.BindCommand,
			&socks5.AddrSpec{FQDN: "example.com", Port: 80},
			&socks5.AuthContext{Method: socks5.NoAuth},
		)),
	}

	_, err = plug.Handle(stream, &stubConn{})
	if err == nil || !strings.Contains(err.Error(), "Unsupported command") {
		t.Fatalf("expected unsupported command error, got %v", err)
	}
	if !bytes.Contains(stream.writes.Bytes(), []byte{0x05, socks5.CommandNotSupported}) {
		t.Fatalf("missing command-not-supported reply: %v", stream.writes.Bytes())
	}
}

func TestRelayReturnsBridgeWriteError(t *testing.T) {
	server, err := NewSocks5Server("", "", nil)
	if err != nil {
		t.Fatalf("NewSocks5Server returned error: %v", err)
	}

	plug := &Socks5Plugin{Server: server}
	conn := newStubConn(buildSOCKSRequest(
		socks5.ConnectCommand,
		&socks5.AddrSpec{FQDN: "example.com", Port: 80},
		&socks5.AuthContext{Method: socks5.NoAuth},
	))
	bridgeErr := errors.New("bridge write failed")
	bridge := &stubReadWriteCloser{writeErr: bridgeErr}

	_, err = plug.Relay(conn, bridge)
	if !errors.Is(err, bridgeErr) {
		t.Fatalf("expected bridge write error, got %v", err)
	}
}

func TestRelayReturnsReplyReadError(t *testing.T) {
	server, err := NewSocks5Server("", "", nil)
	if err != nil {
		t.Fatalf("NewSocks5Server returned error: %v", err)
	}

	plug := &Socks5Plugin{Server: server}
	conn := newStubConn(buildSOCKSRequest(
		socks5.ConnectCommand,
		&socks5.AddrSpec{FQDN: "example.com", Port: 80},
		&socks5.AuthContext{Method: socks5.NoAuth},
	))
	bridge := &stubReadWriteCloser{
		reader: bytes.NewReader([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0}),
	}

	_, err = plug.Relay(conn, bridge)
	if err == nil || !strings.Contains(err.Error(), "SOCKS5 request failed") {
		t.Fatalf("expected reply read error, got %v", err)
	}
}

func buildSOCKSRequest(cmd uint8, dest *socks5.AddrSpec, auth *socks5.AuthContext) []byte {
	if auth == nil {
		auth = &socks5.AuthContext{Method: socks5.NoAuth}
	}
	req := &socks5.Request{
		Command:     cmd,
		AuthContext: auth,
		DestAddr:    dest,
	}
	return req.BuildRequest()
}

type stubReadWriteCloser struct {
	reader     io.Reader
	writes     bytes.Buffer
	writeErr   error
	closeCount int
}

func (s *stubReadWriteCloser) Read(p []byte) (int, error) {
	if s.reader == nil {
		return 0, io.EOF
	}
	return s.reader.Read(p)
}

func (s *stubReadWriteCloser) Write(p []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.writes.Write(p)
}

func (s *stubReadWriteCloser) Close() error {
	s.closeCount++
	return nil
}

type stubConn struct {
	reader     *bytes.Reader
	writes     bytes.Buffer
	closeCount int
}

func newStubConn(input []byte) *stubConn {
	return &stubConn{reader: bytes.NewReader(input)}
}

func (s *stubConn) Read(p []byte) (int, error) {
	if s.reader == nil {
		return 0, io.EOF
	}
	return s.reader.Read(p)
}

func (s *stubConn) Write(p []byte) (int, error) {
	return s.writes.Write(p)
}

func (s *stubConn) Close() error {
	s.closeCount++
	return nil
}

func (s *stubConn) LocalAddr() net.Addr              { return stubAddr("127.0.0.1:1000") }
func (s *stubConn) RemoteAddr() net.Addr             { return stubAddr("127.0.0.1:2000") }
func (s *stubConn) SetDeadline(time.Time) error      { return nil }
func (s *stubConn) SetReadDeadline(time.Time) error  { return nil }
func (s *stubConn) SetWriteDeadline(time.Time) error { return nil }

type stubAddr string

func (a stubAddr) Network() string { return "tcp" }
func (a stubAddr) String() string  { return string(a) }
