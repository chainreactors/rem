//go:build oss
// +build oss

package simplex

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/logs"
)

func init() {
	RegisterSimplex("oss", func(addr *SimplexAddr) (Simplex, error) {
		return NewOSSClient(addr)
	}, func(network, address string) (Simplex, error) {
		return NewOSSServer(network, address)
	}, func(network, address string) (*SimplexAddr, error) {
		return ResolveOSSAddr(network, address)
	})
}

const (
	DefaultOSSInterval = 2000             // 2000 milliseconds (cloud storage API, match OneDrive)
	MaxOSSMessageSize  = 1024 * 1024      // 1MB
	DefaultOSSTimeout  = 30 * time.Second // 30 seconds
	SessionTimeout     = 300 * time.Second // 5 minutes session timeout (cloud storage high latency)
)

// OSSConfig contains configuration for OSS connection
type OSSConfig struct {
	Host         string        // OSS host (e.g., bucket.oss-cn-shanghai.aliyuncs.com or custom domain)
	Bucket       string        // OSS bucket name (for signing)
	AccessKeyId  string        // Access Key ID (empty = anonymous)
	AccessSecret string        // Access Key Secret (empty = anonymous)
	Prefix       string        // Path prefix from URL (e.g., /oss/)
	SessionID    string        // Session ID (filename)
	Interval     time.Duration // Polling interval
	MaxBodySize  int           // Maximum message size
	ServerAddr   string        // Server HTTP address for mirror back-to-origin
	Timeout      time.Duration // HTTP timeout
	Mode         string        // "mirror" or "file" (default: "file")
}

// OSSObjectInfo represents an object returned by ListObjects
type OSSObjectInfo struct {
	Key  string
	Size int64
}

// OSSConnector defines interface for OSS operations
type OSSConnector interface {
	PutObject(key string, data []byte) error
	GetObject(key string) ([]byte, error)
	HeadObject(key string) (bool, error)
	DeleteObject(key string) error
	ListObjects(prefix string) ([]OSSObjectInfo, error)
	GenerateOSSURL(key string) string
	Sign(req *http.Request, key string) error
	Validate() error
}

// aliyunOSSConnector implements OSSConnector for Aliyun OSS
type aliyunOSSConnector struct {
	client *http.Client
	config *OSSConfig
}

// newOSSConnector creates a new OSS connector (currently Aliyun OSS)
func newOSSConnector(config *OSSConfig, httpClient *http.Client) OSSConnector {
	return &aliyunOSSConnector{
		client: httpClient,
		config: config,
	}
}

// Validate validates OSS credentials by performing a HEAD bucket request
func (c *aliyunOSSConnector) Validate() error {
	// Skip validation for anonymous access
	if c.config.AccessKeyId == "" {
		logs.Log.Infof("[OSS] Using anonymous access mode (no credentials)")
		return nil
	}

	// Build bucket URL (always use HTTPS)
	bucketURL := fmt.Sprintf("https://%s/", c.config.Host)

	// Create HEAD request to validate credentials
	req, err := http.NewRequest(http.MethodHead, bucketURL, nil)
	if err != nil {
		return err
	}

	// Sign the request
	if err := c.Sign(req, ""); err != nil {
		return err
	}

	// Execute request
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to OSS: %v", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusForbidden {
		// 200 OK: bucket exists and accessible
		// 403 Forbidden: credentials are valid but may lack permissions (still valid credentials)
		return nil
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid credentials (status 401)")
	}

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("bucket not found (status 404)")
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("validation failed with status %d: %s", resp.StatusCode, string(body))
}

// buildOSSURL constructs full OSS URL
func (c *aliyunOSSConnector) buildOSSURL(key string) string {
	return fmt.Sprintf("https://%s/%s", c.config.Host, key)
}

// doRequest executes an HTTP request with signing and logs errors
func (c *aliyunOSSConnector) doRequest(method, key string, body io.Reader) (*http.Response, error) {
	apiURL := c.buildOSSURL(key)

	req, err := http.NewRequest(method, apiURL, body)
	if err != nil {
		return nil, err
	}

	if err := c.Sign(req, key); err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	// Log unexpected status codes (exclude expected errors)
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		shouldLog := true

		// Handle 404 - expected when file doesn't exist
		if resp.StatusCode == http.StatusNotFound {
			shouldLog = false
		}

		// Handle 424 - mirror back-to-origin failed
		// Don't log if error contains "204" (expected when server has no data)
		if resp.StatusCode == 424 && strings.Contains(string(bodyBytes), "204") {
			shouldLog = false
		}

		if shouldLog {
			logs.Log.Warnf("[OSS] Request failed: %s %s -> HTTP %d: %s", method, key, resp.StatusCode, string(bodyBytes))
		}

		// Return error response with body for caller to handle
		resp.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
	}

	return resp, nil
}

// Sign implements Aliyun OSS Signature V1
func (c *aliyunOSSConnector) Sign(req *http.Request, key string) error {
	if c.config.AccessKeyId == "" {
		// Anonymous access, no signature
		return nil
	}

	// Build resource path for signing
	// When using bucket subdomain (e.g., bucket.oss-cn-shanghai.aliyuncs.com),
	// the resource path is /{bucket}/{key}
	// The bucket is part of the canonical resource even when using subdomain
	var resource string
	if c.config.Bucket != "" {
		resource = "/" + c.config.Bucket + "/" + key
	} else {
		resource = "/" + key
	}

	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("Date", date)

	// OSS Signature V1: VERB + "\n" + Content-MD5 + "\n" + Content-Type + "\n" + Date + "\n" + CanonicalizedResource
	stringToSign := req.Method + "\n" +
		req.Header.Get("Content-MD5") + "\n" +
		req.Header.Get("Content-Type") + "\n" +
		date + "\n" +
		resource

	h := hmac.New(sha1.New, []byte(c.config.AccessSecret))
	h.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))

	req.Header.Set("Authorization", "OSS "+c.config.AccessKeyId+":"+signature)
	return nil
}

// PutObject uploads data to OSS
func (c *aliyunOSSConnector) PutObject(key string, data []byte) error {
	// Calculate Content-MD5 for data integrity
	md5Hash := md5.Sum(data)
	md5Base64 := base64.StdEncoding.EncodeToString(md5Hash[:])

	// Create request with MD5
	apiURL := c.buildOSSURL(key)
	req, err := http.NewRequest(http.MethodPut, apiURL, strings.NewReader(string(data)))
	if err != nil {
		return err
	}

	// Set Content-MD5 header
	req.Header.Set("Content-MD5", md5Base64)

	// Sign and execute
	if err := c.Sign(req, key); err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PutObject failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetObject retrieves object from OSS
func (c *aliyunOSSConnector) GetObject(key string) ([]byte, error) {
	resp, err := c.doRequest(http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("GetObject %s: %w", key, os.ErrNotExist)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GetObject failed with status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// HeadObject checks if object exists (returns true if exists, false if not)
func (c *aliyunOSSConnector) HeadObject(key string) (bool, error) {
	resp, err := c.doRequest(http.MethodHead, key, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, nil // File exists
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil // File not exists (normal)
	}

	return false, fmt.Errorf("HeadObject failed with status %d", resp.StatusCode)
}

// DeleteObject deletes object from OSS
func (c *aliyunOSSConnector) DeleteObject(key string) error {
	resp, err := c.doRequest(http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("DeleteObject failed with status %d", resp.StatusCode)
	}

	return nil
}

// ListObjects lists objects with given prefix
func (c *aliyunOSSConnector) ListObjects(prefix string) ([]OSSObjectInfo, error) {
	// OSS List Objects V2 API: GET /?list-type=2&prefix=xxx
	apiURL := fmt.Sprintf("https://%s/?list-type=2&prefix=%s", c.config.Host, url.QueryEscape(prefix))

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}

	if err := c.Sign(req, ""); err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ListObjects failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse XML response
	type listResult struct {
		XMLName  xml.Name `xml:"ListBucketResult"`
		Contents []struct {
			Key  string `xml:"Key"`
			Size int64  `xml:"Size"`
		} `xml:"Contents"`
	}

	var result listResult
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse ListObjects response: %v", err)
	}

	objects := make([]OSSObjectInfo, 0, len(result.Contents))
	for _, c := range result.Contents {
		objects = append(objects, OSSObjectInfo{Key: c.Key, Size: c.Size})
	}
	return objects, nil
}

// GenerateOSSURL generates public OSS URL
func (c *aliyunOSSConnector) GenerateOSSURL(key string) string {
	return c.buildOSSURL(key)
}

// ResolveOSSAddr parses OSS URL and creates SimplexAddr
func ResolveOSSAddr(network, address string) (*SimplexAddr, error) {
	// URL format: oss://{host}/[sessionid]?bucket=xxx&ak=xxx&sk=xxx&server=:8080&...
	u, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("invalid oss address: %v", err)
	}

	// Host can be any OSS compatible endpoint
	host := u.Host
	if host == "" {
		return nil, fmt.Errorf("host is required")
	}

	// Get bucket name from query parameter or try to extract from host
	bucket := u.Query().Get("bucket")
	if bucket == "" {
		// Try to extract bucket from host if it follows {bucket}.{endpoint} format
		// e.g., mybucket.oss-cn-shanghai.aliyuncs.com -> mybucket
		parts := strings.SplitN(host, ".", 2)
		if len(parts) == 2 && strings.Contains(parts[1], "aliyuncs.com") {
			bucket = parts[0]
		}
		// For custom domains or other scenarios, bucket remains empty
	}

	// Parse prefix and sessionId from path
	path := u.Path
	var prefix, sessionId string

	// Ensure path starts with /
	if path == "" {
		path = "/"
	}

	if strings.HasSuffix(path, "/") {
		// Directory path: /oss/ → prefix: /oss/, sessionId: auto-generated
		prefix = path
		sessionId = randomString(8)
	} else {
		// File path: /oss/1.txt → prefix: /oss/, sessionId: 1.txt
		lastSlash := strings.LastIndex(path, "/")
		prefix = path[:lastSlash+1] // Include trailing /
		sessionId = path[lastSlash+1:]
	}

	// Parse configuration from query parameters
	var interval int
	if intervalStr := u.Query().Get("interval"); intervalStr != "" {
		interval, err = strconv.Atoi(intervalStr)
		if err != nil {
			return nil, fmt.Errorf("invalid interval: %v", err)
		}
	} else {
		interval = DefaultOSSInterval
	}

	var maxBodySize int
	if maxStr := u.Query().Get("max"); maxStr != "" {
		maxBodySize, err = strconv.Atoi(maxStr)
		if err != nil {
			return nil, fmt.Errorf("invalid max body size: %v", err)
		}
	} else {
		maxBodySize = MaxOSSMessageSize
	}

	var timeout time.Duration
	if timeoutStr := u.Query().Get("timeout"); timeoutStr != "" {
		timeoutSec, err := strconv.Atoi(timeoutStr)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout: %v", err)
		}
		timeout = time.Duration(timeoutSec) * time.Second
	} else {
		timeout = DefaultOSSTimeout
	}

	// Create temporary SimplexAddr for getOption
	query := u.Query()
	tmpAddr := &SimplexAddr{URL: u, options: query}

	// Parse access key and server from query or environment
	accessKeyId := getOption(tmpAddr, "ak")
	accessSecret := getOption(tmpAddr, "sk")
	serverAddr := getOption(tmpAddr, "server")
	if serverAddr == "" {
		serverAddr = ":18080"
	}

	// Parse mode: "mirror" or "file"
	// Auto-detect: if server= is set, default to "mirror"; otherwise "file"
	mode := u.Query().Get("mode")
	if mode == "" {
		if u.Query().Get("server") != "" || getOption(tmpAddr, "server") != "" {
			mode = "mirror"
		} else {
			mode = "file"
		}
	}

	// Check credentials mode
	if accessKeyId == "" && accessSecret == "" {
		logs.Log.Debugf("[OSS] Anonymous access mode enabled (no AK/SK provided)")
	} else if accessKeyId == "" || accessSecret == "" {
		logs.Log.Warnf("[OSS] Incomplete credentials: ak=%v, sk=%v", accessKeyId != "", accessSecret != "")
	}

	config := &OSSConfig{
		Host:         host,
		Bucket:       bucket,
		AccessKeyId:  accessKeyId,
		AccessSecret: accessSecret,
		Prefix:       prefix,
		SessionID:    sessionId,
		Interval:     time.Duration(interval) * time.Millisecond,
		MaxBodySize:  maxBodySize,
		ServerAddr:   serverAddr,
		Timeout:      timeout,
		Mode:         mode,
	}

	addr := &SimplexAddr{
		URL:         u,
		id:          sessionId,
		interval:    config.Interval,
		maxBodySize: maxBodySize,
		options:     u.Query(),
	}
	addr.SetConfig(config)

	return addr, nil
}

// OSSSession represents a client session on the server
type OSSSession struct {
	sessionId  string
	buffer     *AsymBuffer
	addr       *SimplexAddr
	lastActive time.Time
}

func (s *OSSSession) LastActive() time.Time {
	return s.lastActive
}

// OSSServer implements server-side OSS simplex
type OSSServer struct {
	addr       *SimplexAddr
	config     *OSSConfig
	connector  OSSConnector
	sessions   sync.Map // map[sessionId]*OSSSession
	httpServer *http.Server

	ctx    context.Context
	cancel context.CancelFunc
}

// NewOSSServer creates a new OSS server (dispatches by mode)
func NewOSSServer(network, address string) (Simplex, error) {
	addr, err := ResolveOSSAddr(network, address)
	if err != nil {
		return nil, err
	}

	config := addr.Config().(*OSSConfig)

	// Create HTTP client with proxy support
	proxyURL := getOption(addr, "proxy")
	httpClient := createHTTPClient(proxyURL)
	httpClient.Timeout = config.Timeout

	connector := newOSSConnector(config, httpClient)

	// Validate AK/SK on server startup (skip for anonymous access)
	if config.AccessKeyId != "" {
		if err := connector.Validate(); err != nil {
			return nil, fmt.Errorf("OSS credentials validation failed: %v", err)
		}
		logs.Log.Infof("[OSS] Credentials validated successfully for bucket: %s", config.Bucket)
	}

	if config.Mode == "file" {
		return NewOSSFileServer(addr, config, connector)
	}

	// Mirror mode
	return newOSSMirrorServer(addr, config, connector)
}

// newOSSMirrorServer creates a mirror-mode OSS server (with HTTP back-to-origin)
func newOSSMirrorServer(addr *SimplexAddr, config *OSSConfig, connector OSSConnector) (*OSSServer, error) {
	ctx, cancel := context.WithCancel(context.Background())

	server := &OSSServer{
		addr:      addr,
		config:    config,
		connector: connector,
		ctx:       ctx,
		cancel:    cancel,
	}

	// Start HTTP server for mirror back-to-origin
	go server.startHTTPServer()

	// Start session cleanup
	StartSessionCleanup(ctx, &server.sessions, 1*time.Minute, SessionTimeout, "[OSS]")

	return server, nil
}

// startHTTPServer starts HTTP server for mirror back-to-origin
func (s *OSSServer) startHTTPServer() {
	mux := http.NewServeMux()

	// Mirror back-to-origin endpoint: {prefix}*
	mux.HandleFunc(s.config.Prefix, s.handleMirrorRequest)

	s.httpServer = &http.Server{
		Addr:         s.config.ServerAddr,
		Handler:      mux,
		ReadTimeout:  s.config.Timeout,
		WriteTimeout: s.config.Timeout,
	}

	logs.Log.Infof("[OSS] Server HTTP listening on %s for %s*", s.config.ServerAddr, s.config.Prefix)

	if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
		logs.Log.Errorf("[OSS] HTTP server error: %v", err)
	}
}

// handleMirrorRequest handles OSS mirror back-to-origin requests
func (s *OSSServer) handleMirrorRequest(w http.ResponseWriter, r *http.Request) {
	// Extract path: {prefix}{sessionId}/{randomfile} or {prefix}{sessionId}
	path := strings.TrimPrefix(r.URL.Path, s.config.Prefix)
	if path == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Extract sessionId (first part before /)
	var sessionId string
	if idx := strings.Index(path, "/"); idx > 0 {
		// Path like "sessionId/randomfile" - this is client polling for download
		sessionId = path[:idx]
	} else {
		// Path like "sessionId" - should not happen in mirror back-to-origin
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Get or create session
	sessionInterface, exists := s.sessions.Load(sessionId)
	if !exists {
		// Auto-create session and start polling for this session
		addr := generateAddrFromPath(sessionId, s.addr)
		session := &OSSSession{
			sessionId:  sessionId,
			buffer:     NewAsymBuffer(addr),
			addr:       addr,
			lastActive: time.Now(),
		}
		s.sessions.Store(sessionId, session)

		// Start dedicated polling goroutine for this session
		go s.pollSessionFile(session)

		sessionInterface = session
		logs.Log.Infof("[OSS] Auto-created session: %s (from path: %s)", sessionId, r.URL.Path)
	}

	session := sessionInterface.(*OSSSession)

	// Get pending packets from write buffer (data to send to client)
	packets, err := session.buffer.WriteBuf().GetPackets()
	if err != nil || packets.Size() == 0 {
		// Return 200 OK with empty body instead of 204
		// OSS mirror back-to-origin requires 200, otherwise it returns 424
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "0")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Serialize and send
	data := packets.Marshal()

	// Calculate MD5 for OSS mirror back-to-origin validation
	md5Hash := md5.Sum(data)
	md5Base64 := base64.StdEncoding.EncodeToString(md5Hash[:])

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-MD5", md5Base64)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Write(data)
}

// pollSessionFile polls OSS for a specific session's upload file (client to server)
func (s *OSSServer) pollSessionFile(session *OSSSession) {
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	// File path: {prefix}{sessionId} (client uploads to this file)
	ossKey := strings.TrimPrefix(s.config.Prefix, "/") + session.sessionId

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			// Try to get file
			data, err := s.connector.GetObject(ossKey)
			if err != nil {
				// 404 is normal (no upload yet), don't log
				continue
			}

			// Parse packets
			packets, err := ParseSimplexPackets(data)
			if err != nil {
				logs.Log.Errorf("[OSS] Failed to parse packets from %s: %v", ossKey, err)
				s.connector.DeleteObject(ossKey)
				continue
			}

			// Write to read buffer (data received from client)
			if err := session.buffer.ReadBuf().PutPackets(packets); err != nil {
				logs.Log.Errorf("[OSS] Failed to write packets to buffer: %v", err)
			}

			session.lastActive = time.Now()

			// Delete processed file (important!)
			if err := s.connector.DeleteObject(ossKey); err != nil {
				logs.Log.Warnf("[OSS] Failed to delete %s: %v", ossKey, err)
			}
		}
	}
}

// Addr returns the server address
func (s *OSSServer) Addr() *SimplexAddr {
	return s.addr
}

// Receive receives a packet from any session
func (s *OSSServer) Receive() (*SimplexPacket, *SimplexAddr, error) {
	select {
	case <-s.ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
		var foundPkt *SimplexPacket
		var foundAddr *SimplexAddr

		s.sessions.Range(func(key, value interface{}) bool {
			session := value.(*OSSSession)
			// Read from read buffer (data received from client)
			if pkt, err := session.buffer.ReadBuf().GetPacket(); err == nil && pkt != nil {
				foundPkt = pkt
				foundAddr = session.addr
				return false
			}
			return true
		})

		if foundPkt != nil {
			return foundPkt, foundAddr, nil
		}

		return nil, nil, nil
	}
}

// Send sends packets to a specific session
func (s *OSSServer) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	if pkts == nil || pkts.Size() == 0 {
		return 0, nil
	}

	// Extract sessionId from addr.Path
	sessionId := addr.Path
	if sessionId == "" {
		return 0, fmt.Errorf("session id not found in address")
	}

	sessionInterface, exists := s.sessions.Load(sessionId)
	if !exists {
		return 0, fmt.Errorf("session not found: %s", sessionId)
	}

	session := sessionInterface.(*OSSSession)

	// Write to write buffer (data to send to client)
	if err := session.buffer.WriteBuf().PutPackets(pkts); err != nil {
		return 0, err
	}
	return pkts.Size(), nil
}

// Close closes the server
func (s *OSSServer) Close() error {
	s.cancel()

	// Close all session buffers
	s.sessions.Range(func(key, value interface{}) bool {
		session := value.(*OSSSession)
		session.buffer.Close()
		return true
	})

	if s.httpServer != nil {
		return s.httpServer.Close()
	}
	return nil
}

// OSSClient implements client-side OSS simplex
type OSSClient struct {
	addr            *SimplexAddr
	config          *OSSConfig
	connector       OSSConnector
	buffer          *SimplexBuffer
	sessionId       string
	pollingStarted  bool
	pollingStartMux sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
}

// NewOSSClient creates a new OSS client (dispatches by mode)
func NewOSSClient(addr *SimplexAddr) (Simplex, error) {
	config := addr.Config().(*OSSConfig)

	// Create HTTP client with proxy support
	proxyURL := getOption(addr, "proxy")
	httpClient := createHTTPClient(proxyURL)
	httpClient.Timeout = config.Timeout

	connector := newOSSConnector(config, httpClient)

	if config.Mode == "file" {
		return NewOSSFileClient(addr, config, connector)
	}

	// Mirror mode
	return newOSSMirrorClient(addr, config, connector)
}

// newOSSMirrorClient creates a mirror-mode OSS client
func newOSSMirrorClient(addr *SimplexAddr, config *OSSConfig, connector OSSConnector) (*OSSClient, error) {
	ctx, cancel := context.WithCancel(context.Background())

	client := &OSSClient{
		addr:           addr,
		config:         config,
		connector:      connector,
		buffer:         NewSimplexBuffer(addr),
		sessionId:      config.SessionID,
		pollingStarted: false,
		ctx:            ctx,
		cancel:         cancel,
	}

	// Polling will be started after first successful Send
	return client, nil
}

// polling continuously downloads data from OSS mirror back-to-origin
func (c *OSSClient) polling() {
	ticker := time.NewTicker(c.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			// Request random file under session directory to trigger mirror back-to-origin
			// Format: {prefix}{sessionId}/{randomfile}
			randomFile := randomString(8)
			key := strings.TrimPrefix(c.config.Prefix, "/") + c.sessionId + "/" + randomFile

			// Try to get object (will trigger mirror back-to-origin if not exists)
			data, err := c.connector.GetObject(key)
			if err != nil {
				// Error is expected if server has no data
				continue
			}

			// Empty response means no data available
			if len(data) == 0 {
				continue
			}

			// Parse and queue packets
			packets, err := ParseSimplexPackets(data)
			if err != nil {
				logs.Log.Errorf("[OSS] Failed to parse packets: %v", err)
				// Delete invalid file
				c.connector.DeleteObject(key)
				continue
			}

			if len(packets.Packets) > 0 {
				for _, pkt := range packets.Packets {
					c.buffer.PutPacket(pkt)
				}
			}

			// Delete the file after reading (important to prevent re-reading cached data)
			if err := c.connector.DeleteObject(key); err != nil {
				logs.Log.Warnf("[OSS] Failed to delete %s: %v", key, err)
			}
		}
	}
}

// Addr returns the client address
func (c *OSSClient) Addr() *SimplexAddr {
	return c.addr
}

// Receive receives a packet from buffer
func (c *OSSClient) Receive() (*SimplexPacket, *SimplexAddr, error) {
	select {
	case <-c.ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
		pkt, err := c.buffer.GetPacket()
		if err != nil || pkt == nil {
			return nil, c.addr, err
		}
		return pkt, c.addr, err
	}
}

// Send sends packets by uploading to OSS
func (c *OSSClient) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
	}

	if pkts == nil || pkts.Size() == 0 {
		return 0, nil
	}

	// Upload key: {prefix}{sessionId} (file object, not directory)
	key := strings.TrimPrefix(c.config.Prefix, "/") + c.sessionId

	// Check if file exists (use file as lock)
	exists, err := c.connector.HeadObject(key)
	if err != nil {
		return 0, nil // Skip this send, retry next time
	}

	if exists {
		// File exists, means previous data hasn't been read by server yet
		// Skip sending, wait for server to delete the file
		return 0, nil
	}

	// File doesn't exist, safe to upload
	data := pkts.Marshal()
	err = c.connector.PutObject(key, data)
	if err != nil {
		logs.Log.Errorf("[OSS] Failed to upload: %v", err)
		return 0, err
	}

	// Start polling after first successful send
	c.startPollingOnce()

	return len(data), nil
}

// startPollingOnce ensures polling is started only once
func (c *OSSClient) startPollingOnce() {
	c.pollingStartMux.Lock()
	defer c.pollingStartMux.Unlock()

	if !c.pollingStarted {
		c.pollingStarted = true
		go c.polling()
	}
}

// Close closes the client
func (c *OSSClient) Close() error {
	c.cancel()
	return nil
}

// ============================================================================
// File Mode: Pure file-based OSS transport (no HTTP server needed)
// Uses shared FileTransportServer/Client from file_transport.go
// ============================================================================

const (
	ossFileHandlerIdleMultiplier = 150 // 150 × 2s = 5min idle timeout
)

// ossStorageOps implements FileStorageOps using OSSConnector.
type ossStorageOps struct {
	connector OSSConnector
	prefix    string // object key prefix (e.g., "oss/")
}

func newOSSStorageOps(connector OSSConnector, prefix string) FileStorageOps {
	return &ossStorageOps{
		connector: connector,
		prefix:    prefix,
	}
}

func (op *ossStorageOps) ReadFile(path string) ([]byte, error) {
	data, err := op.connector.GetObject(path)
	if err != nil {
		// GetObject already returns os.ErrNotExist for 404
		return nil, err
	}
	return data, nil
}

func (op *ossStorageOps) WriteFile(path string, data []byte) error {
	return op.connector.PutObject(path, data)
}

func (op *ossStorageOps) DeleteFile(path string) error {
	return op.connector.DeleteObject(path)
}

func (op *ossStorageOps) ListFiles(folderPath string) ([]string, error) {
	// OSS uses prefix-based listing; ensure trailing separator
	prefix := folderPath
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	objects, err := op.connector.ListObjects(prefix)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(objects))
	seen := make(map[string]bool)
	for _, obj := range objects {
		name := strings.TrimPrefix(obj.Key, prefix)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

func (op *ossStorageOps) FileExists(path string) (bool, error) {
	exists, err := op.connector.HeadObject(path)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func ossFileTransportConfig() FileTransportConfig {
	return FileTransportConfig{
		SendSuffix:     "_send",
		RecvSuffix:     "_recv",
		IdleMultiplier: ossFileHandlerIdleMultiplier,
		MaxFailures:    10,
		LogPrefix:      "[OSS-File]",
	}
}

// OSSFileServer implements server-side file-based OSS simplex — thin wrapper around FileTransportServer
type OSSFileServer struct {
	addr      *SimplexAddr
	config    *OSSConfig
	connector OSSConnector
	fts       *FileTransportServer
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewOSSFileServer creates a new file-mode OSS server
func NewOSSFileServer(addr *SimplexAddr, config *OSSConfig, connector OSSConnector) (*OSSFileServer, error) {
	ctx, cancel := context.WithCancel(context.Background())

	dirPrefix := strings.TrimPrefix(config.Prefix, "/")
	ops := newOSSStorageOps(connector, dirPrefix)

	cfg := ossFileTransportConfig()
	cfg.Interval = config.Interval
	cfg.MaxBodySize = config.MaxBodySize

	fts := NewFileTransportServer(ops, dirPrefix, cfg, addr, ctx, cancel)
	fts.StartPolling()

	server := &OSSFileServer{
		addr:      addr,
		config:    config,
		connector: connector,
		fts:       fts,
		ctx:       ctx,
		cancel:    cancel,
	}

	return server, nil
}

func (s *OSSFileServer) Addr() *SimplexAddr {
	return s.addr
}

func (s *OSSFileServer) Receive() (*SimplexPacket, *SimplexAddr, error) {
	return s.fts.Receive()
}

func (s *OSSFileServer) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	return s.fts.Send(pkts, addr)
}

func (s *OSSFileServer) Close() error {
	s.fts.CloseAll()
	s.cancel()
	return nil
}

// OSSFileClient implements client-side file-based OSS simplex — thin wrapper around FileTransportClient
type OSSFileClient struct {
	addr      *SimplexAddr
	config    *OSSConfig
	connector OSSConnector
	ftc       *FileTransportClient
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewOSSFileClient creates a new file-mode OSS client
func NewOSSFileClient(addr *SimplexAddr, config *OSSConfig, connector OSSConnector) (*OSSFileClient, error) {
	clientID := getOption(addr, "client_id")
	if clientID == "" {
		clientID = randomString(8)
	}

	dirPrefix := strings.TrimPrefix(config.Prefix, "/")
	ops := newOSSStorageOps(connector, dirPrefix)

	cfg := ossFileTransportConfig()
	cfg.Interval = config.Interval
	cfg.MaxBodySize = config.MaxBodySize

	sendFile := dirPrefix + clientID + "_send"
	recvFile := dirPrefix + clientID + "_recv"

	ctx, cancel := context.WithCancel(context.Background())
	buffer := NewSimplexBuffer(addr)
	ftc := NewFileTransportClient(ops, cfg, buffer, sendFile, recvFile, addr, ctx, cancel)
	ftc.StartMonitoring()

	logs.Log.Infof("[OSS-File] Client created with clientID=%s", clientID)

	client := &OSSFileClient{
		addr:      addr,
		config:    config,
		connector: connector,
		ftc:       ftc,
		ctx:       ctx,
		cancel:    cancel,
	}

	return client, nil
}

func (c *OSSFileClient) Addr() *SimplexAddr {
	return c.addr
}

func (c *OSSFileClient) Receive() (*SimplexPacket, *SimplexAddr, error) {
	return c.ftc.Receive()
}

func (c *OSSFileClient) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	return c.ftc.Send(pkts, addr)
}

func (c *OSSFileClient) Close() error {
	c.cancel()
	return nil
}
