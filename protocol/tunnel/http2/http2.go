package http2

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/xtls"
	xhttp2 "golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

const (
	http2SecureAlias = "http2s"
)

var errHTTP2ListenerClosed = fmt.Errorf("http2 listener closed")

func init() {
	core.DialerRegister(core.HTTP2Tunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewHTTP2Dialer(ctx), nil
	}, http2SecureAlias)

	core.ListenerRegister(core.HTTP2Tunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewHTTP2Listener(ctx), nil
	}, http2SecureAlias)
}

type http2ClientConn struct {
	localAddr  net.Addr
	remoteAddr net.Addr

	respBody io.ReadCloser
	reqBody  *io.PipeWriter
	cancel   context.CancelFunc

	done      chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex
}

func (c *http2ClientConn) Read(p []byte) (int, error) {
	n, err := c.respBody.Read(p)
	if err == nil {
		return n, nil
	}
	select {
	case <-c.done:
		if err != nil {
			return n, io.EOF
		}
	default:
	}
	return n, err
}

func (c *http2ClientConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	select {
	case <-c.done:
		return 0, io.ErrClosedPipe
	default:
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	select {
	case <-c.done:
		return 0, io.ErrClosedPipe
	default:
	}
	return c.reqBody.Write(p)
}

func (c *http2ClientConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()

		close(c.done)
		c.cancel()
		if closeErr := c.reqBody.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if closeErr := c.respBody.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	})
	return err
}

func (c *http2ClientConn) LocalAddr() net.Addr              { return c.localAddr }
func (c *http2ClientConn) RemoteAddr() net.Addr             { return c.remoteAddr }
func (c *http2ClientConn) SetDeadline(time.Time) error      { return nil }
func (c *http2ClientConn) SetReadDeadline(time.Time) error  { return nil }
func (c *http2ClientConn) SetWriteDeadline(time.Time) error { return nil }

type http2ServerConn struct {
	localAddr  net.Addr
	remoteAddr net.Addr

	body    io.ReadCloser
	writer  http.ResponseWriter
	flusher http.Flusher

	done      chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex
	onClose   func()
}

func (c *http2ServerConn) Read(p []byte) (int, error) {
	n, err := c.body.Read(p)
	if err == nil {
		return n, nil
	}
	select {
	case <-c.done:
		if err != nil {
			return n, io.EOF
		}
	default:
	}
	return n, err
}

func (c *http2ServerConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	select {
	case <-c.done:
		return 0, io.ErrClosedPipe
	default:
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	select {
	case <-c.done:
		return 0, io.ErrClosedPipe
	default:
	}

	n, err := c.writer.Write(p)
	if err != nil {
		return n, err
	}
	c.flusher.Flush()
	return n, nil
}

func (c *http2ServerConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()

		close(c.done)
		if closeErr := c.body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if c.onClose != nil {
			c.onClose()
		}
	})
	return err
}

func (c *http2ServerConn) LocalAddr() net.Addr              { return c.localAddr }
func (c *http2ServerConn) RemoteAddr() net.Addr             { return c.remoteAddr }
func (c *http2ServerConn) SetDeadline(time.Time) error      { return nil }
func (c *http2ServerConn) SetReadDeadline(time.Time) error  { return nil }
func (c *http2ServerConn) SetWriteDeadline(time.Time) error { return nil }

type HTTP2Dialer struct {
	meta core.Metas
}

func NewHTTP2Dialer(ctx context.Context) *HTTP2Dialer {
	return &HTTP2Dialer{meta: core.GetMetas(ctx)}
}

func (d *HTTP2Dialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	normalizeHTTP2URL(u)
	d.meta["url"] = u

	client, err := newHTTP2Client(d.meta, u)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	reqReader, reqWriter := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildHTTP2RequestURL(u), reqReader)
	if err != nil {
		cancel()
		_ = reqReader.Close()
		_ = reqWriter.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := client.Do(req)
	if err != nil {
		cancel()
		_ = reqReader.Close()
		_ = reqWriter.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		cancel()
		_ = reqReader.Close()
		_ = reqWriter.Close()
		return nil, fmt.Errorf("http2 dial failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return &http2ClientConn{
		localAddr:  u,
		remoteAddr: u,
		respBody:   resp.Body,
		reqBody:    reqWriter,
		cancel:     cancel,
		done:       make(chan struct{}),
	}, nil
}

type HTTP2Listener struct {
	meta     core.Metas
	listener net.Listener
	server   *http.Server

	acceptCh  chan net.Conn
	done      chan struct{}
	closeOnce sync.Once

	mu    sync.Mutex
	conns map[*http2ServerConn]struct{}
}

func NewHTTP2Listener(ctx context.Context) *HTTP2Listener {
	return &HTTP2Listener{
		meta:     core.GetMetas(ctx),
		acceptCh: make(chan net.Conn, 32),
		done:     make(chan struct{}),
		conns:    make(map[*http2ServerConn]struct{}),
	}
}

func (l *HTTP2Listener) Listen(dst string) (net.Listener, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	normalizeHTTP2URL(u)
	l.meta["url"] = u

	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc(u.Path, l.handleStream)

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if isSecureHTTP2URL(u) {
		tlsConfig, err := xtls.NewServerTLSConfig("", "", "")
		if err != nil {
			_ = ln.Close()
			return nil, err
		}
		tlsConfig.NextProtos = []string{"h2"}
		server.TLSConfig = tlsConfig
		if err := xhttp2.ConfigureServer(server, &xhttp2.Server{}); err != nil {
			_ = ln.Close()
			return nil, err
		}
		ln = tls.NewListener(ln, tlsConfig)
	} else {
		server.Handler = h2c.NewHandler(mux, &xhttp2.Server{})
	}

	l.listener = ln
	l.server = server

	go func() {
		_ = server.Serve(ln)
	}()

	return l, nil
}

func (l *HTTP2Listener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.acceptCh:
		if conn == nil {
			return nil, errHTTP2ListenerClosed
		}
		return conn, nil
	case <-l.done:
		return nil, errHTTP2ListenerClosed
	}
}

func (l *HTTP2Listener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.done)

		l.mu.Lock()
		conns := make([]*http2ServerConn, 0, len(l.conns))
		for conn := range l.conns {
			conns = append(conns, conn)
		}
		l.conns = map[*http2ServerConn]struct{}{}
		l.mu.Unlock()

		for _, conn := range conns {
			_ = conn.Close()
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

func (l *HTTP2Listener) Addr() net.Addr {
	return l.meta.URL()
}

func (l *HTTP2Listener) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.ProtoMajor != 2 {
		http.Error(w, "http/2 required", http.StatusHTTPVersionNotSupported)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	baseAddr := l.meta.URL()
	var conn *http2ServerConn
	conn = &http2ServerConn{
		localAddr:  baseAddr,
		remoteAddr: http2RemoteAddr{network: baseAddr.Network(), address: r.RemoteAddr},
		body:       r.Body,
		writer:     w,
		flusher:    flusher,
		done:       make(chan struct{}),
		onClose: func() {
			l.removeConn(conn)
		},
	}
	l.addConn(conn)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	select {
	case l.acceptCh <- conn:
	case <-l.done:
		_ = conn.Close()
		return
	case <-r.Context().Done():
		_ = conn.Close()
		return
	}

	select {
	case <-conn.done:
	case <-l.done:
		_ = conn.Close()
	case <-r.Context().Done():
		_ = conn.Close()
	}
}

func (l *HTTP2Listener) addConn(conn *http2ServerConn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.conns[conn] = struct{}{}
}

func (l *HTTP2Listener) removeConn(conn *http2ServerConn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.conns, conn)
}

func newHTTP2Client(meta core.Metas, u *core.URL) (*http.Client, error) {
	dialer := core.GetContextDialer(meta)
	transport := &xhttp2.Transport{
		DisableCompression: true,
	}

	if isSecureHTTP2URL(u) {
		tlsConfig, err := xtls.NewClientTLSConfig("", "", "", u.Hostname())
		if err != nil {
			return nil, err
		}
		tlsConfig.NextProtos = []string{"h2"}
		transport.TLSClientConfig = tlsConfig
		transport.DialTLSContext = func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			if network == "" {
				network = "tcp"
			}
			rawConn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}

			cfg := tlsConfig.Clone()
			conn := tls.Client(rawConn, cfg)
			if err := conn.HandshakeContext(ctx); err != nil {
				_ = rawConn.Close()
				return nil, err
			}
			return conn, nil
		}
	} else {
		transport.AllowHTTP = true
		transport.DialTLSContext = func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			if network == "" {
				network = "tcp"
			}
			return dialer.DialContext(ctx, network, address)
		}
	}

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func buildHTTP2RequestURL(u *core.URL) string {
	scheme := "http"
	if isSecureHTTP2URL(u) {
		scheme = "https"
	}
	return (&neturl.URL{
		Scheme:   scheme,
		Host:     u.Host,
		Path:     u.Path,
		RawPath:  u.RawPath,
		RawQuery: u.RawQuery,
	}).String()
}

func normalizeHTTP2URL(u *core.URL) {
	if u.Path == "" {
		u.Path = "/"
	}
}

func isSecureHTTP2URL(u *core.URL) bool {
	if u == nil {
		return false
	}
	return u.Scheme == http2SecureAlias || u.RawScheme == http2SecureAlias || u.Tunnel == http2SecureAlias
}

type http2RemoteAddr struct {
	network string
	address string
}

func (a http2RemoteAddr) Network() string { return a.network }
func (a http2RemoteAddr) String() string  { return a.address }
