//go:build !tinygo

package streamhttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"time"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/xtls"
)

// ──────────────────────── constants ────────────────────────

const (
	streamSessionQuery = "_sid"

	pendingTTL     = 10 * time.Second
	reconnectTTL   = 30 * time.Second
	pingInterval   = 15 * time.Second
	replayCapacity = 256

	// maxFramePayload is the maximum raw data bytes per frame.
	// With binary framing there is no encoding overhead, so we use 72KB directly.
	maxFramePayload = 72 * 1024

	readBufSize = 256 * 1024 // utils.Buffer capacity
)

var errStreamListenerClosed = fmt.Errorf("streamhttp listener closed")

// ──────────────────────── init ────────────────────────

func init() {
	core.DialerRegister(core.StreamHTTPTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewStreamHTTPDialer(ctx), nil
	}, core.StreamHTTPSTunnel)
	core.ListenerRegister(core.StreamHTTPTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewStreamHTTPListener(ctx), nil
	}, core.StreamHTTPSTunnel)
}

// ──────────────────────── HTTP client factory ────────────────────────

func newStreamHTTPClient(meta core.Metas, u *core.URL) (*http.Client, *http.Transport, error) {
	dialer := core.GetContextDialer(meta)
	transport := &http.Transport{
		DisableCompression: true,
		ForceAttemptHTTP2:  false,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network == "" {
				network = "tcp"
			}
			return dialer.DialContext(ctx, network, address)
		},
	}
	if _, ok := meta[core.ContextDialerMetaKey]; !ok {
		transport.Proxy = http.ProxyFromEnvironment
	}

	if isSecureStreamURL(u) {
		tlsConfig, err := xtls.NewClientTLSConfig("", "", "", u.Hostname())
		if err != nil {
			return nil, nil, err
		}
		transport.TLSClientConfig = tlsConfig
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client, transport, nil
}

// ──────────────────────── URL helpers ────────────────────────

func buildRequestURL(u *core.URL, sessionID string) string {
	query := u.Query()
	query.Set(streamSessionQuery, sessionID)

	scheme := "http"
	if isSecureStreamURL(u) {
		scheme = "https"
	}

	return (&neturl.URL{
		Scheme:   scheme,
		Host:     u.Host,
		Path:     u.Path,
		RawPath:  u.RawPath,
		RawQuery: query.Encode(),
	}).String()
}

func normalizeStreamURL(u *core.URL) {
	if u.Path == "" {
		u.Path = "/"
	}
}

func isSecureStreamURL(u *core.URL) bool {
	if u == nil {
		return false
	}
	return u.Scheme == core.StreamHTTPSTunnel || u.RawScheme == core.StreamHTTPSTunnel || u.Tunnel == core.StreamHTTPSTunnel
}

// ──────────────────────── streamRemoteAddr ────────────────────────

type streamRemoteAddr struct {
	network string
	address string
}

func (a streamRemoteAddr) Network() string { return a.network }
func (a streamRemoteAddr) String() string  { return a.address }
