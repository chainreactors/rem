//go:build !tinygo

package streamhttp

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
)

// Export replayBuffer for testing.

// ReplayEventExported wraps replayEvent for test access.
type ReplayEventExported struct {
	ID   uint64
	Data []byte
}

// ReplayBufferExported wraps replayBuffer for test access.
type ReplayBufferExported struct {
	rb *replayBuffer
}

func NewReplayBufferForTest(capacity int) *ReplayBufferExported {
	return &ReplayBufferExported{rb: newReplayBuffer(capacity)}
}

func (e *ReplayBufferExported) Append(data []byte) uint64 {
	return e.rb.Append(data)
}

// ReadFrom blocks until events with id > afterID are available.
// Kept for test use only — production code uses Collect + Notify.
func (e *ReplayBufferExported) ReadFrom(afterID uint64) ([]ReplayEventExported, error) {
	for {
		events, err := e.rb.Collect(afterID)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			out := make([]ReplayEventExported, len(events))
			for i, ev := range events {
				out[i] = ReplayEventExported{ID: ev.id, Data: ev.data}
			}
			return out, nil
		}
		// Wait for notification.
		if _, ok := <-e.rb.Notify(); !ok {
			// Channel closed — buffer is closed.
			return nil, io.ErrClosedPipe
		}
	}
}

func (e *ReplayBufferExported) Close() {
	e.rb.Close()
}

// DisableHTTP2Listener patches the listener's HTTP server to disable HTTP/2.
// Must be called after Listen() and before any connections arrive.
func DisableHTTP2Listener(l *StreamHTTPListener) {
	if l.server != nil {
		l.server.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	}
}

// PatchTransportDisableHTTP2 patches an http.Transport to disable HTTP/2 negotiation.
func PatchTransportDisableHTTP2(t *http.Transport) {
	t.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
	t.ForceAttemptHTTP2 = false
}

// ListenerHasTLS reports whether the StreamHTTPListener has TLS configured.
func ListenerHasTLS(l *StreamHTTPListener) bool {
	return l != nil && l.tlsEnabled
}

// ListenerAddr returns the underlying net.Listener address for low-level tests.
func ListenerAddr(l *StreamHTTPListener) net.Addr {
	if l.listener != nil {
		return l.listener.Addr()
	}
	return nil
}
