//go:build dns
// +build dns

package simplex

import (
	"crypto/sha256"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// ============================================================
// DNS Stress Tests — 负载、并发、吞吐量
//
// 运行:
//   go test -v -tags dns -run "TestDNSStress_" ./x/simplex/ -timeout 60s
// ============================================================

// TestDNSStress_BurstSend_20Messages 突发发送 20 条消息
func TestDNSStress_BurstSend_20Messages(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	burstCount := 20
	t.Logf("Burst sending %d messages", burstCount)

	for i := 0; i < burstCount; i++ {
		msg := fmt.Sprintf("burst-%d", i)
		dnsSendData(t, client, addr, msg)
		time.Sleep(50 * time.Millisecond) // DNS 需要时间处理
	}

	received := make(map[string]bool)
	deadline := time.Now().Add(10 * time.Second)
	for len(received) < burstCount && time.Now().Before(deadline) {
		pkt, _, _ := server.Receive()
		if pkt != nil {
			received[string(pkt.Data)] = true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}

	t.Logf("Received: %d/%d", len(received), burstCount)
	lost := 0
	for i := 0; i < burstCount; i++ {
		expected := fmt.Sprintf("burst-%d", i)
		if !received[expected] {
			t.Errorf("LOST: %q", expected)
			lost++
		}
	}
	if lost > 0 {
		t.Errorf("BUG: %d/%d burst messages lost", lost, burstCount)
	} else {
		t.Log("All burst messages received")
	}
}

// TestDNSStress_ConcurrentClients_5 5 个并发客户端
func TestDNSStress_ConcurrentClients_5(t *testing.T) {
	dnsStressConcurrentClients(t, 5, 5)
}

// TestDNSStress_ConcurrentClients_10 10 个并发客户端
func TestDNSStress_ConcurrentClients_10(t *testing.T) {
	dnsStressConcurrentClients(t, 10, 3)
}

func dnsStressConcurrentClients(t *testing.T, clientCount, msgsPerClient int) {
	port := dnsTestPort(t)
	serverURL := dnsServerURL(port, testDomain)

	server, err := NewDNSServer("dns", serverURL)
	if err != nil {
		t.Fatalf("NewDNSServer error: %v", err)
	}
	defer server.Close()

	serverAddr := fmt.Sprintf("127.0.0.1:%d", port)
	dnsWarmupWait(t, serverAddr, testDomain, 3*time.Second)

	// 创建客户端
	clients := make([]*DNSClient, clientCount)
	addrs := make([]*SimplexAddr, clientCount)
	for i := 0; i < clientCount; i++ {
		clientURL := dnsServerURL(port, testDomain)
		addrs[i], _ = ResolveDNSAddr("dns", clientURL)
		clients[i], err = NewDNSClient(addrs[i])
		if err != nil {
			t.Fatalf("NewDNSClient[%d] error: %v", i, err)
		}
		defer clients[i].Close()
	}

	// 并发发送
	t.Logf("Phase 1: %d clients each send %d messages", clientCount, msgsPerClient)
	var wg sync.WaitGroup
	for i, client := range clients {
		wg.Add(1)
		go func(idx int, c *DNSClient, addr *SimplexAddr) {
			defer wg.Done()
			for j := 0; j < msgsPerClient; j++ {
				msg := fmt.Sprintf("client%d-msg%d", idx, j)
				pkts := NewSimplexPacketWithMaxSize([]byte(msg), SimplexPacketTypeDATA, addr.maxBodySize)
				c.Send(pkts, addr)
				time.Sleep(50 * time.Millisecond)
			}
		}(i, client, addrs[i])
	}
	wg.Wait()

	// 收集
	totalExpected := clientCount * msgsPerClient
	received := make(map[string]bool)
	deadline := time.Now().Add(15 * time.Second)
	for len(received) < totalExpected && time.Now().Before(deadline) {
		pkt, _, _ := server.Receive()
		if pkt != nil {
			received[string(pkt.Data)] = true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}

	t.Logf("Received: %d/%d", len(received), totalExpected)
	lost := 0
	for i := 0; i < clientCount; i++ {
		for j := 0; j < msgsPerClient; j++ {
			expected := fmt.Sprintf("client%d-msg%d", i, j)
			if !received[expected] {
				t.Errorf("LOST: %q", expected)
				lost++
			}
		}
	}
	if lost > 0 {
		t.Errorf("BUG: %d/%d messages lost in %d-client concurrent test", lost, totalExpected, clientCount)
	} else {
		t.Logf("PASS: all %d messages from %d clients received", totalExpected, clientCount)
	}
}

// TestDNSStress_Bidirectional_Sustained 持续双向通信 10 轮
func TestDNSStress_Bidirectional_Sustained(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	rounds := 10
	t.Logf("Sustained bidirectional: %d rounds", rounds)

	for round := 0; round < rounds; round++ {
		// Client → Server
		clientMsg := fmt.Sprintf("c2s-round-%d", round)
		dnsSendData(t, client, addr, clientMsg)

		// 搜索本轮的消息（忽略其他消息如 trigger 查询）
		deadline := time.Now().Add(testPollTimeout)
		found := false
		for time.Now().Before(deadline) {
			pkt, recvAddr, _ := server.Receive()
			if pkt == nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			if string(pkt.Data) == clientMsg {
				found = true
				// Server → Client
				serverMsg := fmt.Sprintf("s2c-round-%d", round)
				respPkts := NewSimplexPacketWithMaxSize([]byte(serverMsg), SimplexPacketTypeDATA, addr.maxBodySize)
				server.Send(respPkts, recvAddr)

				// 触发响应
				triggerPkts := NewSimplexPackets()
				client.Send(triggerPkts, addr)

				respPkt := dnsPollClientUntilReceived(t, client, testPollTimeout)
				if string(respPkt.Data) != serverMsg {
					t.Errorf("Round %d: Client got %q, want %q", round, string(respPkt.Data), serverMsg)
				}
				break
			}
		}
		if !found {
			t.Fatalf("Round %d: Timeout waiting for %q", round, clientMsg)
		}
	}
	t.Logf("Sustained bidirectional passed: %d rounds", rounds)
}

// TestDNSStress_Throughput_UDP UDP 吞吐量测量
func TestDNSStress_Throughput_UDP(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	messageCount := 50
	t.Logf("Throughput test: sending %d messages", messageCount)

	start := time.Now()
	for i := 0; i < messageCount; i++ {
		msg := fmt.Sprintf("tput-%d", i)
		dnsSendData(t, client, addr, msg)
	}
	sendDuration := time.Since(start)

	// 收集
	received := 0
	deadline := time.Now().Add(15 * time.Second)
	for received < messageCount && time.Now().Before(deadline) {
		pkt, _, _ := server.Receive()
		if pkt != nil {
			received++
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	totalDuration := time.Since(start)

	sendRate := float64(messageCount) / sendDuration.Seconds()
	recvRate := float64(received) / totalDuration.Seconds()

	t.Logf("========== THROUGHPUT ==========")
	t.Logf("Sent: %d messages in %v (%.1f msg/s)", messageCount, sendDuration, sendRate)
	t.Logf("Received: %d/%d in %v (%.1f msg/s)", received, messageCount, totalDuration, recvRate)
	t.Logf("Loss rate: %.1f%%", float64(messageCount-received)/float64(messageCount)*100)

	if received < messageCount/2 {
		t.Errorf("High loss rate: only %d/%d received", received, messageCount)
	}
}

// TestDNSStress_Bandwidth_Uplink 上行带宽: Client→Server (受 DNS query 253 字符限制)
func TestDNSStress_Bandwidth_Uplink(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	maxData := addr.maxBodySize - 5 // 减去 TLV header
	payload := make([]byte, maxData)
	for i := range payload {
		payload[i] = byte(i%254 + 1)
	}

	t.Logf("上行带宽测试: 每消息载荷=%d bytes, maxBodySize=%d", maxData, addr.maxBodySize)

	// 探测最优间隔
	intervals := []time.Duration{1 * time.Millisecond, 3 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond}
	bestInterval := 5 * time.Millisecond
	bestRate := 0.0

	for _, interval := range intervals {
		sent := 0
		start := time.Now()
		for i := 0; i < 50; i++ {
			pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, addr.maxBodySize)
			if _, err := client.Send(pkts, addr); err == nil {
				sent++
			}
			time.Sleep(interval)
		}

		received := 0
		totalBytes := 0
		deadline := time.Now().Add(5 * time.Second)
		for received < sent && time.Now().Before(deadline) {
			pkt, _, _ := server.Receive()
			if pkt != nil {
				received++
				totalBytes += len(pkt.Data)
			} else {
				time.Sleep(2 * time.Millisecond)
			}
		}

		dur := time.Since(start).Seconds()
		lossRate := 0.0
		if sent > 0 {
			lossRate = float64(sent-received) / float64(sent) * 100
		}
		rate := float64(totalBytes) / dur

		t.Logf("  探测 interval=%v: sent=%d recv=%d loss=%.0f%% rate=%.0f B/s (%.2f KB/s)",
			interval, sent, received, lossRate, rate, rate/1024)

		if lossRate < 5.0 && rate > bestRate {
			bestRate = rate
			bestInterval = interval
		}
	}

	t.Logf("  选择间隔: %v", bestInterval)

	// 60 秒持续发送
	var mu sync.Mutex
	totalSent, totalRecv, totalRecvBytes := 0, 0, 0
	done := make(chan struct{})
	testDuration := 60 * time.Second

	go func() {
		ticker := time.NewTicker(bestInterval)
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
		ticker := time.NewTicker(15 * time.Second)
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
	time.Sleep(1 * time.Second)
	close(progressDone)

	mu.Lock()
	defer mu.Unlock()

	lossRate := 0.0
	if totalSent > 0 {
		lossRate = float64(totalSent-totalRecv) / float64(totalSent) * 100
	}
	bps := float64(totalRecvBytes) / testDuration.Seconds()

	t.Logf("========== 上行带宽 (Client→Server) ==========")
	t.Logf("载荷: %d bytes/msg, 间隔: %v", maxData, bestInterval)
	t.Logf("发送: %d 条, 接收: %d 条, 丢包: %.1f%%", totalSent, totalRecv, lossRate)
	t.Logf("总字节: %d (%.2f KB)", totalRecvBytes, float64(totalRecvBytes)/1024)
	t.Logf("带宽: %.0f B/s (%.2f KB/s, %.4f Mbps)", bps, bps/1024, bps*8/1000000)
}

// TestDNSStress_Bandwidth_Downlink 下行带宽: Server→Client (受 DNS response 512/65535 限制)
func TestDNSStress_Bandwidth_Downlink(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	// 先建立连接
	dnsSendData(t, client, addr, "init")
	_, recvAddr := dnsPollServerUntilReceived(t, server, testPollTimeout)

	// Server 端 maxBodySize = 512 (UDP), 这是 GetPackets 的限制
	serverMaxBody := 512 // MaxUDPMessageSize
	downPayloadSize := serverMaxBody - 5
	t.Logf("下行带宽测试: server maxBodySize=%d, 载荷=%d bytes/msg", serverMaxBody, downPayloadSize)
	t.Logf("上行轮询载荷: %d bytes/query (仅用于触发响应)", addr.maxBodySize)

	payload := make([]byte, downPayloadSize)
	for i := range payload {
		payload[i] = byte(i%254 + 1)
	}

	// 探测: 不同轮询间隔下的下行吞吐量
	// 客户端必须发送查询才能触发服务端响应，所以下行受轮询频率限制
	intervals := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}
	bestInterval := 10 * time.Millisecond
	bestRate := 0.0

	for _, interval := range intervals {
		// 预填充 server 的 WriteBuf
		for i := 0; i < 50; i++ {
			pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, serverMaxBody)
			server.Send(pkts, recvAddr)
		}

		received := 0
		totalBytes := 0
		start := time.Now()

		for i := 0; i < 50; i++ {
			// 发送轮询查询触发响应
			triggerPkts := NewSimplexPackets()
			triggerPkts.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("p")))
			client.Send(triggerPkts, addr)

			// 读取响应
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				pkt, _, _ := client.Receive()
				if pkt != nil {
					received++
					totalBytes += len(pkt.Data)
					break
				}
				time.Sleep(1 * time.Millisecond)
			}
			time.Sleep(interval)
		}

		dur := time.Since(start).Seconds()
		rate := float64(totalBytes) / dur

		t.Logf("  探测 interval=%v: recv=%d/50 bytes=%d rate=%.0f B/s (%.2f KB/s)",
			interval, received, totalBytes, rate, rate/1024)

		if rate > bestRate {
			bestRate = rate
			bestInterval = interval
		}
	}

	t.Logf("  选择间隔: %v", bestInterval)

	// 60 秒持续下行测试
	var mu sync.Mutex
	totalPolls, totalRecv, totalRecvBytes := 0, 0, 0
	done := make(chan struct{})
	testDuration := 60 * time.Second

	// Server 持续填充 WriteBuf
	go func() {
		ticker := time.NewTicker(bestInterval / 2) // 填充速度 > 轮询速度
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, serverMaxBody)
				server.Send(pkts, recvAddr)
			}
		}
	}()

	// Client 轮询获取响应
	go func() {
		ticker := time.NewTicker(bestInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				triggerPkts := NewSimplexPackets()
				triggerPkts.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("p")))
				client.Send(triggerPkts, addr)
				mu.Lock()
				totalPolls++
				mu.Unlock()
			}
		}
	}()

	// Client 接收响应
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
					time.Sleep(1 * time.Millisecond)
				}
			}
		}
	}()

	// 进度报告
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				mu.Lock()
				t.Logf("  进度: polls=%d recv=%d bytes=%.2f KB", totalPolls, totalRecv, float64(totalRecvBytes)/1024)
				mu.Unlock()
			}
		}
	}()

	time.Sleep(testDuration)
	close(done)
	time.Sleep(1 * time.Second)
	close(progressDone)

	mu.Lock()
	defer mu.Unlock()

	bps := float64(totalRecvBytes) / testDuration.Seconds()

	t.Logf("========== 下行带宽 (Server→Client) ==========")
	t.Logf("载荷: %d bytes/msg, 轮询间隔: %v", downPayloadSize, bestInterval)
	t.Logf("轮询: %d 次, 收到响应: %d 条", totalPolls, totalRecv)
	t.Logf("总字节: %d (%.2f KB)", totalRecvBytes, float64(totalRecvBytes)/1024)
	t.Logf("带宽: %.0f B/s (%.2f KB/s, %.4f Mbps)", bps, bps/1024, bps*8/1000000)
	t.Logf("对比: 上行载荷=%d bytes/msg, 下行载荷=%d bytes/msg (%.1fx)",
		addr.maxBodySize-5, downPayloadSize, float64(downPayloadSize)/float64(addr.maxBodySize-5))
}

// TestDNSStress_DataIntegrity_SHA256 数据完整性 SHA256 验证
func TestDNSStress_DataIntegrity_SHA256(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	// 不同大小的 payload（都在 maxBodySize 范围内）
	maxData := addr.maxBodySize - 5 // 减去 TLV header
	sizes := []int{10, 30, 50}
	if maxData < 50 {
		sizes = []int{5, 10, maxData}
	}

	for _, size := range sizes {
		if size > maxData {
			size = maxData
		}
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			data := make([]byte, size)
			for i := range data {
				data[i] = byte((i * 7 + 13) % 256)
			}
			expectedHash := sha256.Sum256(data)

			pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeDATA, addr.maxBodySize)
			_, err := client.Send(pkts, addr)
			if err != nil {
				t.Fatalf("Send error: %v", err)
			}

			pkt, _ := dnsPollServerUntilReceived(t, server, testPollTimeout)
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

// TestDNSStress_RapidOpenClose 快速创建和关闭 server+client
func TestDNSStress_RapidOpenClose(t *testing.T) {
	initialGoroutines := runtime.NumGoroutine()
	iterations := 10

	t.Logf("Rapid open/close: %d iterations (initial goroutines: %d)", iterations, initialGoroutines)

	for i := 0; i < iterations; i++ {
		port := dnsTestPort(t)
		serverURL := dnsServerURL(port, testDomain)

		server, err := NewDNSServer("dns", serverURL)
		if err != nil {
			t.Fatalf("Iteration %d: NewDNSServer error: %v", i, err)
		}

		serverAddr := fmt.Sprintf("127.0.0.1:%d", port)
		dnsWarmupWait(t, serverAddr, testDomain, 3*time.Second)

		clientURL := dnsServerURL(port, testDomain)
		addr, _ := ResolveDNSAddr("dns", clientURL)
		client, err := NewDNSClient(addr)
		if err != nil {
			server.Close()
			t.Fatalf("Iteration %d: NewDNSClient error: %v", i, err)
		}

		client.Close()
		server.Close()
	}

	// 等待 goroutine 清理
	time.Sleep(500 * time.Millisecond)
	finalGoroutines := runtime.NumGoroutine()

	t.Logf("Goroutines: initial=%d, final=%d, leaked=%d", initialGoroutines, finalGoroutines, finalGoroutines-initialGoroutines)

	// 允许一些波动 (test runner 本身的 goroutine)
	leaked := finalGoroutines - initialGoroutines
	if leaked > 5 {
		t.Errorf("Possible goroutine leak: %d goroutines leaked after %d open/close cycles", leaked, iterations)
	}
}

// TestDNSStress_ConcurrentSendReceive 并发发送和接收
func TestDNSStress_ConcurrentSendReceive(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	// 先建立连接
	dnsSendData(t, client, addr, "init")
	_, recvAddr := dnsPollServerUntilReceived(t, server, testPollTimeout)

	duration := 2 * time.Second
	var sendCount, serverRecvCount, clientRecvCount int
	var mu sync.Mutex
	done := make(chan struct{})

	// Client → Server 发送 goroutine
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				mu.Lock()
				i := sendCount
				sendCount++
				mu.Unlock()
				msg := fmt.Sprintf("concurrent-%d", i)
				pkts := NewSimplexPacketWithMaxSize([]byte(msg), SimplexPacketTypeDATA, addr.maxBodySize)
				client.Send(pkts, addr)
			}
		}
	}()

	// Server → Client 响应 goroutine
	go func() {
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				mu.Lock()
				i := clientRecvCount
				mu.Unlock()
				msg := fmt.Sprintf("response-%d", i)
				pkts := NewSimplexPacketWithMaxSize([]byte(msg), SimplexPacketTypeDATA, addr.maxBodySize)
				server.Send(pkts, recvAddr)
			}
		}
	}()

	// Server 接收 goroutine
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := server.Receive()
				if pkt != nil {
					mu.Lock()
					serverRecvCount++
					mu.Unlock()
				} else {
					time.Sleep(10 * time.Millisecond)
				}
			}
		}
	}()

	// Client 接收 goroutine
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				pkt, _, _ := client.Receive()
				if pkt != nil {
					mu.Lock()
					clientRecvCount++
					mu.Unlock()
				} else {
					time.Sleep(10 * time.Millisecond)
				}
			}
		}
	}()

	// 运行指定时间
	time.Sleep(duration)
	close(done)
	time.Sleep(500 * time.Millisecond) // 等待 goroutine 退出

	mu.Lock()
	t.Logf("========== CONCURRENT RESULTS ==========")
	t.Logf("Duration: %v", duration)
	t.Logf("Client sent: %d messages", sendCount)
	t.Logf("Server received: %d messages", serverRecvCount)
	t.Logf("Client received: %d responses", clientRecvCount)
	mu.Unlock()

	if serverRecvCount == 0 {
		t.Error("Server received no messages during concurrent test")
	}
}
