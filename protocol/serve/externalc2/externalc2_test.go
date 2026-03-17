package externalc2

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/internal/servetest"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
)

func TestNewExternalC2InboundRequiresDest(t *testing.T) {
	ensureExternalC2Logger()

	_, err := NewExternalC2Inbound(map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "dest not found") {
		t.Fatalf("expected dest error, got %v", err)
	}
}

func TestNewExternalC2OutboundRequiresDest(t *testing.T) {
	ensureExternalC2Logger()

	_, err := NewExternalC2Outbound(map[string]string{}, &servetest.Dialer{})
	if err == nil || !strings.Contains(err.Error(), "dest not found") {
		t.Fatalf("expected dest error, got %v", err)
	}
}

func TestHandleWritesStagerFrame(t *testing.T) {
	ensureExternalC2Logger()

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	errCh := make(chan error, 1)
	go func() {
		for _, want := range []string{"arch=x64", "pipename=foobar", "block=500", "go"} {
			frame, err := ReadFrame(client)
			if err != nil {
				errCh <- err
				return
			}
			if string(frame.Data) != want {
				errCh <- errors.New("unexpected config frame: " + string(frame.Data))
				return
			}
		}
		errCh <- WriteFrame(client, []byte("stage-1"))
	}()

	outbound, err := NewExternalC2Outbound(map[string]string{"dest": "127.0.0.1:50050"}, &servetest.Dialer{Conn: server})
	if err != nil {
		t.Fatalf("NewExternalC2Outbound returned error: %v", err)
	}

	stream := servetest.NewReadWriteCloser(nil)
	conn, err := outbound.(*ExternalC2Plugin).Handle(stream, servetest.NewConn(nil))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if conn == nil {
		t.Fatal("Handle returned nil conn")
	}

	frame, err := ReadFrame(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatalf("ReadFrame(stream) returned error: %v", err)
	}
	if string(frame.Data) != "stage-1" {
		t.Fatalf("unexpected stager payload: %q", string(frame.Data))
	}

	if err := <-errCh; err != nil {
		t.Fatalf("stager server error: %v", err)
	}
}

func TestRelayPassthrough(t *testing.T) {
	ensureExternalC2Logger()

	inbound, err := NewExternalC2Inbound(map[string]string{"dest": "127.0.0.1:50050"})
	if err != nil {
		t.Fatalf("NewExternalC2Inbound returned error: %v", err)
	}

	conn := servetest.NewConn(nil)
	got, err := inbound.(*ExternalC2Plugin).Relay(conn, servetest.NewReadWriteCloser(nil))
	if err != nil {
		t.Fatalf("Relay returned error: %v", err)
	}
	if got != conn {
		t.Fatal("Relay should return the original conn")
	}
}

func TestOutboundName(t *testing.T) {
	ensureExternalC2Logger()

	outbound, err := NewExternalC2Outbound(map[string]string{"dest": "127.0.0.1:50050"}, &servetest.Dialer{})
	if err != nil {
		t.Fatalf("NewExternalC2Outbound returned error: %v", err)
	}
	if outbound.Name() != core.CobaltStrikeServe {
		t.Fatalf("unexpected outbound name: %q", outbound.Name())
	}
}

func ensureExternalC2Logger() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}
