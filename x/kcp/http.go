package kcp

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/chainreactors/logs"
)

var (
	DefaultHTTPMaxBody = 128 * 1024 // 128k
)

func ResolveHTTPAddr(network, address string) (*SimplexAddr, error) {
	u, err := url.Parse(fmt.Sprintf("%s://%s/", network, address))
	if err != nil {
		return nil, err
	}
	var interval, maxSize int
	iv := u.Query().Get("internal")
	if iv == "" {
		interval = DefaultSimplexInternal
	} else {
		interval, err = strconv.Atoi(iv)
		if err != nil {
			return nil, err
		}
	}

	max := u.Query().Get("max")
	if max == "" {
		maxSize = DefaultHTTPMaxBody
	} else {
		maxSize, err = strconv.Atoi(max)
		if err != nil {
			return nil, err
		}
	}

	addr := &SimplexAddr{
		URL:         u,
		id:          u.Path,
		internal:    time.Duration(interval) * time.Millisecond,
		maxBodySize: maxSize,
		options:     make(map[string]interface{}),
	}

	if u.Scheme == "https" {
		config := &tls.Config{
			InsecureSkipVerify: true,
		}
		cert, err := generateSelfSignedCert()
		if err != nil {
			return nil, err
		}
		config.Certificates = []tls.Certificate{cert}
		addr.options["tls"] = config
	}

	return addr, nil
}

// httpServer 实现服务端的PacketConn接口
type httpServer struct {
	server  *http.Server
	addr    *SimplexAddr
	buffers sync.Map // 替换为sync.Map

	ctx    context.Context
	cancel context.CancelFunc
}

// httpClient 实现客户端的PacketConn接口
type httpClient struct {
	client *http.Client
	addr   *SimplexAddr
	buffer *ChannelBuffer

	ctx    context.Context
	cancel context.CancelFunc
}

func newHTTPBuffer(addr *SimplexAddr) *httpBuffer {
	return &httpBuffer{
		readBuf:  NewChannel(32, addr.internal),
		writeBuf: NewChannel(32, addr.internal),
		addr:     addr,
	}
}

type httpBuffer struct {
	readBuf  *ChannelBuffer
	writeBuf *ChannelBuffer
	addr     *SimplexAddr
}

func (buf *httpBuffer) Close() error {
	buf.readBuf.Close()
	buf.writeBuf.Close()
	return nil
}

// Server端方法实现
func (c *httpServer) Receive() (p []byte, addr *SimplexAddr, err error) {
	found := false
	c.buffers.Range(func(_, value interface{}) bool {
		buf := value.(*httpBuffer)
		p, err = buf.readBuf.Get()
		if p != nil {
			found = true
			addr = buf.addr
			return false
		}
		return true
	})

	if found {
		return p, addr, err
	}

	select {
	case <-c.ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
		return nil, nil, nil
	}
}

func (c *httpServer) Send(p []byte, addr *SimplexAddr) (n int, err error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		value, _ := c.buffers.LoadOrStore(addr.Path, newHTTPBuffer(addr))
		buf := value.(*httpBuffer)
		return buf.writeBuf.Put(p)
	}
}

func (c *httpServer) Close() error {
	c.cancel()
	c.buffers.Range(func(_, value interface{}) bool {
		buf := value.(*httpBuffer)
		buf.Close()
		return true
	})
	if c.server != nil {
		return c.server.Close()
	}
	return nil
}

// Client端方法实现
func (c *httpClient) Receive() (p []byte, addr *SimplexAddr, err error) {
	select {
	case <-c.ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
		p, err := c.buffer.Get()
		return p, c.addr, err
	}
}

func (c *httpClient) Send(p []byte, addr *SimplexAddr) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		req, err := http.NewRequestWithContext(c.ctx, "POST", c.addr.String(), bytes.NewReader(p))
		if err != nil {
			return 0, err
		}

		req.Header.Set("Content-Type", "application/octet-stream")
		go func() {
			resp, err := c.client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				respData, err := io.ReadAll(resp.Body)
				if err != nil {
					return
				}
				c.buffer.Put(respData)
			}
		}()
		return len(p), nil
	}
}

func (c *httpClient) Close() error {
	c.cancel()
	c.buffer.Close()
	return nil
}

func (c *httpServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Path
	if path == "" {
		path = "/"
	}

	if r.ContentLength > 0 {
		// 读取请求体
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		addr := generateAddrFromPath(r.Host, path)
		value, _ := c.buffers.LoadOrStore(path, newHTTPBuffer(addr))
		buf := value.(*httpBuffer)
		if _, err := buf.readBuf.Put(data); err != nil {
			logs.Log.Error(err)
		}
	}

	value, ok := c.buffers.Load(path)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	buf := value.(*httpBuffer)
	respData, err := buf.writeBuf.Get()
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// 发送响应
	w.Header().Set("Content-Type", "application/octet-stream")
	_, err = w.Write(respData)
	if err != nil {
		logs.Log.Error(err)
		return
	}
	//logs.Log.Debugf("send %d bytes to %s", len(respData), r.RemoteAddr)
}

// 创建HTTP服务端
func newHTTPServer(network, address string) (*httpServer, error) {
	addr, err := ResolveHTTPAddr(network, address)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	conn := &httpServer{
		addr:   addr,
		ctx:    ctx,
		cancel: cancel,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", conn.handleRequest)

	conn.server = &http.Server{
		Addr:    addr.Host,
		Handler: mux,
	}

	if tlsConfig, ok := addr.options["tls"]; ok {
		conn.server.TLSConfig = tlsConfig.(*tls.Config)
	}
	go func() {
		err := conn.server.ListenAndServe()
		if err != http.ErrServerClosed {
			// TODO: 处理错误
		}
	}()

	return conn, nil
}

// 创建HTTP客户端
func newHTTPClient(addr *SimplexAddr) (*httpClient, error) {
	if addr.Path == "/" {
		addr.Path = "/client/" + randomString(8)
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn := &httpClient{
		addr:   addr,
		buffer: NewChannel(32, addr.internal),
		client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		},
		ctx:    ctx,
		cancel: cancel,
	}

	return conn, nil
}

func generateAddrFromPath(host, path string) *SimplexAddr {
	ip := net.IPv4(169, 254, byte(djb2Hash(host)), byte(djb2Hash(path)))
	addr, _ := ResolveHTTPAddr("http", ip.String())
	addr.Path = path
	return addr
}

func djb2Hash(input string) uint32 {
	var hash uint32 = 5381
	for _, char := range input {
		hash = ((hash << 5) + hash) + uint32(char)
	}
	return hash
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var seededRand = rand.New(rand.NewSource(time.Now().UnixNano()))
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(result)
}

// 生成自签名证书
func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{randomString(8)},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour * 24 * 180),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(cryptorand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  key,
	}, nil
}
