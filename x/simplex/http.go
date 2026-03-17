package simplex

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/x/xtls"
)

func init() {
	RegisterSimplex("http", func(addr *SimplexAddr) (Simplex, error) {
		return NewHTTPClient(addr)
	}, func(network, address string) (Simplex, error) {
		return NewHTTPServer(network, address)
	}, func(network, address string) (*SimplexAddr, error) {
		return ResolveHTTPAddr(network, address)
	})

	RegisterSimplex("https", func(addr *SimplexAddr) (Simplex, error) {
		return NewHTTPClient(addr)
	}, func(network, address string) (Simplex, error) {
		return NewHTTPServer(network, address)
	}, func(network, address string) (*SimplexAddr, error) {
		return ResolveHTTPAddr(network, address)
	})
}

const (
	DefaultHTTPInterval = 100              // 默认间隔(毫秒)
	DefaultHTTPMaxBody  = 128 * 1024       // 128k
	DefaultHTTPTimeout  = 30 * time.Second // 30秒超时
)

// HTTP 配置结构
type HTTPConfig struct {
	MaxBodySize  int               // 最大消息体大小
	Interval     time.Duration     // 发包间隔
	Timeout      time.Duration     // 请求超时
	Path         string            // 请求路径
	TLSConfig    *tls.Config       // TLS配置
	UserAgent    string            // User-Agent
	ContentType  string            // Content-Type
	Host         string            // Host header
	ExtraHeaders map[string]string // 额外的HTTP头
}

// DefaultHTTPConfig 返回默认的HTTP配置
func DefaultHTTPConfig() *HTTPConfig {
	return &HTTPConfig{
		MaxBodySize:  DefaultHTTPMaxBody,
		Interval:     DefaultHTTPInterval * time.Millisecond,
		Timeout:      DefaultHTTPTimeout,
		Path:         "/" + randomString(8),
		UserAgent:    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		ContentType:  "application/json",
		ExtraHeaders: make(map[string]string),
	}
}

func ResolveHTTPAddr(network, address string) (*SimplexAddr, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}

	config := DefaultHTTPConfig()

	// 解析间隔
	if iv := u.Query().Get("interval"); iv != "" {
		interval, err := strconv.Atoi(iv)
		if err == nil {
			config.Interval = time.Duration(interval) * time.Millisecond
		}
	}

	// 解析最大消息大小
	if max := u.Query().Get("max"); max != "" {
		maxSize, err := strconv.Atoi(max)
		if err == nil {
			config.MaxBodySize = maxSize
		}
	}

	// 解析超时
	if timeout := u.Query().Get("timeout"); timeout != "" {
		timeoutSec, err := strconv.Atoi(timeout)
		if err == nil {
			config.Timeout = time.Duration(timeoutSec) * time.Second
		}
	}

	// 解析路径
	if u.Path == "/" || u.Path == "" {
		config.Path = "/" + randomString(8)
	} else {
		config.Path = u.Path
	}

	// 解析User-Agent
	if ua := u.Query().Get("ua"); ua != "" {
		config.UserAgent = ua
	}

	// 解析Content-Type
	if ct := u.Query().Get("content_type"); ct != "" {
		config.ContentType = ct
	}

	// 解析Host
	if host := u.Query().Get("host"); host != "" {
		config.Host = host
	}

	// 如果是HTTPS，设置TLS配置
	if u.Scheme == "https" {
		cert := xtls.NewRandomTLSKeyPair()
		config.TLSConfig = &tls.Config{
			Certificates:       []tls.Certificate{*cert},
			InsecureSkipVerify: true,
		}
	}

	addr := &SimplexAddr{
		URL:         u,
		id:          config.Path,
		interval:    config.Interval,
		maxBodySize: config.MaxBodySize,
		options:     u.Query(),
		config:      config,
	}

	return addr, nil
}

// getHTTPConfig 从SimplexAddr中获取HTTP配置
func getHTTPConfig(addr *SimplexAddr) *HTTPConfig {
	return addr.Config().(*HTTPConfig)
}

// NewHTTPServer 创建HTTP服务端
func NewHTTPServer(network, address string) (*HttpServer, error) {
	addr, err := ResolveHTTPAddr(network, address)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	server := &HttpServer{
		addr:   addr,
		ctx:    ctx,
		cancel: cancel,
	}

	// 启动HTTP服务器
	go server.startHTTPServer()

	// 启动会话清理 (5分钟超时)
	StartSessionCleanup(ctx, &server.buffers, 1*time.Minute, 5*time.Minute, "[HTTP]")

	return server, nil
}

// startHTTPServer 启动HTTP服务器
func (c *HttpServer) startHTTPServer() {
	config := getHTTPConfig(c.addr)

	mux := http.NewServeMux()
	mux.HandleFunc("/", c.handleRequest)

	c.server = &http.Server{
		Addr:         c.addr.Host,
		Handler:      mux,
		ReadTimeout:  config.Timeout,
		WriteTimeout: config.Timeout,
	}

	// 如果有TLS配置，从 HTTP 配置中获取
	httpConfig := getHTTPConfig(c.addr)
	if httpConfig.TLSConfig != nil {
		c.server.TLSConfig = httpConfig.TLSConfig
	}
	var err error
	if c.addr.URL.Scheme == "https" {
		err = c.server.ListenAndServeTLS("", "")
	} else {
		err = c.server.ListenAndServe()
	}

	if err != http.ErrServerClosed {
		logs.Log.Errorf("HTTP server error: %v", err)
	}
}

// NewHTTPClient 创建HTTP客户端
func NewHTTPClient(addr *SimplexAddr) (*HTTPClient, error) {
	config := getHTTPConfig(addr)

	ctx, cancel := context.WithCancel(context.Background())

	// 从 URL 参数获取代理配置
	proxyURL := getOption(addr, "proxy")
	client := createHTTPClient(proxyURL)
	client.Timeout = config.Timeout

	httpClient := &HTTPClient{
		client: client,
		addr:   addr,
		config: config,
		buffer: NewSimplexBuffer(addr),
		ctx:    ctx,
		cancel: cancel,
	}

	return httpClient, nil
}

// HttpServer 实现服务端的PacketConn接口
type HttpServer struct {
	server  *http.Server
	addr    *SimplexAddr
	buffers sync.Map // 替换为sync.Map

	ctx    context.Context
	cancel context.CancelFunc
}

// HTTPClient 实现客户端的PacketConn接口
type HTTPClient struct {
	client *http.Client
	addr   *SimplexAddr
	config *HTTPConfig
	buffer *SimplexBuffer

	ctx    context.Context
	cancel context.CancelFunc
}

func newHTTPBuffer(addr *SimplexAddr) *AsymBuffer {
	return NewAsymBuffer(addr)
}

func (c *HttpServer) Addr() *SimplexAddr {
	return c.addr
}

// Server端方法实现
func (c *HttpServer) Receive() (p *SimplexPacket, addr *SimplexAddr, err error) {
	return AsymServerReceive(c.ctx, &c.buffers)
}

func (c *HttpServer) Send(p *SimplexPackets, addr *SimplexAddr) (n int, err error) {
	return AsymServerSend(c.ctx, &c.buffers, p, addr)
}

func (c *HttpServer) Close() error {
	c.cancel()
	c.buffers.Range(func(_, value interface{}) bool {
		buf := value.(*AsymBuffer)
		buf.Close()
		return true
	})
	if c.server != nil {
		return c.server.Close()
	}
	return nil
}

func (c *HTTPClient) Addr() *SimplexAddr {
	return c.addr
}

// Client端方法实现
// 适配Simplex接口，全部用*SimplexPackets
func (c *HTTPClient) Receive() (*SimplexPacket, *SimplexAddr, error) {
	select {
	case <-c.ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
		pkt, err := c.buffer.GetPacket()
		if pkt != nil {
			return pkt, c.addr, err
		}
		// Buffer empty — poll server for pending responses.
		// HTTP is request-response: server can only deliver data in HTTP responses.
		// Without polling, server-to-client data stays stuck in the server's WriteBuf.
		c.poll()
		pkt, err = c.buffer.GetPacket()
		if err != nil || pkt == nil {
			return nil, c.addr, err
		}
		return pkt, c.addr, err
	}
}

func (c *HTTPClient) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		data := pkts.Marshal()

		req, err := http.NewRequestWithContext(c.ctx, "POST", c.addr.String(), bytes.NewReader(data))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/octet-stream")

		// 设置自定义 Host header
		if c.config.Host != "" {
			req.Host = c.config.Host
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		c.processResponse(resp)
		return len(data), nil
	}
}

func (c *HTTPClient) Close() error {
	c.cancel()
	return nil
}

// processResponse reads response data from an HTTP response and puts packets into the receive buffer.
func (c *HTTPClient) processResponse(resp *http.Response) {
	if resp.StatusCode != http.StatusOK {
		return
	}
	respData, err := io.ReadAll(resp.Body)
	if err != nil || len(respData) == 0 {
		return
	}
	pkts, err := ParseSimplexPackets(respData)
	if err != nil {
		return
	}
	for _, pkt := range pkts.Packets {
		c.buffer.PutPacket(pkt)
	}
}

// poll sends an empty POST to the server to retrieve any pending response data.
// HTTP is request-response: the server can only deliver data in HTTP responses.
// This enables server→client data flow even when the client has nothing to send.
func (c *HTTPClient) poll() {
	req, err := http.NewRequestWithContext(c.ctx, "POST", c.addr.String(), http.NoBody)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.config.Host != "" {
		req.Host = c.config.Host
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	c.processResponse(resp)
}

func (c *HttpServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimLeft(r.URL.Path, "/")
	if r.ContentLength > 0 {
		// 读取请求体
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pkts, err := ParseSimplexPackets(data)
		if err != nil {
			return
		}
		addr := generateAddrFromPath(path, c.addr)
		value, _ := c.buffers.LoadOrStore(path, newHTTPBuffer(addr))
		buf := value.(*AsymBuffer)
		buf.Touch()
		if err := buf.ReadBuf().PutPackets(pkts); err != nil {
			logs.Log.Error(err)
		}
	}

	value, ok := c.buffers.Load(path)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	buf := value.(*AsymBuffer)
	// Send response with any pending data (包括控制包和数据包)
	respPackets, _ := buf.WriteBuf().GetPackets()
	if respPackets.Size() > 0 {
		respData := respPackets.Marshal()
		// 发送响应
		w.Header().Set("Content-Type", "application/octet-stream")
		_, err := w.Write(respData)
		if err != nil {
			logs.Log.Error(err)
			return
		}
		logs.Log.Debugf("HTTP: sent %d bytes to %s", len(respData), r.RemoteAddr)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
	//logs.Log.Debugf("send %d bytes to %s", len(respData), r.RemoteAddr)
}
