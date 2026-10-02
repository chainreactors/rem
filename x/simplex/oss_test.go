//go:build oss

package simplex

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/chainreactors/rem/x/arq"
)

// --- Unit Tests ---

func TestResolveOSSAddr_FileMode(t *testing.T) {
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?ak=test-ak&sk=test-sk&mode=file")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config := addr.Config().(*OSSConfig)
	if config.Mode != "file" {
		t.Fatalf("expected mode=file, got %q", config.Mode)
	}
	if config.AccessKeyId != "test-ak" {
		t.Fatalf("expected ak=test-ak, got %q", config.AccessKeyId)
	}
}

func TestResolveOSSAddr_MirrorMode(t *testing.T) {
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?ak=test-ak&sk=test-sk&mode=mirror&server=:9090")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config := addr.Config().(*OSSConfig)
	if config.Mode != "mirror" {
		t.Fatalf("expected mode=mirror, got %q", config.Mode)
	}
	if config.ServerAddr != ":9090" {
		t.Fatalf("expected server=:9090, got %q", config.ServerAddr)
	}
}

func TestResolveOSSAddr_AutoDetectMode(t *testing.T) {
	// With server= parameter, should auto-detect mirror
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?ak=test&sk=test&server=:8080")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config := addr.Config().(*OSSConfig)
	if config.Mode != "mirror" {
		t.Fatalf("expected auto-detect mode=mirror when server= present, got %q", config.Mode)
	}

	// Without server= parameter, should default to file
	addr2, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?ak=test&sk=test")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config2 := addr2.Config().(*OSSConfig)
	if config2.Mode != "file" {
		t.Fatalf("expected default mode=file without server=, got %q", config2.Mode)
	}
}

func TestResolveOSSAddr_Constants(t *testing.T) {
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config := addr.Config().(*OSSConfig)

	// Verify updated constants
	if config.Interval.Milliseconds() != 2000 {
		t.Fatalf("expected default interval=2000ms, got %dms", config.Interval.Milliseconds())
	}
	if config.MaxBodySize != 1024*1024 {
		t.Fatalf("expected MaxBodySize=1MB, got %d", config.MaxBodySize)
	}
}

func TestResolveOSSAddr_PathPrefixParsing(t *testing.T) {
	tests := []struct {
		raw         string
		prefix      string
		hasAutoID   bool
		fixedSessID string
	}{
		{"oss://bucket.oss-cn-hangzhou.aliyuncs.com/", "/", true, ""},
		{"oss://bucket.oss-cn-hangzhou.aliyuncs.com/dir/", "/dir/", true, ""},
		{"oss://bucket.oss-cn-hangzhou.aliyuncs.com/dir/sub/", "/dir/sub/", true, ""},
		{"oss://bucket.oss-cn-hangzhou.aliyuncs.com/session", "/", false, "session"},
		{"oss://bucket.oss-cn-hangzhou.aliyuncs.com/dir/session", "/dir/", false, "session"},
		{"oss://bucket.oss-cn-hangzhou.aliyuncs.com/?prefix=dir/sub", "/dir/sub/", true, ""},
		{"oss://bucket.oss-cn-hangzhou.aliyuncs.com/session?prefix=/dir/sub/", "/dir/sub/", false, "session"},
		{"oss://bucket.oss-cn-hangzhou.aliyuncs.com/dir/?prefix=other", "/other/", true, ""},
	}

	for _, tc := range tests {
		addr, err := ResolveOSSAddr("oss", tc.raw)
		if err != nil {
			t.Fatalf("ResolveOSSAddr(%s): %v", tc.raw, err)
		}
		config := addr.Config().(*OSSConfig)
		if config.Prefix != tc.prefix {
			t.Fatalf("url=%s: prefix=%q, want %q", tc.raw, config.Prefix, tc.prefix)
		}
		if tc.hasAutoID && len(config.SessionID) != 8 {
			t.Fatalf("url=%s: auto session id=%q, want 8 chars", tc.raw, config.SessionID)
		}
		if !tc.hasAutoID && config.SessionID == "" {
			t.Fatalf("url=%s: expected fixed session id", tc.raw)
		}
		if tc.fixedSessID != "" && config.SessionID != tc.fixedSessID {
			t.Fatalf("url=%s: session id=%q, want %q", tc.raw, config.SessionID, tc.fixedSessID)
		}
	}
}

func TestResolveOSSAddr_AutoARQConfig(t *testing.T) {
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?interval=100&timeout=30")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	cfg := addr.ARQConfig()

	if cfg.RTO != 5000 {
		t.Fatalf("RTO = %dms, want 5000ms", cfg.RTO)
	}
	if cfg.MTU != arq.ARQ_MAX_MTU {
		t.Fatalf("MTU = %d, want %d", cfg.MTU, arq.ARQ_MAX_MTU)
	}
	if cfg.WndSize != 256 {
		t.Fatalf("WndSize = %d, want 256", cfg.WndSize)
	}
	if cfg.StandaloneAckSegments != 16 {
		t.Fatalf("StandaloneAckSegments = %d, want 16", cfg.StandaloneAckSegments)
	}
	if cfg.MaxRetransmissions != 20 {
		t.Fatalf("MaxRetransmissions = %d, want 20", cfg.MaxRetransmissions)
	}
}

func TestResolveOSSAddr_AutoSimplexConfig(t *testing.T) {
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?interval=100&seq=true")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}

	sc := addr.SimplexConfig()
	if sc.ItemsPerCycle != seqMaxOpsPerTick {
		t.Fatalf("ItemsPerCycle = %d, want %d", sc.ItemsPerCycle, seqMaxOpsPerTick)
	}
	if sc.DataBudget() != MaxOSSMessageSize*seqMaxOpsPerTick {
		t.Fatalf("DataBudget = %d, want %d", sc.DataBudget(), MaxOSSMessageSize*seqMaxOpsPerTick)
	}
}

func TestNormalizeOSSConfig_Variants(t *testing.T) {
	tests := []struct {
		name  string
		input OSSConfig
		check func(t *testing.T, cfg *OSSConfig)
	}{
		{
			name:  "default 1MB",
			input: OSSConfig{Interval: 100 * time.Millisecond},
			check: func(t *testing.T, cfg *OSSConfig) {
				if cfg.ARQ.MTU != arq.ARQ_MAX_MTU {
					t.Fatalf("MTU = %d, want %d", cfg.ARQ.MTU, arq.ARQ_MAX_MTU)
				}
				if cfg.ARQ.WndSize != 256 {
					t.Fatalf("WndSize = %d, want 256", cfg.ARQ.WndSize)
				}
				if cfg.ARQ.RTO != 5000 {
					t.Fatalf("RTO = %d, want 5000", cfg.ARQ.RTO)
				}
				if cfg.ARQ.MaxRetransmissions != 20 {
					t.Fatalf("MaxRetransmissions = %d, want 20", cfg.ARQ.MaxRetransmissions)
				}
			},
		},
		{
			name:  "small 64KB body",
			input: OSSConfig{MaxBodySize: 64 * 1024, Interval: 100 * time.Millisecond},
			check: func(t *testing.T, cfg *OSSConfig) {
				if cfg.ARQ.WndSize < 64 {
					t.Fatalf("WndSize = %d, want >= 64 for 64KB body", cfg.ARQ.WndSize)
				}
			},
		},
		{
			name:  "preset ARQ not overwritten",
			input: OSSConfig{Interval: 100 * time.Millisecond, ARQ: arq.ARQConfig{WndSize: 32, MTU: 1000, RTO: 2000}},
			check: func(t *testing.T, cfg *OSSConfig) {
				if cfg.ARQ.WndSize != 32 {
					t.Fatalf("WndSize = %d, want 32 (preset)", cfg.ARQ.WndSize)
				}
				if cfg.ARQ.MTU != 1000 {
					t.Fatalf("MTU = %d, want 1000 (preset)", cfg.ARQ.MTU)
				}
				if cfg.ARQ.RTO != 2000 {
					t.Fatalf("RTO = %d, want 2000 (preset)", cfg.ARQ.RTO)
				}
			},
		},
		{
			name:  "seq mode itemsPerCycle",
			input: OSSConfig{Interval: 100 * time.Millisecond, SequenceMode: true},
			check: func(t *testing.T, cfg *OSSConfig) {
				// SequenceMode only affects itemsPerCycle via ResolveOSSAddr, not normalizeOSSConfig
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.input
			normalizeOSSConfig(&cfg)
			if cfg.ARQ.MTU <= 0 || cfg.ARQ.WndSize <= 0 || cfg.ARQ.RTO <= 0 || cfg.ARQ.MaxRetransmissions <= 0 {
				t.Fatalf("normalize left zero fields: MTU=%d WndSize=%d RTO=%d MaxRetrans=%d",
					cfg.ARQ.MTU, cfg.ARQ.WndSize, cfg.ARQ.RTO, cfg.ARQ.MaxRetransmissions)
			}
			tt.check(t, &cfg)
		})
	}
}

func TestSimplexBuffer_QueueCapScalesWithARQWindow(t *testing.T) {
	config := &OSSConfig{Interval: 100 * time.Millisecond}
	normalizeOSSConfig(config)

	u, _ := url.Parse("oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/")
	addr := &SimplexAddr{
		URL:         u,
		interval:    config.Interval,
		maxBodySize: config.MaxBodySize,
		options:     u.Query(),
	}
	addr.SetConfig(config)

	buf := NewSimplexBuffer(addr)
	wndSize := config.ARQ.WndSize
	if buf.dataCap < wndSize {
		t.Fatalf("sendCap (%d) < ARQ WndSize (%d)", buf.dataCap, wndSize)
	}
	for i := 0; i < wndSize*2; i++ {
		if err := buf.recvBuf.Put([]byte{1}); err != nil {
			t.Fatalf("recvCap should follow ARQ window, failed at %d/%d: %v", i, wndSize*2, err)
		}
	}
}

func TestMockListObjects_PrefixFilter(t *testing.T) {
	mock := newMockOSSConnector()

	// Put objects with different prefixes
	mock.PutObject("test/client1_send", []byte("data1"))
	mock.PutObject("test/client2_send", []byte("data2"))
	mock.PutObject("test/client1_recv", []byte("data3"))
	mock.PutObject("other/client3_send", []byte("data4"))

	// List with prefix "test/"
	objects, err := mock.ListObjects("test/")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}

	// Should find 3 objects (test/ prefix only)
	if len(objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(objects))
	}

	// List with prefix "other/"
	objects2, err := mock.ListObjects("other/")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(objects2) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objects2))
	}
}

// ============================================================
// Boundary Tests
// ============================================================

// TestOSSFile_NetworkDisconnect tests network failure and recovery.
func TestOSSFile_NetworkDisconnect(t *testing.T) {
	server, client, mock := ossFileTestSetup(t)

	// Phase 1: Normal communication
	msg1 := []byte("before-disconnect")
	client.Send(sendPacket(msg1), client.Addr())
	pkt1, _ := ossFilePollServerUntilReceived(t, server, 5*time.Second)
	if string(pkt1.Data) != string(msg1) {
		t.Fatalf("phase 1: got %q, want %q", pkt1.Data, msg1)
	}

	// Phase 2: Simulate network disconnect
	mock.setFault(true, true)

	// Send during disconnect (should be queued in pendingData)
	msg2 := []byte("during-disconnect")
	client.Send(sendPacket(msg2), client.Addr())

	// Wait a few polling cycles
	time.Sleep(300 * time.Millisecond)

	// Phase 3: Recover network
	mock.setFault(false, false)

	// Data sent during disconnect should eventually arrive
	pkt2, _ := ossFilePollServerUntilReceived(t, server, 10*time.Second)
	if string(pkt2.Data) != string(msg2) {
		t.Fatalf("phase 3: got %q, want %q", pkt2.Data, msg2)
	}
}

// TestOSSFile_ConsecutiveFailureShutdown tests auto-close after too many failures.
func TestOSSFile_ConsecutiveFailureShutdown(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	config.Interval = 20 * time.Millisecond // fast polling for test speed

	addr := ossFileTestAddr(config)
	client, err := NewOSSFileClient(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Set permanent failure
	mock.setFault(true, false)

	// Send data to trigger consecutive failures
	client.Send(sendPacket([]byte("will-fail")), client.Addr())

	// Wait for maxConsecutiveFailures (10) to trigger shutdown
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-client.ctx.Done():
			// Client shut down as expected
			return
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatal("client did not shut down after consecutive failures")
}

// TestOSSFile_ServerRestartMidTransfer tests server restart during data transfer.
func TestOSSFile_ServerRestartMidTransfer(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server1, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}

	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	clientAddr.options.Set("client_id", "mid-transfer")
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Send multiple messages rapidly
	for i := 0; i < 3; i++ {
		msg := []byte(fmt.Sprintf("msg-%d", i))
		client.Send(sendPacket(msg), client.Addr())
	}

	// Receive first message on server1
	pkt, _ := ossFilePollServerUntilReceived(t, server1, 5*time.Second)
	t.Logf("server1 received: %s", pkt.Data)

	// Kill server1 mid-transfer
	server1.Close()

	// Start server2
	server2, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer (restart): %v", err)
	}
	defer server2.Close()

	// Send a new message and verify server2 receives it
	newMsg := []byte("after-mid-restart")
	client.Send(sendPacket(newMsg), client.Addr())

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pkt, _, err := server2.Receive()
		if err != nil {
			t.Fatalf("server2.Receive: %v", err)
		}
		if pkt != nil && string(pkt.Data) == string(newMsg) {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server2 did not receive message after restart")
}

// TestOSSFile_ClientRestartWithPendingData tests client restart with queued data.
func TestOSSFile_ClientRestartWithPendingData(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	// Create client and queue data
	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	clientAddr.options.Set("client_id", "pending-test")
	client1, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}

	// Prevent send file from being uploaded (simulate slow network)
	mock.setFault(true, false)
	client1.Send(sendPacket([]byte("pending-data")), client1.Addr())
	time.Sleep(100 * time.Millisecond) // let monitoring try and fail

	// Close client (pendingData is lost)
	client1.Close()
	mock.setFault(false, false)

	// Create new client and verify it can communicate
	clientAddr2 := ossFileTestAddr(&clientConfig)
	clientAddr2.options.Set("client_id", "pending-test-v2")
	client2, err := NewOSSFileClient(clientAddr2, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient v2: %v", err)
	}
	defer client2.Close()

	msg := []byte("fresh-data")
	client2.Send(sendPacket(msg), client2.Addr())
	pkt, _ := ossFilePollServerUntilReceived(t, server, 10*time.Second)
	if string(pkt.Data) != string(msg) {
		t.Fatalf("got %q, want %q", pkt.Data, msg)
	}
}

// TestOSSFile_HighLatency tests communication with high latency.
func TestOSSFile_HighLatency(t *testing.T) {
	mock := newMockOSSConnector()
	mock.latency = 500 * time.Millisecond // 500ms per operation

	config := ossFileTestConfig()
	config.Interval = 1 * time.Second // slower polling to accommodate latency
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Send and receive with high latency
	msg := []byte("high-latency-data")
	client.Send(sendPacket(msg), client.Addr())

	pkt, addr2 := ossFilePollServerUntilReceived(t, server, 30*time.Second)
	if string(pkt.Data) != string(msg) {
		t.Fatalf("got %q, want %q", pkt.Data, msg)
	}

	reply := []byte("high-latency-reply")
	server.Send(sendPacket(reply), addr2)

	pkt2 := ossFilePollClientUntilReceived(t, client, 30*time.Second)
	if string(pkt2.Data) != string(reply) {
		t.Fatalf("got %q, want %q", pkt2.Data, reply)
	}
}

// TestOSSFile_HighPacketLoss tests communication with 30% loss rate.
func TestOSSFile_HighPacketLoss(t *testing.T) {
	mock := newMockOSSConnector()
	mock.lossRate = 0.3

	config := ossFileTestConfig()
	config.Interval = 50 * time.Millisecond
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// With 30% loss, data should eventually arrive via retries
	msg := []byte("lossy-data")
	client.Send(sendPacket(msg), client.Addr())

	pkt, _ := ossFilePollServerUntilReceived(t, server, 30*time.Second)
	if string(pkt.Data) != string(msg) {
		t.Fatalf("got %q, want %q", pkt.Data, msg)
	}
}

// TestOSSFile_BurstSend tests sending 10 messages in rapid succession.
func TestOSSFile_BurstSend(t *testing.T) {
	server, client, _ := ossFileTestSetup(t)

	const burstCount = 10
	// Send burst
	for i := 0; i < burstCount; i++ {
		msg := []byte(fmt.Sprintf("burst-%d", i))
		client.Send(sendPacket(msg), client.Addr())
	}

	// Collect all messages
	received := make(map[string]bool)
	deadline := time.Now().Add(15 * time.Second)
	for len(received) < burstCount && time.Now().Before(deadline) {
		pkt, _, err := server.Receive()
		if err != nil {
			t.Fatalf("server.Receive: %v", err)
		}
		if pkt != nil {
			received[string(pkt.Data)] = true
		}
		time.Sleep(10 * time.Millisecond)
	}

	for i := 0; i < burstCount; i++ {
		key := fmt.Sprintf("burst-%d", i)
		if !received[key] {
			t.Errorf("missing burst message: %s", key)
		}
	}
}

// TestOSSFile_MaxBodySizeRespect tests server-side outBuffer chunking respects MaxBodySize.
func TestOSSFile_MaxBodySizeRespect(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	config.MaxBodySize = 512 // Small limit to force chunking on server side
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	clientConfig := *config
	clientConfig.MaxBodySize = MaxOSSMessageSize // Client uses large limit (no split issue)
	clientAddr := ossFileTestAddr(&clientConfig)
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Client sends one message, server receives, then sends large reply
	msg := []byte("request")
	client.Send(sendPacket(msg), client.Addr())
	_, srvAddr := ossFilePollServerUntilReceived(t, server, 5*time.Second)

	// Server sends multiple packets that exceed MaxBodySize
	// handleClient will chunk them across multiple recv files
	for i := 0; i < 5; i++ {
		data := make([]byte, 200)
		for j := range data {
			data[j] = byte(i)
		}
		server.Send(sendPacket(data), srvAddr)
	}

	// Client should receive all packets eventually (server sends them in chunks)
	received := 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pkt, _, err := client.Receive()
		if err != nil {
			t.Fatalf("client.Receive: %v", err)
		}
		if pkt != nil {
			received++
		}
		if received >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if received < 5 {
		t.Fatalf("received %d packets, want 5", received)
	}
}

// ============================================================
// OSS Semantics Tests
// ============================================================

func TestOSSMirrorServerSend_MissingSessionReturnsClosedPipe(t *testing.T) {
	config := &OSSConfig{
		Host:        "mock.oss.com",
		Bucket:      "test",
		Prefix:      "/test/",
		SessionID:   "server",
		Interval:    50 * time.Millisecond,
		MaxBodySize: MaxOSSMessageSize,
		Timeout:     5 * time.Second,
		ServerAddr:  "127.0.0.1:0",
		Mode:        "mirror",
	}

	u, _ := url.Parse("oss://mock.oss.com/test/")
	addr := &SimplexAddr{
		URL:         u,
		id:          "test",
		interval:    config.Interval,
		maxBodySize: config.MaxBodySize,
		options:     url.Values{},
	}
	addr.SetConfig(config)

	server, err := newOSSMirrorServer(addr, config, newMockOSSConnector())
	if err != nil {
		t.Fatalf("newOSSMirrorServer: %v", err)
	}
	defer server.Close()

	clientAddr := generateAddrFromPath("missing-session", addr)
	if _, err := server.Send(sendPacket([]byte("server-msg")), clientAddr); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Send error = %v, want %v", err, io.ErrClosedPipe)
	}
}

// ============================================================
// E2E Tests
// ============================================================

// --- Test helpers ---

func ossFileTestConfig() *OSSConfig {
	return &OSSConfig{
		Host:        "mock.oss.com",
		Bucket:      "test",
		Prefix:      "/test/",
		Interval:    50 * time.Millisecond,
		MaxBodySize: MaxOSSMessageSize,
		Timeout:     5 * time.Second,
		Mode:        "file",
	}
}

func ossFileTestAddr(config *OSSConfig) *SimplexAddr {
	u, _ := url.Parse("oss://mock.oss.com/test/")
	addr := &SimplexAddr{
		URL:         u,
		id:          "test",
		interval:    config.Interval,
		maxBodySize: config.MaxBodySize,
		options:     url.Values{},
	}
	addr.SetConfig(config)
	return addr
}

func ossFileTestSetup(t *testing.T) (*OSSFileServer, *OSSFileClient, *mockOSSConnector) {
	t.Helper()
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}

	clientConfig := *config // copy
	clientAddr := ossFileTestAddr(&clientConfig)
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}

	t.Cleanup(func() {
		client.Close()
		server.Close()
	})

	return server, client, mock
}

func ossFilePollServerUntilReceived(t *testing.T, server *OSSFileServer, timeout time.Duration) (*SimplexPacket, *SimplexAddr) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pkt, addr, err := server.Receive()
		if err != nil {
			t.Fatalf("server.Receive error: %v", err)
		}
		if pkt != nil {
			return pkt, addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for server to receive packet")
	return nil, nil
}

func ossFilePollClientUntilReceived(t *testing.T, client *OSSFileClient, timeout time.Duration) *SimplexPacket {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pkt, _, err := client.Receive()
		if err != nil {
			t.Fatalf("client.Receive error: %v", err)
		}
		if pkt != nil {
			return pkt
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for client to receive packet")
	return nil
}

func sendPacket(data []byte) *SimplexPackets {
	return &SimplexPackets{
		Packets: []*SimplexPacket{
			{PacketType: SimplexPacketTypeDATA, Data: data},
		},
	}
}

func TestOSSFile_ServerClientFullRoundTrip(t *testing.T) {
	server, client, _ := ossFileTestSetup(t)

	// Client → Server
	msg := []byte("hello from client")
	if _, err := client.Send(sendPacket(msg), client.Addr()); err != nil {
		t.Fatalf("client.Send: %v", err)
	}

	pkt, addr := ossFilePollServerUntilReceived(t, server, 5*time.Second)
	if string(pkt.Data) != string(msg) {
		t.Fatalf("server received wrong data: got %q, want %q", pkt.Data, msg)
	}

	// Server → Client
	reply := []byte("hello from server")
	if _, err := server.Send(sendPacket(reply), addr); err != nil {
		t.Fatalf("server.Send: %v", err)
	}

	pkt2 := ossFilePollClientUntilReceived(t, client, 5*time.Second)
	if string(pkt2.Data) != string(reply) {
		t.Fatalf("client received wrong data: got %q, want %q", pkt2.Data, reply)
	}
}

func TestOSSFile_MultipleClients(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	const numClients = 3
	clients := make([]*OSSFileClient, numClients)
	for i := 0; i < numClients; i++ {
		clientConfig := *config
		clientAddr := ossFileTestAddr(&clientConfig)
		clientAddr.options.Set("client_id", fmt.Sprintf("client%d", i))
		c, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
		if err != nil {
			t.Fatalf("NewOSSFileClient[%d]: %v", i, err)
		}
		clients[i] = c
		defer c.Close()
	}

	// Each client sends a unique message
	for i, c := range clients {
		msg := []byte(fmt.Sprintf("msg-from-client-%d", i))
		if _, err := c.Send(sendPacket(msg), c.Addr()); err != nil {
			t.Fatalf("client[%d].Send: %v", i, err)
		}
	}

	// Server should receive all messages
	received := make(map[string]bool)
	deadline := time.Now().Add(10 * time.Second)
	for len(received) < numClients && time.Now().Before(deadline) {
		pkt, _, err := server.Receive()
		if err != nil {
			t.Fatalf("server.Receive error: %v", err)
		}
		if pkt != nil {
			received[string(pkt.Data)] = true
		}
		time.Sleep(10 * time.Millisecond)
	}

	for i := 0; i < numClients; i++ {
		key := fmt.Sprintf("msg-from-client-%d", i)
		if !received[key] {
			t.Errorf("server did not receive message from client %d", i)
		}
	}
}

func TestOSSFile_BidirectionalMultiRound(t *testing.T) {
	server, client, _ := ossFileTestSetup(t)

	for round := 0; round < 5; round++ {
		// Client → Server
		c2s := []byte(fmt.Sprintf("c2s-round-%d", round))
		if _, err := client.Send(sendPacket(c2s), client.Addr()); err != nil {
			t.Fatalf("round %d: client.Send: %v", round, err)
		}

		pkt, addr := ossFilePollServerUntilReceived(t, server, 5*time.Second)
		if string(pkt.Data) != string(c2s) {
			t.Fatalf("round %d: server got %q, want %q", round, pkt.Data, c2s)
		}

		// Server → Client
		s2c := []byte(fmt.Sprintf("s2c-round-%d", round))
		if _, err := server.Send(sendPacket(s2c), addr); err != nil {
			t.Fatalf("round %d: server.Send: %v", round, err)
		}

		pkt2 := ossFilePollClientUntilReceived(t, client, 5*time.Second)
		if string(pkt2.Data) != string(s2c) {
			t.Fatalf("round %d: client got %q, want %q", round, pkt2.Data, s2c)
		}
	}
}

func TestOSSFile_LargePayload(t *testing.T) {
	server, client, _ := ossFileTestSetup(t)

	// 100KB payload
	payload := make([]byte, 100*1024)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, client.Addr().MaxBodySize())
	if _, err := client.Send(pkts, client.Addr()); err != nil {
		t.Fatalf("client.Send: %v", err)
	}

	// Collect all packets
	var received []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pkt, _, err := server.Receive()
		if err != nil {
			t.Fatalf("server.Receive error: %v", err)
		}
		if pkt != nil {
			received = append(received, pkt.Data...)
		}
		if len(received) >= len(payload) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(received) != len(payload) {
		t.Fatalf("received %d bytes, want %d", len(received), len(payload))
	}
	for i := range payload {
		if received[i] != payload[i] {
			t.Fatalf("data mismatch at byte %d: got %d, want %d", i, received[i], payload[i])
		}
	}
}

func TestOSSFile_ServerRestart(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server1, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}

	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	clientAddr.options.Set("client_id", "stable-client")
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Phase 1: Normal communication
	msg1 := []byte("before-restart")
	client.Send(sendPacket(msg1), client.Addr())
	pkt1, _ := ossFilePollServerUntilReceived(t, server1, 5*time.Second)
	if string(pkt1.Data) != string(msg1) {
		t.Fatalf("phase 1: got %q, want %q", pkt1.Data, msg1)
	}

	// Phase 2: Restart server
	server1.Close()

	server2, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer (restart): %v", err)
	}
	defer server2.Close()

	// Phase 3: Communication should recover
	msg2 := []byte("after-restart")
	client.Send(sendPacket(msg2), client.Addr())
	pkt2, _ := ossFilePollServerUntilReceived(t, server2, 10*time.Second)
	if string(pkt2.Data) != string(msg2) {
		t.Fatalf("phase 3: got %q, want %q", pkt2.Data, msg2)
	}
}

func TestOSSFile_ClientRestart(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	// Phase 1: Create client1 and communicate
	clientConfig := *config
	clientAddr1 := ossFileTestAddr(&clientConfig)
	clientAddr1.options.Set("client_id", "client-v1")
	client1, err := NewOSSFileClient(clientAddr1, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient v1: %v", err)
	}

	msg1 := []byte("from-client-v1")
	client1.Send(sendPacket(msg1), client1.Addr())
	pkt1, _ := ossFilePollServerUntilReceived(t, server, 5*time.Second)
	if string(pkt1.Data) != string(msg1) {
		t.Fatalf("phase 1: got %q, want %q", pkt1.Data, msg1)
	}

	// Phase 2: Close client1, create client2 (different clientID)
	client1.Close()

	clientAddr2 := ossFileTestAddr(&clientConfig)
	clientAddr2.options.Set("client_id", "client-v2")
	client2, err := NewOSSFileClient(clientAddr2, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient v2: %v", err)
	}
	defer client2.Close()

	// Phase 3: Communication with new client
	msg2 := []byte("from-client-v2")
	client2.Send(sendPacket(msg2), client2.Addr())
	pkt2, _ := ossFilePollServerUntilReceived(t, server, 10*time.Second)
	if string(pkt2.Data) != string(msg2) {
		t.Fatalf("phase 3: got %q, want %q", pkt2.Data, msg2)
	}
}

// ============================================================
// OSS Registry Matrix Tests
// ============================================================

func TestRegisteredOSSSimplexResolvers(t *testing.T) {
	const scheme = "oss"
	const address = "oss://bucket.oss-cn-shanghai.aliyuncs.com/rem?bucket=test-bucket&server=:8080&interval=1000&wrapper=raw"

	if _, err := GetSimplexClient(scheme); err != nil {
		t.Fatalf("GetSimplexClient(%q) error: %v", scheme, err)
	}
	if _, err := GetSimplexServer(scheme); err != nil {
		t.Fatalf("GetSimplexServer(%q) error: %v", scheme, err)
	}

	addr, err := ResolveSimplexAddr(scheme, address)
	if err != nil {
		t.Fatalf("ResolveSimplexAddr(%q) error: %v", scheme, err)
	}
	if got := addr.Network(); got != scheme {
		t.Fatalf("unexpected simplex addr network: got %q want %q", got, scheme)
	}
}
