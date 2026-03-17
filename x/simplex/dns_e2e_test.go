//go:build dns
// +build dns

package simplex

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// ============================================================
// DNS E2E Integration Tests — 本地 DNS 服务器 + 客户端完整通信
//
// 运行:
//   go test -v -tags dns -run "TestDNS_E2E_" ./x/simplex/ -timeout 30s
// ============================================================

const (
	testDomain      = "test.com"
	testPollTimeout = 5 * time.Second
	testInterval    = 50 // ms
)

// --- 共享测试辅助函数 ---

// dnsTestPort 获取一个可用的随机高端口
func dnsTestPort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to get free port: %v", err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	return port
}

// dnsServerURL 构建 DNS 服务端 URL
func dnsServerURL(port int, domain string) string {
	return fmt.Sprintf("dns://127.0.0.1:%d?domain=%s&interval=%d", port, domain, testInterval)
}

// dnsWarmupWait 等待 DNS 服务器就绪（异步启动需要预热）
func dnsWarmupWait(t *testing.T, serverAddr, domain string, timeout time.Duration) {
	t.Helper()
	client := &dns.Client{Net: "udp", Timeout: 500 * time.Millisecond}
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn("warmup."+domain), dns.TypeTXT)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, _, err := client.Exchange(msg, serverAddr)
		if err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("DNS server at %s did not become ready within %v", serverAddr, timeout)
}

// dnsSetupUDP 创建 UDP DNS server+client 对
func dnsSetupUDP(t *testing.T) (*DNSServer, *DNSClient, *SimplexAddr) {
	t.Helper()
	port := dnsTestPort(t)
	serverURL := dnsServerURL(port, testDomain)

	server, err := NewDNSServer("dns", serverURL)
	if err != nil {
		t.Fatalf("NewDNSServer error: %v", err)
	}
	t.Cleanup(func() { server.Close() })

	serverAddr := fmt.Sprintf("127.0.0.1:%d", port)
	dnsWarmupWait(t, serverAddr, testDomain, 3*time.Second)

	clientURL := dnsServerURL(port, testDomain)
	addr, err := ResolveDNSAddr("dns", clientURL)
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}

	client, err := NewDNSClient(addr)
	if err != nil {
		t.Fatalf("NewDNSClient error: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return server, client, addr
}

// dnsSetupDoH 创建 DoH DNS server+client 对
func dnsSetupDoH(t *testing.T, method string) (*DNSServer, *DNSClient, *SimplexAddr) {
	t.Helper()
	port := dnsTestPort(t)
	serverURL := fmt.Sprintf("doh://127.0.0.1:%d?domain=%s&interval=%d", port, testDomain, testInterval)

	server, err := NewDNSServer("doh", serverURL)
	if err != nil {
		t.Fatalf("NewDNSServer (DoH) error: %v", err)
	}
	t.Cleanup(func() { server.Close() })

	// 等待 DoH HTTP server 启动
	time.Sleep(500 * time.Millisecond)

	clientURL := fmt.Sprintf("doh://127.0.0.1:%d?domain=%s&interval=%d&method=%s", port, testDomain, testInterval, method)
	addr, err := ResolveDNSAddr("doh", clientURL)
	if err != nil {
		t.Fatalf("ResolveDNSAddr (DoH) error: %v", err)
	}

	client, err := NewDNSClient(addr)
	if err != nil {
		t.Fatalf("NewDNSClient (DoH) error: %v", err)
	}
	// 确保 TLS 配置允许自签名证书
	if client.httpClient != nil && client.httpClient.Transport != nil {
		if tr, ok := client.httpClient.Transport.(*http.Transport); ok {
			if tr.TLSClientConfig == nil {
				tr.TLSClientConfig = &tls.Config{}
			}
			tr.TLSClientConfig.InsecureSkipVerify = true
		}
	}
	t.Cleanup(func() { client.Close() })

	return server, client, addr
}

// dnsSendData 发送数据包
func dnsSendData(t *testing.T, client *DNSClient, addr *SimplexAddr, data string) {
	t.Helper()
	pkts := NewSimplexPacketWithMaxSize([]byte(data), SimplexPacketTypeDATA, addr.maxBodySize)
	_, err := client.Send(pkts, addr)
	if err != nil {
		t.Fatalf("Send %q error: %v", data, err)
	}
}

// dnsPollServerUntilReceived 轮询服务端直到收到数据包
func dnsPollServerUntilReceived(t *testing.T, server *DNSServer, timeout time.Duration) (*SimplexPacket, *SimplexAddr) {
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
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Timeout waiting for server to receive data")
	return nil, nil
}

// dnsPollClientUntilReceived 轮询客户端直到收到响应
func dnsPollClientUntilReceived(t *testing.T, client *DNSClient, timeout time.Duration) *SimplexPacket {
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
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Timeout waiting for client to receive response")
	return nil
}

// ============================================================
// E2E 测试
// ============================================================

// TestDNS_E2E_UDP_FullRoundTrip 完整 UDP 双向通信
func TestDNS_E2E_UDP_FullRoundTrip(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	// Client → Server
	dnsSendData(t, client, addr, "hello from client")
	pkt, recvAddr := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if string(pkt.Data) != "hello from client" {
		t.Errorf("Server received %q, want %q", string(pkt.Data), "hello from client")
	}
	t.Logf("Server received: %q from %s", string(pkt.Data), recvAddr.ID())

	// Server → Client（通过 WriteBuf 存储，等待下次客户端查询时返回）
	respPkts := NewSimplexPacketWithMaxSize([]byte("hello from server"), SimplexPacketTypeDATA, addr.maxBodySize)
	n, err := server.Send(respPkts, recvAddr)
	if err != nil {
		t.Fatalf("Server.Send error: %v", err)
	}
	t.Logf("Server queued %d bytes for client", n)

	// 客户端需要发送另一个查询来触发响应
	dnsSendData(t, client, addr, "poll")

	respPkt := dnsPollClientUntilReceived(t, client, testPollTimeout)
	if string(respPkt.Data) != "hello from server" {
		t.Errorf("Client received %q, want %q", string(respPkt.Data), "hello from server")
	}
	t.Logf("Client received: %q", string(respPkt.Data))
}

// TestDNS_E2E_MultipleClients_UDP 多客户端连接同一服务器
func TestDNS_E2E_MultipleClients_UDP(t *testing.T) {
	port := dnsTestPort(t)
	serverURL := dnsServerURL(port, testDomain)

	server, err := NewDNSServer("dns", serverURL)
	if err != nil {
		t.Fatalf("NewDNSServer error: %v", err)
	}
	defer server.Close()

	serverAddr := fmt.Sprintf("127.0.0.1:%d", port)
	dnsWarmupWait(t, serverAddr, testDomain, 3*time.Second)

	// 创建 3 个客户端
	clients := make([]*DNSClient, 3)
	addrs := make([]*SimplexAddr, 3)
	for i := 0; i < 3; i++ {
		clientURL := dnsServerURL(port, testDomain)
		addrs[i], _ = ResolveDNSAddr("dns", clientURL)
		clients[i], err = NewDNSClient(addrs[i])
		if err != nil {
			t.Fatalf("NewDNSClient[%d] error: %v", i, err)
		}
		defer clients[i].Close()
	}

	// 每个客户端发送一条消息
	for i, client := range clients {
		msg := fmt.Sprintf("data from client %d", i)
		dnsSendData(t, client, addrs[i], msg)
	}

	// 收集所有消息
	received := make(map[string]bool)
	deadline := time.Now().Add(testPollTimeout)
	for len(received) < 3 && time.Now().Before(deadline) {
		pkt, _, _ := server.Receive()
		if pkt != nil {
			received[string(pkt.Data)] = true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}

	for i := 0; i < 3; i++ {
		expected := fmt.Sprintf("data from client %d", i)
		if !received[expected] {
			t.Errorf("Missing: %q", expected)
		}
	}

	// 验证 buffer 隔离 — 每个 client 有独立的 connID
	bufferCount := 0
	server.buffers.Range(func(_, _ interface{}) bool {
		bufferCount++
		return true
	})
	// bufferCount 应等于收到不同连接的数量
	if bufferCount < 2 {
		t.Errorf("Expected at least 2 client buffers, got %d", bufferCount)
	}
	t.Logf("Multi-client test passed: %d clients, %d buffers, %d messages received",
		len(clients), bufferCount, len(received))
}

// TestDNS_E2E_MultiRound_Bidirectional 多轮双向通信
func TestDNS_E2E_MultiRound_Bidirectional(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	rounds := 3
	for round := 0; round < rounds; round++ {
		// Client → Server
		clientMsg := fmt.Sprintf("client-round-%d", round)
		dnsSendData(t, client, addr, clientMsg)

		// 收集服务端消息直到找到本轮的数据
		deadline := time.Now().Add(testPollTimeout)
		found := false
		for time.Now().Before(deadline) {
			pkt, recvAddr, err := server.Receive()
			if err != nil {
				t.Fatalf("Round %d: Server.Receive error: %v", round, err)
			}
			if pkt == nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			if string(pkt.Data) == clientMsg {
				found = true
				// Server → Client
				serverMsg := fmt.Sprintf("server-round-%d", round)
				respPkts := NewSimplexPacketWithMaxSize([]byte(serverMsg), SimplexPacketTypeDATA, addr.maxBodySize)
				_, err := server.Send(respPkts, recvAddr)
				if err != nil {
					t.Fatalf("Round %d: Server.Send error: %v", round, err)
				}

				// 客户端发送触发查询以获取响应
				triggerPkts := NewSimplexPackets()
				client.Send(triggerPkts, addr)

				respPkt := dnsPollClientUntilReceived(t, client, testPollTimeout)
				if string(respPkt.Data) != serverMsg {
					t.Errorf("Round %d: Client got %q, want %q", round, string(respPkt.Data), serverMsg)
				}
				break
			}
			// 忽略其他消息（如上一轮的 trigger 查询）
		}
		if !found {
			t.Fatalf("Round %d: Timeout waiting for %q", round, clientMsg)
		}
	}
	t.Logf("Multi-round bidirectional test passed: %d rounds", rounds)
}

// TestDNS_E2E_ConsecutiveSends 连续快速发送
func TestDNS_E2E_ConsecutiveSends(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	count := 3
	for i := 0; i < count; i++ {
		msg := fmt.Sprintf("consecutive-%d", i)
		dnsSendData(t, client, addr, msg)
		time.Sleep(100 * time.Millisecond) // 给 DNS 一点处理时间
	}

	received := make(map[string]bool)
	deadline := time.Now().Add(testPollTimeout)
	for len(received) < count && time.Now().Before(deadline) {
		pkt, _, _ := server.Receive()
		if pkt != nil {
			received[string(pkt.Data)] = true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}

	for i := 0; i < count; i++ {
		expected := fmt.Sprintf("consecutive-%d", i)
		if !received[expected] {
			t.Errorf("Missing: %q", expected)
		}
	}
	t.Logf("Consecutive sends: %d/%d received", len(received), count)
}

// TestDNS_E2E_PollingWithPendingResponse 空轮询获取待发响应
func TestDNS_E2E_PollingWithPendingResponse(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	// 先建立连接（server 需要知道 client 的 connID）
	dnsSendData(t, client, addr, "init")
	_, recvAddr := dnsPollServerUntilReceived(t, server, testPollTimeout)

	// Server 预存响应数据
	respPkts := NewSimplexPacketWithMaxSize([]byte("pending-response"), SimplexPacketTypeDATA, addr.maxBodySize)
	_, err := server.Send(respPkts, recvAddr)
	if err != nil {
		t.Fatalf("Server.Send error: %v", err)
	}

	// Client 发送空查询来触发响应
	emptyPkts := NewSimplexPackets()
	emptyPkts.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("poll")))
	client.Send(emptyPkts, addr)

	respPkt := dnsPollClientUntilReceived(t, client, testPollTimeout)
	if string(respPkt.Data) != "pending-response" {
		t.Errorf("Client got %q, want %q", string(respPkt.Data), "pending-response")
	}
}

// TestDNS_E2E_CloseCleanup 关闭后的清理验证
func TestDNS_E2E_CloseCleanup(t *testing.T) {
	server, client, _ := dnsSetupUDP(t)

	// 关闭客户端
	err := client.Close()
	if err != nil {
		t.Errorf("Client.Close error: %v", err)
	}

	// 关闭后 Receive 应返回 ErrClosedPipe
	_, _, err = client.Receive()
	if err != io.ErrClosedPipe {
		t.Errorf("After close, Receive error: got %v, want ErrClosedPipe", err)
	}

	// 关闭服务端
	err = server.Close()
	if err != nil {
		t.Errorf("Server.Close error: %v", err)
	}
}

// TestDNS_E2E_BufferSeparation 验证 ReadBuf/WriteBuf 分离
func TestDNS_E2E_BufferSeparation(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	// Client → Server
	dnsSendData(t, client, addr, "client-data")
	pkt, recvAddr := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if string(pkt.Data) != "client-data" {
		t.Errorf("Server got %q, want %q", string(pkt.Data), "client-data")
	}

	// Server 发送响应
	respPkts := NewSimplexPacketWithMaxSize([]byte("server-response"), SimplexPacketTypeDATA, addr.maxBodySize)
	server.Send(respPkts, recvAddr)

	// 确认 server 不会收到自己发出的数据
	time.Sleep(200 * time.Millisecond)
	pkt2, _, _ := server.Receive()
	// 可能收到 "poll" 数据，但不应收到 "server-response"
	if pkt2 != nil && string(pkt2.Data) == "server-response" {
		t.Error("Server received its own response data - buffer separation broken")
	}
}

// TestDNS_E2E_DoH_POST_FullRoundTrip DoH POST 完整通信
func TestDNS_E2E_DoH_POST_FullRoundTrip(t *testing.T) {
	server, client, addr := dnsSetupDoH(t, "POST")

	// Client → Server
	dnsSendData(t, client, addr, "doh-post-hello")
	pkt, recvAddr := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if string(pkt.Data) != "doh-post-hello" {
		t.Errorf("Server received %q, want %q", string(pkt.Data), "doh-post-hello")
	}

	// Server → Client
	respPkts := NewSimplexPacketWithMaxSize([]byte("doh-post-response"), SimplexPacketTypeDATA, addr.maxBodySize)
	server.Send(respPkts, recvAddr)

	// 触发响应
	dnsSendData(t, client, addr, "poll")
	respPkt := dnsPollClientUntilReceived(t, client, testPollTimeout)
	if string(respPkt.Data) != "doh-post-response" {
		t.Errorf("Client received %q, want %q", string(respPkt.Data), "doh-post-response")
	}
	t.Log("DoH POST roundtrip passed")
}

// TestDNS_E2E_DoH_GET_FullRoundTrip DoH GET 完整通信
func TestDNS_E2E_DoH_GET_FullRoundTrip(t *testing.T) {
	server, client, addr := dnsSetupDoH(t, "GET")

	dnsSendData(t, client, addr, "doh-get-hello")
	pkt, recvAddr := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if string(pkt.Data) != "doh-get-hello" {
		t.Errorf("Server received %q, want %q", string(pkt.Data), "doh-get-hello")
	}

	respPkts := NewSimplexPacketWithMaxSize([]byte("doh-get-response"), SimplexPacketTypeDATA, addr.maxBodySize)
	server.Send(respPkts, recvAddr)

	dnsSendData(t, client, addr, "poll")
	respPkt := dnsPollClientUntilReceived(t, client, testPollTimeout)
	if string(respPkt.Data) != "doh-get-response" {
		t.Errorf("Client received %q, want %q", string(respPkt.Data), "doh-get-response")
	}
	t.Log("DoH GET roundtrip passed")
}

// dnsDoHWarmupWait 等待 DoH 服务器就绪
func dnsDoHWarmupWait(t *testing.T, serverURL string, timeout time.Duration) {
	t.Helper()
	httpClient := &http.Client{
		Timeout: 500 * time.Millisecond,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := httpClient.Get(serverURL)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("DoH server at %s did not become ready within %v", serverURL, timeout)
}
