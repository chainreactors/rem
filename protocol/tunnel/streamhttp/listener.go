//go:build !tinygo

package streamhttp

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/xtls"
)

// ════════════════════════════════════════════════════════════
//  streamHTTPSession — server-side session with state machine
// ════════════════════════════════════════════════════════════

const (
	sessionPending      = 0
	sessionActive       = 1
	sessionReconnecting = 2
	sessionClosed       = 3
)

type streamHTTPSession struct {
	conn *streamHTTPConn

	mu       sync.Mutex
	state    int
	lastSeen time.Time

	// Downlink writer management: only one downlink goroutine active at a time.
	downlinkCancel context.CancelFunc
	downlinkReady  chan struct{} // signaled when a new GET arrives

	hasGet  bool
	hasPost bool
}

func newSession(conn *streamHTTPConn) *streamHTTPSession {
	return &streamHTTPSession{
		conn:          conn,
		state:         sessionPending,
		lastSeen:      time.Now(),
		downlinkReady: make(chan struct{}, 1),
	}
}

func (s *streamHTTPSession) touch() {
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

// ════════════════════════════════════════════════════════════
//  StreamHTTPListener — server side
// ════════════════════════════════════════════════════════════

type StreamHTTPListener struct {
	meta       core.Metas
	listener   net.Listener
	server     *http.Server
	tlsEnabled bool

	acceptCh  chan net.Conn
	done      chan struct{}
	closeOnce sync.Once

	mu       sync.Mutex
	sessions map[string]*streamHTTPSession
}

func NewStreamHTTPListener(ctx context.Context) *StreamHTTPListener {
	return &StreamHTTPListener{
		meta:     core.GetMetas(ctx),
		acceptCh: make(chan net.Conn, 32),
		done:     make(chan struct{}),
		sessions: make(map[string]*streamHTTPSession),
	}
}

func (l *StreamHTTPListener) Listen(dst string) (net.Listener, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	normalizeStreamURL(u)
	l.meta["url"] = u

	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc(u.Path, l.handleStream)

	l.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	l.tlsEnabled = false
	if isSecureStreamURL(u) {
		tlsConfig, err := xtls.NewServerTLSConfig("", "", "")
		if err != nil {
			_ = ln.Close()
			return nil, err
		}
		l.server.TLSConfig = tlsConfig
		l.tlsEnabled = true
		ln = tls.NewListener(ln, tlsConfig)
	}

	l.listener = ln
	go l.reapSessions()
	go func() {
		_ = l.server.Serve(ln)
	}()

	return l, nil
}

func (l *StreamHTTPListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.acceptCh:
		if conn == nil {
			return nil, errStreamListenerClosed
		}
		return conn, nil
	case <-l.done:
		return nil, errStreamListenerClosed
	}
}

func (l *StreamHTTPListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.done)

		l.mu.Lock()
		sessions := make([]*streamHTTPSession, 0, len(l.sessions))
		for _, sess := range l.sessions {
			sessions = append(sessions, sess)
		}
		l.sessions = map[string]*streamHTTPSession{}
		l.mu.Unlock()

		for _, sess := range sessions {
			if sess != nil && sess.conn != nil {
				_ = sess.conn.Close()
			}
		}

		if l.server != nil {
			err = l.server.Close()
			return
		}
		if l.listener != nil {
			err = l.listener.Close()
		}
	})
	return err
}

func (l *StreamHTTPListener) Addr() net.Addr {
	return l.meta.URL()
}

// ──────────── HTTP handler dispatch ────────────

func (l *StreamHTTPListener) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get(streamSessionQuery)
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		l.handleDownlink(w, r, sessionID)
	case http.MethodPost:
		l.handleUpload(w, r, sessionID)
	}
}

// ──────────── GET → binary downlink ────────────

func (l *StreamHTTPListener) handleDownlink(w http.ResponseWriter, r *http.Request, sessionID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sess, isNew, err := l.getOrCreateSession(sessionID, r.RemoteAddr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Cancel any previous downlink handler for this session.
	sess.mu.Lock()
	if sess.downlinkCancel != nil {
		sess.downlinkCancel()
	}
	dlCtx, dlCancel := context.WithCancel(r.Context())
	sess.downlinkCancel = dlCancel
	sess.hasGet = true
	if !isNew && sess.state == sessionReconnecting {
		sess.state = sessionActive
	}
	sess.mu.Unlock()
	sess.touch()

	// Check if session is now ready (has both GET and POST).
	l.tryActivate(sessionID, sess)

	// Set streaming headers. text/event-stream prevents intermediate proxy caching.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Determine replay cursor from X-Last-Seq header.
	var cursor uint64
	if lastSeq := r.Header.Get("X-Last-Seq"); lastSeq != "" {
		if v, err := strconv.ParseUint(lastSeq, 10, 64); err == nil {
			cursor = v
		}
	}

	// Notify anyone waiting for downlink to be ready.
	select {
	case sess.downlinkReady <- struct{}{}:
	default:
	}

	// Binary write loop.
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	replayBuf := sess.conn.replayBuf

	for {
		// First, check for any pending events (non-blocking).
		events, err := replayBuf.Collect(cursor)
		if err != nil {
			_ = writeCloseFrame(w)
			flusher.Flush()
			return
		}

		for _, ev := range events {
			if werr := writeDataFrame(w, ev.id, ev.data); werr != nil {
				l.markReconnecting(sessionID, sess)
				return
			}
			cursor = ev.id
		}
		if len(events) > 0 {
			flusher.Flush()
			sess.touch()
		}

		// Block until something happens.
		select {
		case <-dlCtx.Done():
			return
		case <-sess.conn.done:
			// Flush remaining events before close.
			if remaining, _ := replayBuf.Collect(cursor); len(remaining) > 0 {
				for _, ev := range remaining {
					_ = writeDataFrame(w, ev.id, ev.data)
				}
			}
			_ = writeCloseFrame(w)
			flusher.Flush()
			return
		case <-pingTicker.C:
			if perr := writePingFrame(w); perr != nil {
				l.markReconnecting(sessionID, sess)
				return
			}
			flusher.Flush()
		case <-replayBuf.Notify():
			// New events available — loop back to Collect.
		}
	}
}

// ──────────── POST → upload uplink ────────────

func (l *StreamHTTPListener) handleUpload(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, isNew, err := l.getOrCreateSession(sessionID, r.RemoteAddr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	sess.mu.Lock()
	sess.hasPost = true
	if !isNew && sess.state == sessionReconnecting {
		sess.state = sessionActive
	}
	sess.mu.Unlock()
	sess.touch()

	l.tryActivate(sessionID, sess)

	// Read POST body into session's readBuf.
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			// utils.Buffer.Write() copies internally, no need for extra allocation.
			if _, werr := sess.conn.readBuf.Write(buf[:n]); werr != nil {
				return
			}
			sess.touch()
		}
		if err != nil {
			break
		}
	}

	// POST ended — mark reconnecting (client will re-POST).
	l.markReconnecting(sessionID, sess)
}

// ──────────── session management helpers ────────────

func (l *StreamHTTPListener) getOrCreateSession(sessionID, remoteAddr string) (*streamHTTPSession, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	select {
	case <-l.done:
		return nil, false, errStreamListenerClosed
	default:
	}

	if sess, ok := l.sessions[sessionID]; ok {
		return sess, false, nil
	}

	baseAddr := l.meta.URL()
	conn := newServerConn(
		baseAddr,
		streamRemoteAddr{network: baseAddr.Network(), address: remoteAddr},
		func() { l.removeSession(sessionID) },
	)
	sess := newSession(conn)
	l.sessions[sessionID] = sess
	return sess, true, nil
}

func (l *StreamHTTPListener) tryActivate(sessionID string, sess *streamHTTPSession) {
	sess.mu.Lock()
	if sess.state != sessionPending || !sess.hasGet || !sess.hasPost {
		sess.mu.Unlock()
		return
	}
	sess.state = sessionActive
	sess.mu.Unlock()

	select {
	case l.acceptCh <- sess.conn:
	case <-l.done:
		_ = sess.conn.Close()
	}
}

func (l *StreamHTTPListener) markReconnecting(sessionID string, sess *streamHTTPSession) {
	sess.mu.Lock()
	if sess.state == sessionActive {
		sess.state = sessionReconnecting
		sess.lastSeen = time.Now()
	}
	sess.mu.Unlock()
}

func (l *StreamHTTPListener) removeSession(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sessions, sessionID)
}

func (l *StreamHTTPListener) reapSessions() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			now := time.Now()
			var expired []*streamHTTPConn

			l.mu.Lock()
			for id, sess := range l.sessions {
				if sess == nil {
					delete(l.sessions, id)
					continue
				}
				sess.mu.Lock()
				switch sess.state {
				case sessionPending:
					if now.Sub(sess.lastSeen) > pendingTTL {
						expired = append(expired, sess.conn)
						delete(l.sessions, id)
					}
				case sessionReconnecting:
					if now.Sub(sess.lastSeen) > reconnectTTL {
						expired = append(expired, sess.conn)
						delete(l.sessions, id)
					}
				}
				sess.mu.Unlock()
			}
			l.mu.Unlock()

			for _, conn := range expired {
				_ = conn.Close()
			}

		case <-l.done:
			return
		}
	}
}
