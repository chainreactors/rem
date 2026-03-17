package websocket

import (
	"context"
	"crypto/tls"
	"fmt"
	"golang.org/x/net/websocket"
	"net"
	"net/http"

	"github.com/chainreactors/rem/protocol/core"
)

func init() {
	core.ListenerRegister(core.WebSocketTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewWebsocketListener(ctx), nil
	}, "websocket", "wss")
	core.DialerRegister(core.WebSocketTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewWebsocketDialer(ctx), nil
	}, "websocket", "wss")
}

type WebsocketDialer struct {
	net.Conn
	meta core.Metas
}

func NewWebsocketDialer(ctx context.Context) *WebsocketDialer {
	return &WebsocketDialer{
		meta: core.GetMetas(ctx),
	}
}

func (c *WebsocketDialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	c.meta["url"] = u

	scheme := "ws"
	if v, ok := c.meta["tls"]; ok && v.(bool) {
		scheme = "wss"
	}

	conf, err := websocket.NewConfig(fmt.Sprintf("%s://%s/%s", scheme, u.Host, u.PathString()), "http://127.0.0.1")
	if err != nil {
		return nil, err
	}

	if tlsconf := c.meta["tlsConfig"]; tlsconf != nil {
		conf.TlsConfig = tlsconf.(*tls.Config)
	}

	rawConn, err := core.GetContextDialer(c.meta).DialContext(context.Background(), "tcp", u.Host)
	if err != nil {
		return nil, err
	}

	if scheme == "wss" {
		tlsConn := tls.Client(rawConn, conf.TlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			rawConn.Close()
			return nil, err
		}
		rawConn = tlsConn
	}

	conn, err := websocket.NewClient(conf, rawConn)
	if err != nil {
		rawConn.Close()
		return nil, err
	}
	conn.PayloadType = websocket.BinaryFrame
	return conn, nil
}

type WebsocketListener struct {
	acceptCh chan *websocket.Conn
	server   *http.Server
	listener net.Listener
	meta     core.Metas
}

func NewWebsocketListener(ctx context.Context) *WebsocketListener {
	return &WebsocketListener{
		meta: core.GetMetas(ctx),
	}
}

func (c *WebsocketListener) Listen(dst string) (net.Listener, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	c.meta["url"] = u
	if u.PathString() == "" {
		u.Path = "/" + c.meta.GetString("path")
	}
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, err
	}
	c.listener = ln

	c.acceptCh = make(chan *websocket.Conn)
	muxer := http.NewServeMux()
	muxer.Handle(u.Path, websocket.Handler(func(conn *websocket.Conn) {
		conn.PayloadType = websocket.BinaryFrame
		notifyCh := make(chan struct{})
		c.acceptCh <- conn
		<-notifyCh
	}))

	c.server = &http.Server{
		Handler: muxer,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	go c.server.Serve(ln)

	return c, nil
}

func (c *WebsocketListener) Accept() (net.Conn, error) {
	conn, ok := <-c.acceptCh
	if !ok {
		return nil, fmt.Errorf("websocket error")
	}
	return conn, nil
}

func (c *WebsocketListener) Close() error {
	if c.server != nil {
		return c.server.Close()
	}
	return nil
}

func (c *WebsocketListener) Addr() net.Addr {
	return c.meta.URL()
}
