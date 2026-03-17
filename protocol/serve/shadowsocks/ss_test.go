package shadowsocks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
	ss "github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
)

func init() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}

func TestReadAddrParsesSupportedTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  []byte
		wantIP   net.IP
		wantDNS  string
		wantPort int
	}{
		{
			name:     "domain",
			payload:  append([]byte{AtypDomainName, byte(len("example.com"))}, append([]byte("example.com"), 0x01, 0xbb)...),
			wantDNS:  "example.com",
			wantPort: 443,
		},
		{
			name:     "ipv4",
			payload:  []byte{AtypIPv4, 127, 0, 0, 1, 0x1f, 0x90},
			wantIP:   net.ParseIP("127.0.0.1"),
			wantPort: 8080,
		},
		{
			name:     "ipv6",
			payload:  append(append([]byte{AtypIPv6}, net.ParseIP("2001:db8::5").To16()...), 0x23, 0x82),
			wantIP:   net.ParseIP("2001:db8::5"),
			wantPort: 9090,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			addr, err := ReadAddr(bytes.NewReader(tt.payload), make([]byte, MaxAddrLen))
			if err != nil {
				t.Fatalf("ReadAddr returned error: %v", err)
			}
			if tt.wantDNS != "" && addr.FQDN != tt.wantDNS {
				t.Fatalf("FQDN mismatch: want %q, got %q", tt.wantDNS, addr.FQDN)
			}
			if tt.wantIP != nil && !addr.IP.Equal(tt.wantIP) {
				t.Fatalf("IP mismatch: want %v, got %v", tt.wantIP, addr.IP)
			}
			if addr.Port != tt.wantPort {
				t.Fatalf("port mismatch: want %d, got %d", tt.wantPort, addr.Port)
			}
		})
	}
}

func TestReadAddrRejectsShortBuffer(t *testing.T) {
	_, err := ReadAddr(bytes.NewReader([]byte{AtypIPv4, 127, 0, 0, 1, 0x00, 0x50}), make([]byte, MaxAddrLen-1))
	if !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("expected io.ErrShortBuffer, got %v", err)
	}
}

func TestReadAddrRejectsUnsupportedAddressType(t *testing.T) {
	_, err := ReadAddr(bytes.NewReader([]byte{0xff}), make([]byte, MaxAddrLen))
	if err == nil || !strings.Contains(err.Error(), "unsupported address type") {
		t.Fatalf("expected unsupported address type error, got %v", err)
	}
}

func TestHandleUsesConfiguredDialer(t *testing.T) {
	password := "pw-" + t.Name()
	cipherName := "dummy"

	plugI, err := NewShadowSocksOutbound(
		map[string]string{"password": password, "cipher": cipherName},
		core.NewProxyDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				t.Fatalf("unexpected network: %s", network)
			}
			if address != "dialer-only.invalid:443" {
				t.Fatalf("unexpected dial target: %s", address)
			}
			peer, remote := net.Pipe()
			t.Cleanup(func() {
				_ = peer.Close()
				_ = remote.Close()
			})
			return peer, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewShadowSocksOutbound returned error: %v", err)
	}

	plug := plugI.(*ShadowSocksPlugin)
	serverConn, done := newEncryptedShadowSocksConn(t, password, cipherName, socks.ParseAddr("dialer-only.invalid:443"))
	defer serverConn.Close()

	conn, err := plug.Handle(serverConn, &shadowDummyConn{})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	_ = conn.Close()

	if writeErr := <-done; writeErr != nil {
		t.Fatalf("failed to write encrypted target: %v", writeErr)
	}
}

func TestRelayRejectsUnsupportedAddressType(t *testing.T) {
	password := "pw-" + t.Name()
	cipherName := "dummy"

	plugI, err := NewShadowSocksInbound(map[string]string{"password": password, "cipher": cipherName})
	if err != nil {
		t.Fatalf("NewShadowSocksInbound returned error: %v", err)
	}

	plug := plugI.(*ShadowSocksPlugin)
	serverConn, done := newEncryptedShadowSocksConn(t, password, cipherName, []byte{0xff})
	defer serverConn.Close()

	_, err = plug.Relay(serverConn, &shadowBridge{})
	if err == nil || !strings.Contains(err.Error(), "unsupported address type") {
		t.Fatalf("expected unsupported address type error, got %v", err)
	}

	if writeErr := <-done; writeErr != nil {
		t.Fatalf("failed to write encrypted payload: %v", writeErr)
	}
}

func TestRelayReturnsBridgeWriteError(t *testing.T) {
	password := "pw-" + t.Name()
	cipherName := "dummy"

	plugI, err := NewShadowSocksInbound(map[string]string{"password": password, "cipher": cipherName})
	if err != nil {
		t.Fatalf("NewShadowSocksInbound returned error: %v", err)
	}

	plug := plugI.(*ShadowSocksPlugin)
	serverConn, done := newEncryptedShadowSocksConn(t, password, cipherName, socks.ParseAddr("example.com:443"))
	defer serverConn.Close()

	bridgeErr := errors.New("bridge write failed")
	_, err = plug.Relay(serverConn, &shadowBridge{writeErr: bridgeErr})
	if !errors.Is(err, bridgeErr) {
		t.Fatalf("expected bridge write error, got %v", err)
	}

	if writeErr := <-done; writeErr != nil {
		t.Fatalf("failed to write encrypted target: %v", writeErr)
	}
}

func TestRelayReturnsReplyReadError(t *testing.T) {
	password := "pw-" + t.Name()
	cipherName := "dummy"

	plugI, err := NewShadowSocksInbound(map[string]string{"password": password, "cipher": cipherName})
	if err != nil {
		t.Fatalf("NewShadowSocksInbound returned error: %v", err)
	}

	plug := plugI.(*ShadowSocksPlugin)
	serverConn, done := newEncryptedShadowSocksConn(t, password, cipherName, socks.ParseAddr("example.com:443"))
	defer serverConn.Close()

	_, err = plug.Relay(serverConn, &shadowBridge{
		reader: bytes.NewReader([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0}),
	})
	if err == nil || !strings.Contains(err.Error(), "SOCKS5 request failed") {
		t.Fatalf("expected reply read error, got %v", err)
	}

	if writeErr := <-done; writeErr != nil {
		t.Fatalf("failed to write encrypted target: %v", writeErr)
	}
}

func newEncryptedShadowSocksConn(t *testing.T, password, cipherName string, payload []byte) (net.Conn, <-chan error) {
	t.Helper()

	client, server := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	_ = client.SetDeadline(deadline)
	_ = server.SetDeadline(deadline)

	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		defer client.Close()

		clientCipher, err := ss.PickCipher(cipherName, nil, password)
		if err != nil {
			errCh <- err
			return
		}

		enc := clientCipher.StreamConn(client)
		_, err = enc.Write(payload)
		errCh <- err
	}()

	return server, errCh
}

type shadowBridge struct {
	reader   io.Reader
	writes   bytes.Buffer
	writeErr error
}

func (s *shadowBridge) Read(p []byte) (int, error) {
	if s.reader == nil {
		return 0, io.EOF
	}
	return s.reader.Read(p)
}

func (s *shadowBridge) Write(p []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.writes.Write(p)
}

func (s *shadowBridge) Close() error { return nil }

type shadowDummyConn struct{}

func (shadowDummyConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (shadowDummyConn) Write(p []byte) (int, error)      { return len(p), nil }
func (shadowDummyConn) Close() error                     { return nil }
func (shadowDummyConn) LocalAddr() net.Addr              { return shadowAddr("127.0.0.1:1000") }
func (shadowDummyConn) RemoteAddr() net.Addr             { return shadowAddr("127.0.0.1:2000") }
func (shadowDummyConn) SetDeadline(time.Time) error      { return nil }
func (shadowDummyConn) SetReadDeadline(time.Time) error  { return nil }
func (shadowDummyConn) SetWriteDeadline(time.Time) error { return nil }

type shadowAddr string

func (a shadowAddr) Network() string { return "tcp" }
func (a shadowAddr) String() string  { return string(a) }
