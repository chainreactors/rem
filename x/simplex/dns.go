//go:build dns
// +build dns

package simplex

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/x/encoders"
	"github.com/chainreactors/rem/x/xtls"
	"github.com/miekg/dns"
)

func init() {
	RegisterSimplex("dns", func(addr *SimplexAddr) (Simplex, error) {
		return NewDNSClient(addr)
	}, func(network, address string) (Simplex, error) {
		return NewDNSServer(network, address)
	}, func(network, address string) (*SimplexAddr, error) {
		return ResolveDNSAddr(network, address)
	})

	RegisterSimplex("doh", func(addr *SimplexAddr) (Simplex, error) {
		return NewDNSClient(addr)
	}, func(network, address string) (Simplex, error) {
		return NewDNSServer(network, address)
	}, func(network, address string) (*SimplexAddr, error) {
		return ResolveDNSAddr(network, address)
	})

	RegisterSimplex("dot", func(addr *SimplexAddr) (Simplex, error) {
		return NewDNSClient(addr)
	}, func(network, address string) (Simplex, error) {
		return NewDNSServer(network, address)
	}, func(network, address string) (*SimplexAddr, error) {
		return ResolveDNSAddr(network, address)
	})
}

const (
	// DNS RFC 1035 limits
	MaxUDPMessageSize  = 512   // UDP DNS message max size
	MaxHTTPMessageSize = 65535 // TCP DNS message max size
	MaxLabelLength     = 63    // Max length per DNS label
	MaxDomainLength    = 253   // Max total domain name length

	// 默认配置
	DefaultDNSInterval = 500             // 最大发包间隔(毫秒)
	DefaultDNSPort     = 53              // 默认DNS端口
	DefaultDNSTimeout  = 5 * time.Second // 默认超时时间
)

// DNS 配置结构
type DNSConfig struct {
	Domain   string        // 基础域名
	Interval time.Duration // 发包间隔
	Timeout  time.Duration // 请求超时
	MaxSize  int           // 最大消息大小
	Protocol string        // 协议类型: "udp", "tcp", "doh", "dot"等

	// TLS相关配置
	TLSConfig *tls.Config // TLS配置
	CertFile  string      // 证书文件路径
	KeyFile   string      // 私钥文件路径
	CAFile    string      // CA文件路径

	// DoH相关配置
	HTTPPath   string // DoH HTTP路径，默认为"/dns-query"
	HTTPMethod string // DoH HTTP方法，"GET"或"POST"
}

// DefaultDNSConfig 返回默认的DNS配置
func DefaultDNSConfig() *DNSConfig {
	return &DNSConfig{
		Domain:     "example.com",
		Interval:   DefaultDNSInterval * time.Millisecond,
		Timeout:    DefaultDNSTimeout,
		MaxSize:    MaxDomainLength,
		Protocol:   "udp",
		HTTPPath:   "/dns-query",
		HTTPMethod: "POST",
	}
}

func ResolveDNSAddr(network, address string) (*SimplexAddr, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}

	config := DefaultDNSConfig()

	// 解析域名（必须）
	if domain := u.Query().Get("domain"); domain != "" {
		config.Domain = domain
	} else {
		return nil, fmt.Errorf("domain is required")
	}

	// 解析间隔
	if iv := u.Query().Get("interval"); iv != "" {
		if interval, err := strconv.Atoi(iv); err == nil {
			config.Interval = time.Duration(interval) * time.Millisecond
		}
	}

	// 解析最大消息大小
	if max := u.Query().Get("max"); max != "" {
		if maxSize, err := strconv.Atoi(max); err == nil {
			config.MaxSize = maxSize
		}
	} else {
		// 计算默认最大消息大小
		// 查询格式: chunk1.chunk2...chunkN.connID.domain
		// connID 固定 8 字符, 加 1 个点分隔符 = 9 字符开销
		// 数据经过 B58 编码后膨胀约 1.37 倍, 每 63 字符需要 1 个点分隔符
		// 精确公式: 可用字符 = 253 - len(domain) - 9(connID+dot)
		//          B58 字符数 = 可用字符 - ceil(可用字符/64) (减去 label dots)
		//          原始字节数 = B58字符数 / 1.37
		available := MaxDomainLength - len(config.Domain) - 9 // connID(8) + dot(1)
		// 每 64 字符 (63 data + 1 dot) 中有 63 字符是数据
		b58Chars := available * MaxLabelLength / (MaxLabelLength + 1) // 去掉 label dots
		config.MaxSize = int(float64(b58Chars) / 1.37)               // B58 decode shrink
	}

	// 解析超时时间
	if timeout := u.Query().Get("timeout"); timeout != "" {
		if t, err := strconv.Atoi(timeout); err == nil {
			config.Timeout = time.Duration(t) * time.Second
		}
	}

	switch network {
	case "tcp":
		config.Protocol = network
	case "dot":
		config.Protocol = "dot"
	case "doh":
		config.Protocol = "doh"
	default:
		config.Protocol = "udp"
	}

	// 解析TLS相关配置
	if certFile := u.Query().Get("cert"); certFile != "" {
		config.CertFile = certFile
	}
	if keyFile := u.Query().Get("key"); keyFile != "" {
		config.KeyFile = keyFile
	}
	if caFile := u.Query().Get("ca"); caFile != "" {
		config.CAFile = caFile
	}

	// 解析DoH相关配置
	if httpPath := u.Query().Get("path"); httpPath != "" {
		config.HTTPPath = httpPath
	}
	if httpMethod := u.Query().Get("method"); httpMethod != "" {
		config.HTTPMethod = strings.ToUpper(httpMethod)
	}

	// 根据协议设置TLS配置
	if config.Protocol == "dot" || config.Protocol == "doh" {
		tlsConfig, err := xtls.NewServerTLSConfig(config.CertFile, config.KeyFile, config.CAFile)
		if err != nil {
			logs.Log.Warnf("Failed to create TLS config, using self-signed: %v", err)
			// 使用自签名证书
			tlsConfig, _ = xtls.NewServerTLSConfig("", "", "")
		}
		config.TLSConfig = tlsConfig
	}

	addr := &SimplexAddr{
		URL:         u,
		id:          randomString(8),
		interval:    config.Interval,
		maxBodySize: config.MaxSize,
		options:     u.Query(),
		config:      config,
	}

	return addr, nil
}

// getDNSConfig 从SimplexAddr中获取DNS配置
func getDNSConfig(addr *SimplexAddr) *DNSConfig {
	if config, ok := addr.Config().(*DNSConfig); ok {
		return config
	}

	// 如果没有配置，从 URL 参数重新构建配置
	config := DefaultDNSConfig()

	if interval := addr.options.Get("interval"); interval != "" {
		if iv, err := strconv.Atoi(interval); err == nil {
			config.Interval = time.Duration(iv) * time.Millisecond
		}
	}

	if maxSize := addr.options.Get("max"); maxSize != "" {
		if max, err := strconv.Atoi(maxSize); err == nil {
			config.MaxSize = max
		}
	}

	if timeout := addr.options.Get("timeout"); timeout != "" {
		if to, err := strconv.Atoi(timeout); err == nil {
			config.Timeout = time.Duration(to) * time.Second
		}
	}

	if cert := addr.options.Get("cert"); cert != "" {
		config.CertFile = cert
	}

	if key := addr.options.Get("key"); key != "" {
		config.KeyFile = key
	}

	if ca := addr.options.Get("ca"); ca != "" {
		config.CAFile = ca
	}

	if path := addr.options.Get("path"); path != "" {
		config.HTTPPath = path
	}

	if method := addr.options.Get("method"); method != "" {
		config.HTTPMethod = method
	}

	return config
}

// NewDNSServer 创建DNS服务端
func NewDNSServer(network, address string) (*DNSServer, error) {
	addr, err := ResolveDNSAddr(network, address)
	if err != nil {
		return nil, err
	}

	config := getDNSConfig(addr)
	ctx, cancel := context.WithCancel(context.Background())

	server := &DNSServer{
		addr:   addr,
		domain: config.Domain,
		ctx:    ctx,
		cancel: cancel,
	}

	// 启动DNS服务器
	go server.startDNSServer()

	// 启动会话清理 (5分钟超时)
	StartSessionCleanup(ctx, &server.buffers, 1*time.Minute, 5*time.Minute, "[DNS]")

	return server, nil
}

// NewDNSClient 创建DNS客户端
func NewDNSClient(addr *SimplexAddr) (*DNSClient, error) {
	config := getDNSConfig(addr)
	ctx, cancel := context.WithCancel(context.Background())

	client := &DNSClient{
		addr:   addr,
		config: config,
		buffer: NewSimplexBuffer(addr),
		ctx:    ctx,
		cancel: cancel,
	}

	// 根据协议类型配置客户端
	switch config.Protocol {
	case "udp", "tcp":
		client.client = &dns.Client{
			Net:     config.Protocol,
			Timeout: config.Timeout,
			UDPSize: 1024,
		}
	case "dot":
		client.client = &dns.Client{
			Net:       "tcp-tls",
			Timeout:   config.Timeout,
			TLSConfig: config.TLSConfig,
			UDPSize:   1024,
		}
	case "doh":
		// DoH使用HTTP客户端，不需要dns.Client
		client.httpClient = &http.Client{
			Timeout: config.Timeout,
			Transport: &http.Transport{
				TLSClientConfig: config.TLSConfig,
			},
		}
	default:
		cancel()
		return nil, fmt.Errorf("unsupported DNS protocol: %s", config.Protocol)
	}

	return client, nil
}

// DNSServer 实现服务端的PacketConn接口
type DNSServer struct {
	server     *dns.Server
	httpServer *http.Server
	addr       *SimplexAddr
	buffers    sync.Map
	domain     string

	ctx    context.Context
	cancel context.CancelFunc
}

// DNSClient 实现客户端的PacketConn接口
type DNSClient struct {
	client     *dns.Client
	httpClient *http.Client
	addr       *SimplexAddr
	config     *DNSConfig
	buffer     *SimplexBuffer

	ctx    context.Context
	cancel context.CancelFunc
}

func (c *DNSClient) Addr() *SimplexAddr {
	return c.addr
}

// Client端方法实现
// 适配Simplex接口，全部用*SimplexPackets
func (c *DNSClient) Receive() (*SimplexPacket, *SimplexAddr, error) {
	select {
	case <-c.ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
		data, err := c.buffer.RecvGet()
		if err != nil || data == nil {
			return nil, c.addr, err
		}
		pkt, err := ParseSimplexPacket(data)
		if err != nil || pkt == nil {
			return nil, c.addr, err
		}
		if pkt.Size() == 0 {
			return nil, c.addr, nil
		}
		return pkt, c.addr, nil
	}
}

func (c *DNSClient) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
		if c.config.Protocol == "doh" {
			return c.sendDoH(pkts, addr)
		}
		return c.sendDNS(pkts, addr)
	}
}

// sendDNS 发送标准DNS查询 (UDP/TCP/DoT)
func (c *DNSClient) sendDNS(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	data := pkts.Marshal()

	var dataToSend []byte
	var actualSent int

	if len(data) > 0 {
		if len(data) <= addr.maxBodySize {
			// Full data fits
			dataToSend = data
			actualSent = len(data)
		}
	} else {
		// For polling (p is nil), send empty query to trigger response
		dataToSend = nil
		actualSent = 0
	}

	var encoded string
	if len(dataToSend) > 0 {
		encoded = encoders.B58Encode(dataToSend)
	} else {
		encoded = ""
	}

	// Split into chunks for DNS labels (each label max 63 chars)
	chunks := c.splitIntoChunks(encoded, MaxLabelLength)

	// If no chunks (empty data), create a single polling chunk
	if len(chunks) == 0 {
		chunks = []string{""}
	}

	// Create query with all chunks
	query := c.createMultiLabelQuery(chunks)

	// Final validation
	if len(query) > MaxDomainLength {
		logs.Log.Errorf("DNS query too long after calculation: %d > %d", len(query), MaxDomainLength)
		return 0, fmt.Errorf("DNS query too long: %d > %d", len(query), MaxDomainLength)
	}

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(query), dns.TypeTXT)

	// Debug: log the query name
	logs.Log.Debugf("bytes: %d, actual sent: %d, max allowed: %d, Final DNS query with FQDN: %s",
		len(data), actualSent, c.addr.maxBodySize, dns.Fqdn(query))

	// 使用客户端的Host作为DNS服务器地址
	targetServer := c.addr.Host
	// 如果DNS服务器地址没有端口，添加默认端口
	if !strings.Contains(targetServer, ":") {
		targetServer = net.JoinHostPort(targetServer, "53")
	}

	// Send query
	resp, _, err := c.client.Exchange(msg, targetServer)
	if err != nil {
		logs.Log.Errorf("DNS query failed for query '%s': %v", query, err)
		return 0, fmt.Errorf("DNS query failed: %v", err)
	}

	// Process response
	if resp != nil && len(resp.Answer) > 0 {
		if txt, ok := resp.Answer[0].(*dns.TXT); ok && len(txt.Txt) > 0 {
			// Combine all TXT strings and decode
			combinedTxt := strings.Join(txt.Txt, "")
			if combinedTxt != "" {
				decoded := encoders.B58Decode(combinedTxt)
				if len(decoded) > 0 {
					c.buffer.RecvPut(decoded)
				}
			}
		}
	}

	return actualSent, nil
}

// sendDoH 发送DoH查询
func (c *DNSClient) sendDoH(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	data := pkts.Marshal()

	var dataToSend []byte
	var actualSent int

	if len(data) > 0 {
		if len(data) <= addr.maxBodySize {
			dataToSend = data
			actualSent = len(data)
		}
	} else {
		dataToSend = nil
		actualSent = 0
	}

	var encoded string
	if len(dataToSend) > 0 {
		encoded = encoders.B58Encode(dataToSend)
	} else {
		encoded = ""
	}

	// Split into chunks for DNS labels (each label max 63 chars)
	chunks := c.splitIntoChunks(encoded, MaxLabelLength)
	if len(chunks) == 0 {
		chunks = []string{""}
	}

	// Create query with all chunks
	query := c.createMultiLabelQuery(chunks)

	// Create DNS message
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(query), dns.TypeTXT)

	// Pack DNS message for DoH
	dnsData, err := msg.Pack()
	if err != nil {
		return 0, fmt.Errorf("failed to pack DNS message: %v", err)
	}

	// 使用客户端的Host作为DNS服务器地址
	targetServer := c.addr.Host

	// Create DoH URL
	serverURL := fmt.Sprintf("https://%s%s", targetServer, c.config.HTTPPath)

	var httpResp *http.Response

	if c.config.HTTPMethod == "GET" {
		// RFC 8484 - GET method with base64url encoded dns parameter
		encodedDNS := base64.URLEncoding.EncodeToString(dnsData)
		fullURL := fmt.Sprintf("%s?dns=%s", serverURL, encodedDNS)

		httpResp, err = c.httpClient.Get(fullURL)
	} else {
		// RFC 8484 - POST method with DNS message in body
		httpResp, err = c.httpClient.Post(serverURL, "application/dns-message", strings.NewReader(string(dnsData)))
	}

	if err != nil {
		logs.Log.Errorf("DoH request failed: %v", err)
		return 0, fmt.Errorf("DoH request failed: %v", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("DoH server returned status: %d", httpResp.StatusCode)
	}

	// Read response
	respData, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read DoH response: %v", err)
	}

	// Unpack DNS response
	dnsResp := new(dns.Msg)
	err = dnsResp.Unpack(respData)
	if err != nil {
		return 0, fmt.Errorf("failed to unpack DNS response: %v", err)
	}

	// Process response (same as standard DNS)
	if len(dnsResp.Answer) > 0 {
		if txt, ok := dnsResp.Answer[0].(*dns.TXT); ok && len(txt.Txt) > 0 {
			combinedTxt := strings.Join(txt.Txt, "")
			if combinedTxt != "" {
				decoded := encoders.B58Decode(combinedTxt)
				if len(decoded) > 0 {
					c.buffer.RecvPut(decoded)
				}
			}
		}
	}

	logs.Log.Debugf("DoH: sent %d bytes, received %d bytes", actualSent, len(respData))
	return actualSent, nil
}

func (c *DNSClient) Close() error {
	c.cancel()
	return nil
}

// createMultiLabelQuery creates a query with multiple data labels
func (c *DNSClient) createMultiLabelQuery(chunks []string) string {
	// Build query with multiple labels: data1.data2.data3...path.domain
	var labels []string

	// Add all data labels
	labels = append(labels, chunks...)

	labels = append(labels, c.addr.id)
	labels = append(labels, c.config.Domain)

	return strings.TrimLeft(strings.Join(labels, "."), ".")
}

// splitIntoChunks splits a string into chunks of specified size
func (c *DNSClient) splitIntoChunks(s string, chunkSize int) []string {
	var chunks []string
	for i := 0; i < len(s); i += chunkSize {
		end := i + chunkSize
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
	}
	return chunks
}

// Server端方法实现
func (c *DNSServer) Receive() (pkt *SimplexPacket, addr *SimplexAddr, err error) {
	return AsymServerReceive(c.ctx, &c.buffers)
}

func (c *DNSServer) Send(pkts *SimplexPackets, addr *SimplexAddr) (n int, err error) {
	return AsymServerSend(c.ctx, &c.buffers, pkts, addr)
}

// startDNSServer 启动DNS服务器
func (c *DNSServer) startDNSServer() {
	config := getDNSConfig(c.addr)

	// Setup DNS server
	mux := dns.NewServeMux()
	// 注册处理器以处理所有子域名查询（使用通配符）
	mux.HandleFunc(".", c.handleDNSQuery)

	serverAddr := c.addr.Host

	switch config.Protocol {
	case "udp", "dns":
		c.addr.maxBodySize = MaxUDPMessageSize
		c.server = &dns.Server{
			Addr:    serverAddr,
			Net:     config.Protocol,
			Handler: mux,
		}
		err := c.server.ListenAndServe()
		if err != nil {
			logs.Log.Errorf("DNS server error: %v", err)
		}

	case "dot":
		c.server = &dns.Server{
			Addr:      serverAddr,
			Net:       "tcp-tls",
			Handler:   mux,
			TLSConfig: config.TLSConfig,
		}
		err := c.server.ListenAndServe()
		if err != nil {
			logs.Log.Errorf("DoT server error: %v", err)
		}

	case "doh":
		c.addr.maxBodySize = MaxHTTPMessageSize
		c.startDoHServer(config)

	default:
		logs.Log.Errorf("Unsupported DNS protocol: %s", config.Protocol)
	}
}

// startDoHServer 启动DoH (DNS-over-HTTPS) 服务器
func (c *DNSServer) startDoHServer(config *DNSConfig) {
	httpMux := http.NewServeMux()
	httpMux.HandleFunc(config.HTTPPath, c.handleDoHRequest)

	c.httpServer = &http.Server{
		Addr:      c.addr.Host,
		Handler:   httpMux,
		TLSConfig: config.TLSConfig,
	}

	var err error
	if config.TLSConfig != nil {
		logs.Log.Infof("Starting DoH server with TLS on %s", c.httpServer.Addr)
		err = c.httpServer.ListenAndServeTLS("", "")
	} else {
		logs.Log.Infof("Starting DoH server without TLS on %s", c.httpServer.Addr)
		err = c.httpServer.ListenAndServe()
	}

	if err != http.ErrServerClosed {
		logs.Log.Errorf("DoH server error: %v", err)
	}
}

// handleDoHRequest 处理DoH请求
func (c *DNSServer) handleDoHRequest(w http.ResponseWriter, r *http.Request) {
	var dnsMsg []byte
	var err error

	// 根据HTTP方法处理请求
	switch r.Method {
	case "GET":
		// RFC 8484 - GET方法使用base64url编码的dns参数
		dnsParam := r.URL.Query().Get("dns")
		if dnsParam == "" {
			http.Error(w, "Missing dns parameter", http.StatusBadRequest)
			return
		}
		dnsMsg, err = base64.URLEncoding.DecodeString(dnsParam)
		if err != nil {
			http.Error(w, "Invalid dns parameter encoding", http.StatusBadRequest)
			return
		}

	case "POST":
		// RFC 8484 - POST方法将DNS消息放在请求体中
		dnsMsg, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 解析DNS消息
	msg := new(dns.Msg)
	err = msg.Unpack(dnsMsg)
	if err != nil {
		http.Error(w, "Invalid DNS message", http.StatusBadRequest)
		return
	}

	// 处理DNS查询（复用现有的handleDNSQuery逻辑）
	response := c.processDNSQuery(msg)

	// 打包响应
	respData, err := response.Pack()
	if err != nil {
		http.Error(w, "Failed to pack DNS response", http.StatusInternalServerError)
		return
	}

	// 设置响应头
	w.Header().Set("Content-Type", "application/dns-message")
	w.Header().Set("Cache-Control", "max-age=0")

	// 发送响应
	w.WriteHeader(http.StatusOK)
	w.Write(respData)
}

// processDNSQuery 处理DNS查询的核心逻辑（从handleDNSQuery中提取）
func (c *DNSServer) processDNSQuery(r *dns.Msg) *dns.Msg {
	// Extract data from query name and trim leading dot
	queryName := strings.Trim(r.Question[0].Name, ".")
	labels := strings.Split(queryName, ".")

	response := new(dns.Msg)
	response.SetReply(r)

	// 检查查询类型
	if len(r.Question) == 0 {
		return response
	}

	question := r.Question[0]
	qtype := question.Qtype

	// Find domain part and path part
	domainIndex := -1
	domainLabels := strings.Split(c.domain, ".")

	// Check if the domain labels match at the end of the query
	if len(labels) >= len(domainLabels) {
		// Start from the end and check if domain labels match
		startIndex := len(labels) - len(domainLabels)
		match := true
		for i, domainLabel := range domainLabels {
			if labels[startIndex+i] != domainLabel {
				match = false
				break
			}
		}
		if match {
			domainIndex = startIndex
		}
	}

	// 如果不匹配配置域名，直接返回空响应
	if domainIndex == -1 {
		return response
	}
	// 处理NS记录查询 - 为子域名返回权威NS记录
	if qtype == dns.TypeNS {
		// 创建NS记录，指向当前DNS服务器
		nsRecord := &dns.NS{
			Hdr: dns.RR_Header{
				Name:   question.Name,
				Rrtype: dns.TypeNS,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			Ns: dns.Fqdn(c.domain), // 指向配置的域名
		}
		response.Answer = append(response.Answer, nsRecord)
		logs.Log.Debugf("DNSServer: Responded to NS query for %s with %s", queryName, c.domain)
		return response
	}

	// Extract path and data labels (everything before domain)
	pathAndDataLabels := labels[:domainIndex]
	if len(pathAndDataLabels) < 1 {
		// Need at least path
		return response
	}

	// Last label is path (connection ID), others are data
	path := pathAndDataLabels[len(pathAndDataLabels)-1]
	dataLabels := pathAndDataLabels[:len(pathAndDataLabels)-1]

	// Get or create buffer using path as unique ID
	addr := generateAddrFromPath(path, c.addr)
	value, _ := c.buffers.LoadOrStore(path, NewAsymBuffer(addr))
	buf := value.(*AsymBuffer)
	buf.Touch()

	// Add data labels to buffer (if any)
	if len(dataLabels) > 0 {
		// Skip empty polling requests
		if dataLabels[0] == "" {
			// This is a polling request, just continue
		} else {
			// Join data labels and decode
			dataStr := strings.Join(dataLabels, "")
			decoded := encoders.B58Decode(dataStr)
			if len(decoded) > 0 {
				packet, err := ParseSimplexPacket(decoded)
				if err != nil {
					return response
				}
				logs.Log.Debugf("DNSServer: Received packet from %s, size: %d", addr.String(), len(decoded))
				buf.ReadBuf().PutPacket(packet)
			}
		}
	}

	// Send response with any pending data (包括控制包和数据包)
	respPackets, _ := buf.WriteBuf().GetPackets()
	if respPackets.Size() > 0 {
		respData := respPackets.Marshal()
		encoded := encoders.B58Encode(respData)
		logs.Log.Debugf("DNSServer: Sending response to %s, size: %d", addr.String(), len(encoded))
		// Split response data into DNS labels (each label max 63 chars)
		responseChunks := c.splitIntoChunks(encoded, MaxLabelLength)
		if len(responseChunks) > 0 {
			// Create TXT record with all chunks
			txtRecord := &dns.TXT{
				Hdr: dns.RR_Header{
					Name:   r.Question[0].Name,
					Rrtype: dns.TypeTXT,
					Class:  dns.ClassINET,
					Ttl:    0,
				},
				Txt: responseChunks,
			}
			response.Answer = append(response.Answer, txtRecord)
		}
	}

	return response
}

func (c *DNSServer) handleDNSQuery(w dns.ResponseWriter, r *dns.Msg) {
	response := c.processDNSQuery(r)
	w.WriteMsg(response)
}

func (c *DNSServer) Addr() *SimplexAddr {
	return c.addr
}

func (c *DNSServer) Close() error {
	c.cancel()
	c.buffers.Range(func(_, value interface{}) bool {
		buf := value.(*AsymBuffer)
		buf.Close()
		return true
	})

	// 关闭DNS服务器
	if c.server != nil {
		err := c.server.Shutdown()
		if err != nil {
			logs.Log.Errorf("Error shutting down DNS server: %v", err)
		}
	}

	// 关闭HTTP服务器（用于DoH）
	if c.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := c.httpServer.Shutdown(ctx)
		if err != nil {
			logs.Log.Errorf("Error shutting down DoH server: %v", err)
		}
	}

	return nil
}

// splitIntoChunks splits a string into chunks of specified size
func (c *DNSServer) splitIntoChunks(s string, chunkSize int) []string {
	var chunks []string
	for i := 0; i < len(s); i += chunkSize {
		end := i + chunkSize
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
	}
	return chunks
}
