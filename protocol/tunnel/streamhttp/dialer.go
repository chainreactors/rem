//go:build !tinygo

package streamhttp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
)

// ════════════════════════════════════════════════════════════
//  StreamHTTPDialer — client side
// ════════════════════════════════════════════════════════════

type StreamHTTPDialer struct {
	meta core.Metas
}

func NewStreamHTTPDialer(ctx context.Context) *StreamHTTPDialer {
	return &StreamHTTPDialer{meta: core.GetMetas(ctx)}
}

func (d *StreamHTTPDialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	normalizeStreamURL(u)
	d.meta["url"] = u

	client, transport, err := newStreamHTTPClient(d.meta, u)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	reqURL := buildRequestURL(u, utils.RandomString(16))

	conn := &streamHTTPClientConn{
		connBase: connBase{
			localAddr:  u,
			remoteAddr: u,
			readBuf:    utils.NewBuffer(readBufSize),
			done:       make(chan struct{}),
		},
		writeBuf:  utils.NewBuffer(readBufSize),
		sessionID: utils.RandomString(16),
		reqURL:    reqURL,
		client:    client,
		transport: transport,
		ctx:       ctx,
		cancel:    cancel,
		sseReady:  make(chan struct{}),
		postReady: make(chan struct{}),
	}

	// Start background loops.
	go conn.downlinkLoop()
	go conn.postLoop()

	// Wait for both downlink and POST to connect (with timeout).
	timeout := time.After(pendingTTL)

	select {
	case <-conn.sseReady:
	case <-timeout:
		_ = conn.Close()
		return nil, fmt.Errorf("streamhttp downlink connect timeout")
	}

	select {
	case <-conn.postReady:
	case <-timeout:
		_ = conn.Close()
		return nil, fmt.Errorf("streamhttp POST connect timeout")
	}

	return conn, nil
}

// ──────────── downlink goroutine (client background) ────────────

func (c *streamHTTPClientConn) downlinkLoop() {
	backoff := [4]time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
	}
	attempt := 0
	firstConnect := true

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		reqURL := c.reqURL
		req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			c.sleepBackoff(backoff[:], &attempt)
			continue
		}
		req.Header.Set("Accept", "text/event-stream")
		if lastSeq := c.lastSeqID.Load(); lastSeq > 0 {
			req.Header.Set("X-Last-Seq", strconv.FormatUint(lastSeq, 10))
		}

		resp, err := c.client.Do(req)
		if err != nil {
			c.sleepBackoff(backoff[:], &attempt)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			c.sleepBackoff(backoff[:], &attempt)
			continue
		}

		// Connected successfully.
		attempt = 0
		if firstConnect {
			firstConnect = false
			close(c.sseReady)
		}

		// Read binary frames from response body.
		c.readBinaryStream(resp.Body)
		_ = resp.Body.Close()

		// Stream broke — retry.
	}
}

func (c *streamHTTPClientConn) readBinaryStream(r io.Reader) {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		ft, seqID, data, err := readFrame(r)
		if err != nil {
			return
		}

		switch ft {
		case frameTypeData:
			c.lastSeqID.Store(seqID)
			if len(data) > 0 {
				_, _ = c.readBuf.Write(data)
			}
		case frameTypePing:
			// Ignore.
		case frameTypeClose:
			_ = c.Close()
			return
		}
	}
}

// ──────────── POST goroutine (client background) ────────────

func (c *streamHTTPClientConn) postLoop() {
	backoff := [4]time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
	}
	attempt := 0
	firstConnect := true

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		pr, pw := io.Pipe()

		// Launch the HTTP POST in a goroutine.
		respCh := make(chan error, 1)
		go func() {
			req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.reqURL, pr)
			if err != nil {
				_ = pr.Close()
				respCh <- err
				return
			}
			req.Header.Set("Content-Type", "application/octet-stream")
			resp, err := c.client.Do(req)
			if err != nil {
				respCh <- err
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				respCh <- fmt.Errorf("POST returned %s", resp.Status)
				return
			}
			respCh <- nil
		}()

		if firstConnect {
			// Wait briefly for the POST to be accepted.
			select {
			case err := <-respCh:
				if err != nil {
					_ = pw.Close()
					c.sleepBackoff(backoff[:], &attempt)
					continue
				}
				// This shouldn't happen on first connect (body not sent yet).
			case <-time.After(200 * time.Millisecond):
				// POST is in flight — good.
			case <-c.ctx.Done():
				_ = pw.Close()
				return
			}
			firstConnect = false
			attempt = 0
			close(c.postReady)
		}

		// Pump data from writeBuf into the POST body.
		tmp := make([]byte, 32*1024)
		writeErr := false
		for {
			n, err := c.writeBuf.ReadAtLeast(tmp)
			if err != nil {
				// Buffer closed = conn closing.
				_ = pw.Close()
				return
			}
			if n > 0 {
				if _, werr := pw.Write(tmp[:n]); werr != nil {
					writeErr = true
					break
				}
			}
		}

		_ = pw.Close()

		if writeErr {
			// POST broke — retry.
			c.sleepBackoff(backoff[:], &attempt)
		}
	}
}

func (c *streamHTTPClientConn) sleepBackoff(backoff []time.Duration, attempt *int) {
	idx := *attempt
	if idx >= len(backoff) {
		idx = len(backoff) - 1
	}
	select {
	case <-time.After(backoff[idx]):
	case <-c.ctx.Done():
	}
	*attempt++
}
