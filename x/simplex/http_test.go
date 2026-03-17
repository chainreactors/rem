package simplex

import (
	"crypto/sha256"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// ============================================================
// HTTP Simplex Transport Tests — 带宽、并发、双向通信
//
// 运行:
//   go test -v -run "TestHTTP_" ./x/simplex/ -timeout 120s
// ============================================================

// --- 共享测试辅助函数 ---

func httpFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to get free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func httpServerURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/?interval=50&max=%d", port, 1024*1024)
}

// httpWarmupWait waits for the HTTP server to accept TCP connections.
func httpWarmupWait(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("HTTP server at %s did not become ready within %v", addr, timeout)
}

// httpSetup creates an HTTP server + client pair for testing.
func httpSetup(t *testing.T) (*HttpServer, *HTTPClient, *SimplexAddr) {
	t.Helper()
	port := httpFreePort(t)
	serverURL := httpServerURL(port)

	server, err := NewHTTPServer("http", serverURL)
	if err != nil {
		t.Fatalf("NewHTTPServer error: %v", err)
	}
	t.Cleanup(func() { server.Close() })

	serverAddr := fmt.Sprintf("127.0.0.1:%d", port)
	httpWarmupWait(t, serverAddr, 3*time.Second)

	clientAddr, err := ResolveHTTPAddr("http", serverURL)
	if err != nil {
		t.Fatalf("ResolveHTTPAddr error: %v", err)
	}
	client, err := NewHTTPClient(clientAddr)
	if err != nil {
		t.Fatalf("NewHTTPClient error: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return server, client, clientAddr
}

// httpSendData sends a string message from client to server.
func httpSendData(t *testing.T, client *HTTPClient, addr *SimplexAddr, msg string) {
	t.Helper()
	pkts := NewSimplexPacketWithMaxSize([]byte(msg), SimplexPacketTypeDATA, addr.maxBodySize)
	if _, err := client.Send(pkts, addr); err != nil {
		t.Fatalf("Send error: %v", err)
	}
}

// httpPollServerUntilReceived polls server until a data packet arrives.
func httpPollServerUntilReceived(t *testing.T, server *HttpServer, timeout time.Duration) (*SimplexPacket, *SimplexAddr) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pkt, addr, err := server.Receive()
		if err != nil {
			t.Fatalf("Server.Receive error: %v", err)
		}
		if pkt != nil {
			return pkt, addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Timeout waiting for server to receive data")
	return nil, nil
}

// httpPollClientUntilReceived polls client until a data packet arrives.
func httpPollClientUntilReceived(t *testing.T, client *HTTPClient, timeout time.Duration) *SimplexPacket {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pkt, _, err := client.Receive()
		if err != nil {
			t.Fatalf("Client.Receive error: %v", err)
		}
		if pkt != nil {
			return pkt
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Timeout waiting for client to receive response")
	return nil
}

// ============================================================
// 1. Basic E2E
// ============================================================

func TestHTTP_E2E_BasicRoundtrip(t *testing.T) {
	server, client, addr := httpSetup(t)

	// Client → Server
	httpSendData(t, client, addr, "hello-http")
	pkt, recvAddr := httpPollServerUntilReceived(t, server, 5*time.Second)
	if string(pkt.Data) != "hello-http" {
		t.Fatalf("Expected %q, got %q", "hello-http", string(pkt.Data))
	}

	// Server → Client
	respPkts := NewSimplexPacketWithMaxSize([]byte("reply-http"), SimplexPacketTypeDATA, addr.maxBodySize)
	server.Send(respPkts, recvAddr)

	respPkt := httpPollClientUntilReceived(t, client, 5*time.Second)
	if string(respPkt.Data) != "reply-http" {
		t.Fatalf("Expected %q, got %q", "reply-http", string(respPkt.Data))
	}
	t.Log("PASS: Basic roundtrip")
}

// ============================================================
// 2. Bandwidth Tests
// ============================================================

// TestHTTP_Bandwidth_Uplink Client→Server 上行带宽
func TestHTTP_Bandwidth_Uplink(t *testing.T) {
	server, client, addr := httpSetup(t)

	payloadSize := addr.maxBodySize - 5 // 减去 TLV header
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i%254 + 1)
	}

	t.Logf("上行带宽测试: payload=%d bytes, maxBodySize=%d", payloadSize, addr.maxBodySize)

	var mu sync.Mutex
	totalSent, totalRecv, totalRecvBytes := 0, 0, 0
	done := make(chan struct{})
	testDuration := 10 * time.Second

	// 发送 goroutine
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, addr.maxBodySize)
				if _, err := client.Send(pkts, addr); err == nil {
					mu.Lock()
					totalSent++
					mu.Unlock()
				}
			}
		}
	}()

	// 接收 goroutine
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := server.Receive()
				if pkt != nil {
					mu.Lock()
					totalRecv++
					totalRecvBytes += len(pkt.Data)
					mu.Unlock()
				} else {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}
	}()

	// 进度报告
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				mu.Lock()
				t.Logf("  进度: sent=%d recv=%d bytes=%.2f KB", totalSent, totalRecv, float64(totalRecvBytes)/1024)
				mu.Unlock()
			}
		}
	}()

	time.Sleep(testDuration)
	close(done)
	time.Sleep(500 * time.Millisecond)
	close(progressDone)

	mu.Lock()
	defer mu.Unlock()

	lossRate := 0.0
	if totalSent > 0 {
		lossRate = float64(totalSent-totalRecv) / float64(totalSent) * 100
	}
	bps := float64(totalRecvBytes) / testDuration.Seconds()

	t.Logf("========== 上行带宽 (Client→Server) ==========")
	t.Logf("载荷: %d bytes/msg, 间隔: 5ms", payloadSize)
	t.Logf("发送: %d 条, 接收: %d 条, 丢包: %.1f%%", totalSent, totalRecv, lossRate)
	t.Logf("总字节: %d (%.2f KB)", totalRecvBytes, float64(totalRecvBytes)/1024)
	t.Logf("带宽: %.0f B/s (%.2f KB/s, %.4f Mbps)", bps, bps/1024, bps*8/1000000)

	if totalRecv == 0 {
		t.Error("No messages received")
	}
}

// TestHTTP_Bandwidth_Downlink Server→Client 下行带宽
func TestHTTP_Bandwidth_Downlink(t *testing.T) {
	server, client, addr := httpSetup(t)

	// 先建立连接（发送一条消息创建 buffer）
	httpSendData(t, client, addr, "init")
	_, recvAddr := httpPollServerUntilReceived(t, server, 5*time.Second)

	payloadSize := addr.maxBodySize - 5
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i%254 + 1)
	}

	t.Logf("下行带宽测试: payload=%d bytes", payloadSize)

	var mu sync.Mutex
	totalSent, totalRecv, totalRecvBytes := 0, 0, 0
	done := make(chan struct{})
	testDuration := 10 * time.Second

	// Server 持续填充 WriteBuf
	go func() {
		ticker := time.NewTicker(3 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, addr.maxBodySize)
				server.Send(pkts, recvAddr)
				mu.Lock()
				totalSent++
				mu.Unlock()
			}
		}
	}()

	// Client 接收（Receive 内部会 poll 空 POST 来取数据）
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := client.Receive()
				if pkt != nil {
					mu.Lock()
					totalRecv++
					totalRecvBytes += len(pkt.Data)
					mu.Unlock()
				} else {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}
	}()

	// 进度
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				mu.Lock()
				t.Logf("  进度: sent=%d recv=%d bytes=%.2f KB", totalSent, totalRecv, float64(totalRecvBytes)/1024)
				mu.Unlock()
			}
		}
	}()

	time.Sleep(testDuration)
	close(done)
	time.Sleep(500 * time.Millisecond)
	close(progressDone)

	mu.Lock()
	defer mu.Unlock()

	bps := float64(totalRecvBytes) / testDuration.Seconds()

	t.Logf("========== 下行带宽 (Server→Client) ==========")
	t.Logf("载荷: %d bytes/msg", payloadSize)
	t.Logf("Server 队列: %d 条, Client 接收: %d 条", totalSent, totalRecv)
	t.Logf("总字节: %d (%.2f KB)", totalRecvBytes, float64(totalRecvBytes)/1024)
	t.Logf("带宽: %.0f B/s (%.2f KB/s, %.4f Mbps)", bps, bps/1024, bps*8/1000000)

	if totalRecv == 0 {
		t.Error("No messages received by client — poll() may not be working")
	}
}

// TestHTTP_Bandwidth_Bidirectional 同时双向带宽
func TestHTTP_Bandwidth_Bidirectional(t *testing.T) {
	server, client, addr := httpSetup(t)

	// 先建立连接
	httpSendData(t, client, addr, "init")
	_, recvAddr := httpPollServerUntilReceived(t, server, 5*time.Second)

	payloadSize := addr.maxBodySize - 5
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i%254 + 1)
	}

	t.Logf("双向带宽测试: payload=%d bytes", payloadSize)

	var mu sync.Mutex
	var uplinkSent, uplinkRecv, uplinkBytes int
	var downlinkSent, downlinkRecv, downlinkBytes int
	done := make(chan struct{})
	testDuration := 10 * time.Second

	// Client → Server (上行发送)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, addr.maxBodySize)
				if _, err := client.Send(pkts, addr); err == nil {
					mu.Lock()
					uplinkSent++
					mu.Unlock()
				}
			}
		}
	}()

	// Server → Client (下行发送)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, addr.maxBodySize)
				server.Send(pkts, recvAddr)
				mu.Lock()
				downlinkSent++
				mu.Unlock()
			}
		}
	}()

	// Server 接收 (上行)
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := server.Receive()
				if pkt != nil {
					mu.Lock()
					uplinkRecv++
					uplinkBytes += len(pkt.Data)
					mu.Unlock()
				} else {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}
	}()

	// Client 接收 (下行)
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := client.Receive()
				if pkt != nil {
					mu.Lock()
					downlinkRecv++
					downlinkBytes += len(pkt.Data)
					mu.Unlock()
				} else {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}
	}()

	time.Sleep(testDuration)
	close(done)
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	uplinkBps := float64(uplinkBytes) / testDuration.Seconds()
	downlinkBps := float64(downlinkBytes) / testDuration.Seconds()

	t.Logf("========== 双向带宽 ==========")
	t.Logf("上行: sent=%d recv=%d %.2f KB/s (%.4f Mbps)", uplinkSent, uplinkRecv, uplinkBps/1024, uplinkBps*8/1000000)
	t.Logf("下行: sent=%d recv=%d %.2f KB/s (%.4f Mbps)", downlinkSent, downlinkRecv, downlinkBps/1024, downlinkBps*8/1000000)
	t.Logf("合计: %.2f KB/s (%.4f Mbps)", (uplinkBps+downlinkBps)/1024, (uplinkBps+downlinkBps)*8/1000000)

	if uplinkRecv == 0 {
		t.Error("Uplink: no messages received")
	}
	if downlinkRecv == 0 {
		t.Error("Downlink: no messages received")
	}
}

// ============================================================
// 3. Concurrency Tests
// ============================================================

// TestHTTP_Concurrent_5Clients 5 个并发客户端发送到同一 server
func TestHTTP_Concurrent_5Clients(t *testing.T) {
	httpConcurrentClients(t, 5, 10)
}

// TestHTTP_Concurrent_10Clients 10 个并发客户端
func TestHTTP_Concurrent_10Clients(t *testing.T) {
	httpConcurrentClients(t, 10, 5)
}

func httpConcurrentClients(t *testing.T, clientCount, msgsPerClient int) {
	port := httpFreePort(t)
	serverURL := httpServerURL(port)

	server, err := NewHTTPServer("http", serverURL)
	if err != nil {
		t.Fatalf("NewHTTPServer error: %v", err)
	}
	defer server.Close()

	serverAddr := fmt.Sprintf("127.0.0.1:%d", port)
	httpWarmupWait(t, serverAddr, 3*time.Second)

	// 创建客户端
	clients := make([]*HTTPClient, clientCount)
	addrs := make([]*SimplexAddr, clientCount)
	for i := 0; i < clientCount; i++ {
		addrs[i], err = ResolveHTTPAddr("http", serverURL)
		if err != nil {
			t.Fatalf("ResolveHTTPAddr[%d] error: %v", i, err)
		}
		clients[i], err = NewHTTPClient(addrs[i])
		if err != nil {
			t.Fatalf("NewHTTPClient[%d] error: %v", i, err)
		}
		defer clients[i].Close()
	}

	totalExpected := clientCount * msgsPerClient
	t.Logf("%d clients x %d messages = %d total", clientCount, msgsPerClient, totalExpected)

	// 并发接收（必须在发送前启动，否则 PeekableChannel(32) 溢出）
	var mu sync.Mutex
	received := make(map[string]bool)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := server.Receive()
				if pkt != nil {
					mu.Lock()
					received[string(pkt.Data)] = true
					mu.Unlock()
				} else {
					time.Sleep(5 * time.Millisecond)
				}
			}
		}
	}()

	// 并发发送
	var wg sync.WaitGroup
	for i, client := range clients {
		wg.Add(1)
		go func(idx int, c *HTTPClient, addr *SimplexAddr) {
			defer wg.Done()
			for j := 0; j < msgsPerClient; j++ {
				msg := fmt.Sprintf("client%d-msg%d", idx, j)
				pkts := NewSimplexPacketWithMaxSize([]byte(msg), SimplexPacketTypeDATA, addr.maxBodySize)
				c.Send(pkts, addr)
				time.Sleep(20 * time.Millisecond)
			}
		}(i, client, addrs[i])
	}
	wg.Wait()

	// 等待接收完毕
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= totalExpected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(done)

	mu.Lock()
	t.Logf("Received: %d/%d", len(received), totalExpected)
	lost := 0
	for i := 0; i < clientCount; i++ {
		for j := 0; j < msgsPerClient; j++ {
			expected := fmt.Sprintf("client%d-msg%d", i, j)
			if !received[expected] {
				lost++
			}
		}
	}
	mu.Unlock()
	if lost > 0 {
		t.Errorf("Lost %d/%d messages in %d-client concurrent test", lost, totalExpected, clientCount)
	} else {
		t.Logf("PASS: all %d messages from %d clients received", totalExpected, clientCount)
	}
}

// TestHTTP_Concurrent_BurstSend 突发发送 50 条消息
func TestHTTP_Concurrent_BurstSend(t *testing.T) {
	server, client, addr := httpSetup(t)

	burstCount := 50
	t.Logf("Burst sending %d messages", burstCount)

	// 必须并发接收，否则 server 端 PeekableChannel(32) 会溢出
	var mu sync.Mutex
	received := make(map[string]bool)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := server.Receive()
				if pkt != nil {
					mu.Lock()
					received[string(pkt.Data)] = true
					mu.Unlock()
				} else {
					time.Sleep(5 * time.Millisecond)
				}
			}
		}
	}()

	for i := 0; i < burstCount; i++ {
		msg := fmt.Sprintf("burst-%d", i)
		httpSendData(t, client, addr, msg)
	}

	// 等待接收
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= burstCount {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(done)

	mu.Lock()
	t.Logf("Received: %d/%d", len(received), burstCount)
	if len(received) < burstCount {
		t.Errorf("Only received %d/%d burst messages", len(received), burstCount)
	} else {
		t.Log("PASS: all burst messages received")
	}
	mu.Unlock()
}

// ============================================================
// 4. Sustained Bidirectional
// ============================================================

// TestHTTP_Sustained_Bidirectional 持续双向通信 20 轮
func TestHTTP_Sustained_Bidirectional(t *testing.T) {
	server, client, addr := httpSetup(t)

	rounds := 20
	t.Logf("Sustained bidirectional: %d rounds", rounds)

	for round := 0; round < rounds; round++ {
		// Client → Server
		clientMsg := fmt.Sprintf("c2s-round-%d", round)
		httpSendData(t, client, addr, clientMsg)

		pkt, recvAddr := httpPollServerUntilReceived(t, server, 5*time.Second)
		if string(pkt.Data) != clientMsg {
			t.Fatalf("Round %d: server got %q, want %q", round, string(pkt.Data), clientMsg)
		}

		// Server → Client
		serverMsg := fmt.Sprintf("s2c-round-%d", round)
		respPkts := NewSimplexPacketWithMaxSize([]byte(serverMsg), SimplexPacketTypeDATA, addr.maxBodySize)
		server.Send(respPkts, recvAddr)

		respPkt := httpPollClientUntilReceived(t, client, 5*time.Second)
		if string(respPkt.Data) != serverMsg {
			t.Fatalf("Round %d: client got %q, want %q", round, string(respPkt.Data), serverMsg)
		}
	}
	t.Logf("PASS: %d rounds completed", rounds)
}

// ============================================================
// 5. Data Integrity
// ============================================================

// TestHTTP_DataIntegrity_SHA256 数据完整性 SHA256 验证
func TestHTTP_DataIntegrity_SHA256(t *testing.T) {
	server, client, addr := httpSetup(t)

	sizes := []int{1, 100, 1000, 10000, addr.maxBodySize - 5}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			data := make([]byte, size)
			for i := range data {
				data[i] = byte((i * 7 + 13) % 256)
			}
			expectedHash := sha256.Sum256(data)

			pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeDATA, addr.maxBodySize)
			if _, err := client.Send(pkts, addr); err != nil {
				t.Fatalf("Send error: %v", err)
			}

			pkt, _ := httpPollServerUntilReceived(t, server, 5*time.Second)
			gotHash := sha256.Sum256(pkt.Data)

			if gotHash != expectedHash {
				t.Errorf("SHA256 mismatch for %d bytes", size)
				t.Errorf("  expected: %x", expectedHash)
				t.Errorf("  got:      %x", gotHash)
			} else {
				t.Logf("SHA256 verified: %d bytes", size)
			}
		})
	}
}

// ============================================================
// 6. Throughput Measurement
// ============================================================

// TestHTTP_Throughput_MessageRate 消息吞吐量（msg/s）
func TestHTTP_Throughput_MessageRate(t *testing.T) {
	server, client, addr := httpSetup(t)

	messageCount := 200
	t.Logf("Throughput test: sending %d messages", messageCount)

	// 并发接收
	var mu sync.Mutex
	received := 0
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := server.Receive()
				if pkt != nil {
					mu.Lock()
					received++
					mu.Unlock()
				} else {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}
	}()

	start := time.Now()
	for i := 0; i < messageCount; i++ {
		msg := fmt.Sprintf("tput-%04d", i)
		pkts := NewSimplexPacketWithMaxSize([]byte(msg), SimplexPacketTypeDATA, addr.maxBodySize)
		client.Send(pkts, addr)
	}
	sendDuration := time.Since(start)

	// 等待接收完毕
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := received
		mu.Unlock()
		if n >= messageCount {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	totalDuration := time.Since(start)
	close(done)

	mu.Lock()
	recvCount := received
	mu.Unlock()

	sendRate := float64(messageCount) / sendDuration.Seconds()
	recvRate := float64(recvCount) / totalDuration.Seconds()

	t.Logf("========== THROUGHPUT ==========")
	t.Logf("Sent: %d messages in %v (%.1f msg/s)", messageCount, sendDuration, sendRate)
	t.Logf("Received: %d/%d in %v (%.1f msg/s)", recvCount, messageCount, totalDuration, recvRate)
	t.Logf("Loss: %.1f%%", float64(messageCount-recvCount)/float64(messageCount)*100)

	if recvCount < messageCount*9/10 {
		t.Errorf("High loss rate: only %d/%d received", recvCount, messageCount)
	}
}

// TestHTTP_Throughput_LargePayload 大包吞吐量
func TestHTTP_Throughput_LargePayload(t *testing.T) {
	server, client, addr := httpSetup(t)

	payloadSize := addr.maxBodySize - 5
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	messageCount := 100
	t.Logf("Large payload throughput: %d messages x %d bytes", messageCount, payloadSize)

	// 并发接收
	var mu sync.Mutex
	recvCount := 0
	var totalBytes int64
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := server.Receive()
				if pkt != nil {
					mu.Lock()
					recvCount++
					totalBytes += int64(len(pkt.Data))
					mu.Unlock()
				} else {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}
	}()

	start := time.Now()
	for i := 0; i < messageCount; i++ {
		pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, addr.maxBodySize)
		client.Send(pkts, addr)
	}

	// 等待接收完毕
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := recvCount
		mu.Unlock()
		if n >= messageCount {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	totalDuration := time.Since(start)
	close(done)

	mu.Lock()
	bps := float64(totalBytes) / totalDuration.Seconds()
	t.Logf("========== LARGE PAYLOAD THROUGHPUT ==========")
	t.Logf("Payload: %d bytes/msg", payloadSize)
	t.Logf("Received: %d/%d in %v", recvCount, messageCount, totalDuration)
	t.Logf("Total bytes: %d (%.2f MB)", totalBytes, float64(totalBytes)/1024/1024)
	t.Logf("Throughput: %.0f B/s (%.2f KB/s, %.4f Mbps)", bps, bps/1024, bps*8/1000000)
	lost := recvCount < messageCount*9/10
	mu.Unlock()

	if lost {
		t.Errorf("High loss rate: only %d/%d received", recvCount, messageCount)
	}
}
