package trojan

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/internal/servetest"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}

func TestRelayBuildsDomainRelayRequest(t *testing.T) {
	plugI, err := NewTrojanInbound(map[string]string{"password": "testpass"})
	if err != nil {
		t.Fatalf("NewTrojanInbound returned error: %v", err)
	}

	plug := plugI.(*TrojanPlugin)
	serverConn, done := newTrojanClientConn(t, "testpass", "example.com:443")
	defer serverConn.Close()

	bridge := servetest.NewReadWriteCloser(servetest.SOCKS5SuccessReply())
	conn, err := plug.Relay(serverConn, bridge)
	if err != nil {
		t.Fatalf("Relay returned error: %v", err)
	}
	if conn == nil {
		t.Fatal("Relay returned nil conn")
	}
	_ = conn.Close()

	want, err := socks5.NewRelay("example.com:443")
	if err != nil {
		t.Fatalf("NewRelay returned error: %v", err)
	}
	if string(bridge.Bytes()) != string(want.BuildRelay()) {
		t.Fatalf("unexpected relay payload: %v", bridge.Bytes())
	}

	if writeErr := <-done; writeErr != nil {
		t.Fatalf("failed to write trojan request: %v", writeErr)
	}
}

func TestRelayRejectsInvalidPassword(t *testing.T) {
	plugI, err := NewTrojanInbound(map[string]string{"password": "testpass"})
	if err != nil {
		t.Fatalf("NewTrojanInbound returned error: %v", err)
	}

	plug := plugI.(*TrojanPlugin)
	serverConn, done := newTrojanClientConn(t, "wrongpass", "example.com:443")
	defer serverConn.Close()

	_, err = plug.Relay(serverConn, servetest.NewReadWriteCloser(nil))
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("expected authentication failure, got %v", err)
	}

	if writeErr := <-done; writeErr != nil {
		t.Fatalf("failed to write trojan request: %v", writeErr)
	}
}

func TestRelayReturnsBridgeWriteError(t *testing.T) {
	plugI, err := NewTrojanInbound(map[string]string{"password": "testpass"})
	if err != nil {
		t.Fatalf("NewTrojanInbound returned error: %v", err)
	}

	plug := plugI.(*TrojanPlugin)
	serverConn, done := newTrojanClientConn(t, "testpass", "example.com:443")
	defer serverConn.Close()

	bridgeErr := errors.New("bridge write failed")
	bridge := servetest.NewReadWriteCloser(nil)
	bridge.WriteErr = bridgeErr
	_, err = plug.Relay(serverConn, bridge)
	if !errors.Is(err, bridgeErr) {
		t.Fatalf("expected bridge write error, got %v", err)
	}

	if writeErr := <-done; writeErr != nil {
		t.Fatalf("failed to write trojan request: %v", writeErr)
	}
}

func TestRelayReturnsReplyReadError(t *testing.T) {
	plugI, err := NewTrojanInbound(map[string]string{"password": "testpass"})
	if err != nil {
		t.Fatalf("NewTrojanInbound returned error: %v", err)
	}

	plug := plugI.(*TrojanPlugin)
	serverConn, done := newTrojanClientConn(t, "testpass", "example.com:443")
	defer serverConn.Close()

	bridge := servetest.NewReadWriteCloser(servetest.SOCKS5Reply(0x05))
	_, err = plug.Relay(serverConn, bridge)
	if err == nil || !strings.Contains(err.Error(), "SOCKS5 request failed") {
		t.Fatalf("expected reply read error, got %v", err)
	}

	if writeErr := <-done; writeErr != nil {
		t.Fatalf("failed to write trojan request: %v", writeErr)
	}
}

func newTrojanClientConn(t *testing.T, password, targetAddr string) (net.Conn, <-chan error) {
	t.Helper()

	client, server := net.Pipe()
	deadline := time.Now().Add(15 * time.Second)
	_ = client.SetDeadline(deadline)
	_ = server.SetDeadline(deadline)
	t.Cleanup(func() {
		_ = client.Close()
	})

	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)

		tlsConn := tls.Client(client, &tls.Config{InsecureSkipVerify: true})
		if err := tlsConn.Handshake(); err != nil {
			errCh <- err
			return
		}

		header, err := buildTrojanHeader(password, targetAddr)
		if err != nil {
			errCh <- err
			return
		}

		_, err = tlsConn.Write(header)
		errCh <- err
	}()

	return server, errCh
}

func buildTrojanHeader(password, targetAddr string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(targetAddr)
	if err != nil {
		return nil, err
	}

	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		return nil, err
	}

	header := make([]byte, 0, 128)
	header = append(header, []byte(sha224Hex(password))...)
	header = append(header, 0x0d, 0x0a, 0x01)

	ip := net.ParseIP(host)
	switch {
	case ip != nil && ip.To4() != nil:
		header = append(header, 0x01)
		header = append(header, ip.To4()...)
	case ip != nil:
		header = append(header, 0x04)
		header = append(header, ip.To16()...)
	default:
		header = append(header, 0x03, byte(len(host)))
		header = append(header, host...)
	}

	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, uint16(port))
	header = append(header, portBuf...)
	header = append(header, 0x0d, 0x0a)
	return header, nil
}

func sha224Hex(s string) string {
	h := sha256.New224()
	_, _ = h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
