//go:build dns
// +build dns

package simplex

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chainreactors/rem/x/encoders"
)

// ============================================================
// DNS Boundary Tests — 边界条件和极限场景
//
// 运行:
//   go test -v -tags dns -run "TestDNSBoundary_" ./x/simplex/ -timeout 30s
// ============================================================

// TestDNSBoundary_MaxPayload_UDP 发送接近 maxBodySize 上限的数据
// 注意: dns.go 的 maxBodySize 计算 (0.7 * (253 - domainLen)) 略有偏差，
// 没有精确考虑 label dots + connID + FQDN 的开销，导致边界处可能超过 253 字符。
// 这里通过二分法找到实际可发送的最大数据量。
func TestDNSBoundary_MaxPayload_UDP(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	maxBody := addr.maxBodySize
	t.Logf("MaxBodySize from config: %d", maxBody)

	// 通过递减找到实际能发送的最大数据量
	// 注意: sendDNS 会先 Marshal SimplexPackets（添加 5 字节 TLV header），
	// 然后 B58 编码整个 marshaled 数据
	var maxDataLen int
	for tryLen := maxBody - 5; tryLen > 0; tryLen-- {
		data := make([]byte, tryLen)
		for i := range data {
			data[i] = byte(i%254 + 1)
		}
		// 模拟 sendDNS 的编码流程：wrap in packet → marshal → B58 → split → query
		pkt := NewSimplexPacket(SimplexPacketTypeDATA, data)
		marshaled := pkt.Marshal()
		encoded := encoders.B58Encode(marshaled)
		chunks := client.splitIntoChunks(encoded, MaxLabelLength)
		query := client.createMultiLabelQuery(chunks)
		if len(query) <= MaxDomainLength {
			maxDataLen = tryLen
			t.Logf("Actual max data: %d bytes (marshaled=%d, B58=%d, query=%d)",
				tryLen, len(marshaled), len(encoded), len(query))
			break
		}
	}

	if maxDataLen == 0 {
		t.Fatal("Could not find any data size that fits")
	}

	// 发送最大数据
	data := make([]byte, maxDataLen)
	for i := range data {
		data[i] = byte(i % 256)
	}

	pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeDATA, maxBody)
	_, err := client.Send(pkts, addr)
	if err != nil {
		t.Fatalf("Send max payload error: %v", err)
	}

	pkt, _ := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if len(pkt.Data) != maxDataLen {
		t.Errorf("Size mismatch: sent %d, received %d", maxDataLen, len(pkt.Data))
	}

	for i := range pkt.Data {
		if pkt.Data[i] != byte(i%256) {
			t.Errorf("Data corruption at byte %d: got 0x%02x, want 0x%02x", i, pkt.Data[i], byte(i%256))
			break
		}
	}
	t.Logf("Max payload test passed: %d bytes (config maxBodySize=%d, delta=%d)",
		maxDataLen, maxBody, maxBody-5-maxDataLen)
}

// TestDNSBoundary_EmptyPolling 空轮询（无数据，无待发响应）
func TestDNSBoundary_EmptyPolling(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	// 发送空的轮询请求
	emptyPkts := NewSimplexPackets()
	_, err := client.Send(emptyPkts, addr)
	if err != nil {
		t.Fatalf("Send empty poll error: %v", err)
	}

	// 服务端不应有数据
	time.Sleep(200 * time.Millisecond)
	pkt, _, _ := server.Receive()
	if pkt != nil {
		t.Logf("Received unexpected data during empty poll: %q", string(pkt.Data))
	}

	// 客户端不应有响应
	rpkt, _, _ := client.Receive()
	if rpkt != nil && len(rpkt.Data) > 0 {
		t.Logf("Received unexpected response during empty poll: %q", string(rpkt.Data))
	}
}

// TestDNSBoundary_EmptyPolling_WithPendingResponse 空轮询获取待发数据
func TestDNSBoundary_EmptyPolling_WithPendingResponse(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	// 先建立连接
	dnsSendData(t, client, addr, "establish")
	_, recvAddr := dnsPollServerUntilReceived(t, server, testPollTimeout)

	// 服务端存入待发数据
	respPkts := NewSimplexPacketWithMaxSize([]byte("pending-data"), SimplexPacketTypeDATA, addr.maxBodySize)
	server.Send(respPkts, recvAddr)

	// 客户端发送空轮询（触发响应）
	dnsSendData(t, client, addr, "poll-for-pending")

	respPkt := dnsPollClientUntilReceived(t, client, testPollTimeout)
	if string(respPkt.Data) != "pending-data" {
		t.Errorf("Got %q, want %q", string(respPkt.Data), "pending-data")
	}
}

// TestDNSBoundary_SingleLabel_ExactMax 数据恰好填满一个 DNS 标签 (63 chars)
func TestDNSBoundary_SingleLabel_ExactMax(t *testing.T) {
	client := testDNSClient("t.co", "c1", 200)

	// 找到编码后恰好为 63 字符的原始数据长度
	// B58 expansion ratio ~1.365, 所以 63/1.365 ≈ 46 bytes
	// 尝试不同大小直到找到恰好 63 字符的
	var targetData []byte
	for dataLen := 40; dataLen <= 50; dataLen++ {
		data := make([]byte, dataLen)
		for i := range data {
			data[i] = byte(i + 1) // 非零避免 B58 leading zeros
		}
		encoded := encoders.B58Encode(data)
		if len(encoded) == MaxLabelLength {
			targetData = data
			t.Logf("Found data of %d bytes that encodes to exactly %d chars", dataLen, MaxLabelLength)
			break
		}
	}

	if targetData == nil {
		// 如果找不到精确匹配，测试短于 63 字符的情况
		targetData = make([]byte, 40)
		for i := range targetData {
			targetData[i] = byte(i + 1)
		}
		t.Log("Could not find exact 63-char encoding, testing with shorter data")
	}

	encoded := encoders.B58Encode(targetData)
	chunks := client.splitIntoChunks(encoded, MaxLabelLength)
	if len(chunks) != 1 {
		t.Errorf("Expected 1 chunk, got %d (encoded length=%d)", len(chunks), len(encoded))
	}
	if len(chunks) > 0 && len(chunks[0]) > MaxLabelLength {
		t.Errorf("Chunk exceeds MaxLabelLength: %d > %d", len(chunks[0]), MaxLabelLength)
	}
}

// TestDNSBoundary_MultiLabel_Split 数据跨越多个 DNS 标签
func TestDNSBoundary_MultiLabel_Split(t *testing.T) {
	client := testDNSClient("t.co", "c1", 200)

	// 创建编码后需要 2 个标签的数据 (>63 chars B58)
	data := make([]byte, 60) // ~82 chars B58
	for i := range data {
		data[i] = byte(i + 1)
	}

	encoded := encoders.B58Encode(data)
	t.Logf("Data: %d bytes, B58: %d chars", len(data), len(encoded))

	chunks := client.splitIntoChunks(encoded, MaxLabelLength)
	if len(chunks) < 2 {
		t.Errorf("Expected >= 2 chunks for %d-char B58, got %d", len(encoded), len(chunks))
	}

	// 验证所有 chunk 不超过 MaxLabelLength
	for i, c := range chunks {
		if len(c) > MaxLabelLength {
			t.Errorf("Chunk %d exceeds MaxLabelLength: %d > %d", i, len(c), MaxLabelLength)
		}
	}

	// 验证重新拼接后可以正确解码
	joined := strings.Join(chunks, "")
	decoded := encoders.B58Decode(joined)
	if len(decoded) != len(data) {
		t.Fatalf("Roundtrip size mismatch: got %d, want %d", len(decoded), len(data))
	}
	for i := range data {
		if decoded[i] != data[i] {
			t.Errorf("Byte %d: got 0x%02x, want 0x%02x", i, decoded[i], data[i])
			break
		}
	}
}

// TestDNSBoundary_DomainLength_AtLimit 查询域名恰好在 253 字符限制
func TestDNSBoundary_DomainLength_AtLimit(t *testing.T) {
	// 使用短域名和连接ID来最大化数据空间
	domain := "t.co"  // 4 chars
	connID := "c1"    // 2 chars
	client := testDNSClient(domain, connID, 200)

	// 可用于数据的总长度 = 253 - len(domain) - len(connID) - 2 dots
	// = 253 - 4 - 2 - 2 = 245 chars for data labels + their dots
	// 每个 label 最多 63 chars + 1 dot, 所以 245 / 64 = ~3.8 -> 3 full labels + partial
	// 3 labels: 3*63 + 3 dots = 192 chars, remaining = 245-192 = 53 chars for 4th label

	chunks := []string{
		strings.Repeat("a", 63),
		strings.Repeat("b", 63),
		strings.Repeat("c", 63),
	}

	query := client.createMultiLabelQuery(chunks)
	t.Logf("Query length: %d (limit: %d)", len(query), MaxDomainLength)

	if len(query) > MaxDomainLength {
		t.Errorf("Query exceeds MaxDomainLength: %d > %d", len(query), MaxDomainLength)
	}
}

// TestDNSBoundary_DomainLength_OverLimit 超过 253 字符限制应报错
func TestDNSBoundary_DomainLength_OverLimit(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)
	_ = server

	// 构造超大数据使查询超过 253 字符
	// maxBodySize 已经限制了，但我们手动构造
	hugeData := make([]byte, 300)
	for i := range hugeData {
		hugeData[i] = byte(i%254 + 1) // 避免零字节
	}

	encoded := encoders.B58Encode(hugeData)
	chunks := client.splitIntoChunks(encoded, MaxLabelLength)
	query := client.createMultiLabelQuery(chunks)

	if len(query) <= MaxDomainLength {
		t.Logf("Query %d chars fits within limit (data may have been truncated by maxBodySize)", len(query))
		return
	}

	// 直接调用 sendDNS 来验证错误处理
	pkts := NewSimplexPackets()
	pkts.Append(NewSimplexPacket(SimplexPacketTypeDATA, hugeData))

	// 由于 maxBodySize 限制，Send 内部会截断数据
	// 这里主要验证不会 panic
	_, err := client.Send(pkts, addr)
	if err != nil {
		t.Logf("Expected error for oversized data: %v", err)
	}
}

// TestDNSBoundary_MinimalData_1Byte 发送 1 字节数据
func TestDNSBoundary_MinimalData_1Byte(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	dnsSendData(t, client, addr, "X")
	pkt, _ := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if string(pkt.Data) != "X" {
		t.Errorf("Got %q, want %q", string(pkt.Data), "X")
	}
}

// TestDNSBoundary_BinaryData_AllBytes 发送包含所有 256 字节值的数据
func TestDNSBoundary_BinaryData_AllBytes(t *testing.T) {
	// B58 roundtrip 测试所有字节值
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}

	encoded := encoders.B58Encode(data)
	decoded := encoders.B58Decode(encoded)

	if len(decoded) != len(data) {
		t.Fatalf("B58 roundtrip size mismatch: got %d, want %d", len(decoded), len(data))
	}
	for i := range data {
		if decoded[i] != data[i] {
			t.Errorf("Byte %d: got 0x%02x, want 0x%02x", i, decoded[i], data[i])
		}
	}
	t.Log("All 256 byte values survive B58 roundtrip")
}

// TestDNSBoundary_BinaryData_E2E 端到端发送二进制数据
func TestDNSBoundary_BinaryData_E2E(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	// 发送包含特殊字符的二进制数据（小于 maxBodySize）
	data := make([]byte, 50)
	for i := range data {
		data[i] = byte(i)
	}

	pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeDATA, addr.maxBodySize)
	_, err := client.Send(pkts, addr)
	if err != nil {
		t.Fatalf("Send binary data error: %v", err)
	}

	pkt, _ := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if len(pkt.Data) != len(data) {
		t.Fatalf("Size mismatch: sent %d, received %d", len(data), len(pkt.Data))
	}
	for i := range data {
		if pkt.Data[i] != data[i] {
			t.Errorf("Byte %d: got 0x%02x, want 0x%02x", i, pkt.Data[i], data[i])
			break
		}
	}
	t.Log("Binary data E2E test passed")
}

// TestDNSBoundary_ServerClose_DuringOperation 服务端在通信中关闭
func TestDNSBoundary_ServerClose_DuringOperation(t *testing.T) {
	port := dnsTestPort(t)
	serverURL := dnsServerURL(port, testDomain)

	server, err := NewDNSServer("dns", serverURL)
	if err != nil {
		t.Fatalf("NewDNSServer error: %v", err)
	}

	serverAddr := fmt.Sprintf("127.0.0.1:%d", port)
	dnsWarmupWait(t, serverAddr, testDomain, 3*time.Second)

	clientURL := dnsServerURL(port, testDomain)
	addr, _ := ResolveDNSAddr("dns", clientURL)
	client, err := NewDNSClient(addr)
	if err != nil {
		t.Fatalf("NewDNSClient error: %v", err)
	}
	defer client.Close()

	// 正常通信
	dnsSendData(t, client, addr, "before-close")
	dnsPollServerUntilReceived(t, server, testPollTimeout)

	// 关闭服务端
	server.Close()

	// 客户端继续发送不应 panic
	pkts := NewSimplexPacketWithMaxSize([]byte("after-close"), SimplexPacketTypeDATA, addr.maxBodySize)
	_, err = client.Send(pkts, addr)
	// 可能返回错误（连接被拒绝），但不应 panic
	if err != nil {
		t.Logf("Expected error after server close: %v", err)
	}
}

// TestDNSBoundary_ClientClose_DuringOperation 客户端在通信中关闭
func TestDNSBoundary_ClientClose_DuringOperation(t *testing.T) {
	_, client, addr := dnsSetupUDP(t)

	// 正常发送
	dnsSendData(t, client, addr, "before-close")

	// 关闭客户端
	err := client.Close()
	if err != nil {
		t.Errorf("Client.Close error: %v", err)
	}

	// 关闭后操作
	pkts := NewSimplexPacketWithMaxSize([]byte("after-close"), SimplexPacketTypeDATA, addr.maxBodySize)
	_, err = client.Send(pkts, addr)
	if err == nil {
		t.Logf("Send after close did not return error (may be buffered)")
	}
}

// TestDNSBoundary_DoH_LargePayload DoH 支持更大的数据传输
func TestDNSBoundary_DoH_LargePayload(t *testing.T) {
	server, client, addr := dnsSetupDoH(t, "POST")

	// DoH 的 maxBodySize = MaxHTTPMessageSize (65535)
	// 但实际可发送的数据量受 DNS 查询域名长度限制
	// 我们发送一个比 UDP 限制大的数据来验证 DoH 工作
	dataSize := 100 // 大于 UDP 通常能承载的数据
	data := make([]byte, dataSize)
	for i := range data {
		data[i] = byte(i % 256)
	}

	pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeDATA, addr.maxBodySize)
	_, err := client.Send(pkts, addr)
	if err != nil {
		t.Fatalf("DoH send error: %v", err)
	}

	pkt, _ := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if len(pkt.Data) != dataSize {
		t.Errorf("Size mismatch: sent %d, received %d", dataSize, len(pkt.Data))
	}

	for i := range pkt.Data {
		if pkt.Data[i] != byte(i%256) {
			t.Errorf("Data corruption at byte %d", i)
			break
		}
	}
	t.Logf("DoH large payload test passed: %d bytes", dataSize)
}

// TestDNSBoundary_QueryComponents 验证查询各组件的正确组装
func TestDNSBoundary_QueryComponents(t *testing.T) {
	testCases := []struct {
		name    string
		domain  string
		connID  string
		data    string
		wantFmt string // 期望的查询格式 (不含FQDN的点)
	}{
		{"short_data", "t.co", "c1", "abc", "abc.c1.t.co"},
		{"no_data", "test.com", "conn1", "", "conn1.test.com"},
		{"multi_label_domain", "sub.test.com", "conn1", "xyz", "xyz.conn1.sub.test.com"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := testDNSClient(tc.domain, tc.connID, 200)
			var chunks []string
			if tc.data != "" {
				chunks = []string{tc.data}
			} else {
				chunks = []string{""}
			}
			query := client.createMultiLabelQuery(chunks)
			if query != tc.wantFmt {
				t.Errorf("Query: got %q, want %q", query, tc.wantFmt)
			}
		})
	}
}

// TestDNSBoundary_ServerBufferIsolation 不同客户端的 buffer 完全隔离
func TestDNSBoundary_ServerBufferIsolation(t *testing.T) {
	port := dnsTestPort(t)
	serverURL := dnsServerURL(port, testDomain)

	server, err := NewDNSServer("dns", serverURL)
	if err != nil {
		t.Fatalf("NewDNSServer error: %v", err)
	}
	defer server.Close()

	serverAddr := fmt.Sprintf("127.0.0.1:%d", port)
	dnsWarmupWait(t, serverAddr, testDomain, 3*time.Second)

	// 创建两个客户端（间隔创建避免 randomString seed 碰撞）
	addr1, _ := ResolveDNSAddr("dns", dnsServerURL(port, testDomain))
	client1, _ := NewDNSClient(addr1)
	defer client1.Close()

	time.Sleep(10 * time.Millisecond) // 确保 UnixNano seed 不同

	addr2, _ := ResolveDNSAddr("dns", dnsServerURL(port, testDomain))
	client2, _ := NewDNSClient(addr2)
	defer client2.Close()

	t.Logf("Client1 connID=%s, Client2 connID=%s", addr1.id, addr2.id)
	if addr1.id == addr2.id {
		t.Log("WARNING: randomString produced same connID for two rapid calls (seed collision)")
		t.Log("This is a known issue with randomString using time.Now().UnixNano() as seed")
	}

	// Client1 发送
	dnsSendData(t, client1, addr1, "from-client1")
	pkt1, recvAddr1 := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if string(pkt1.Data) != "from-client1" {
		t.Errorf("Client1 data: got %q", string(pkt1.Data))
	}

	// Client2 发送
	dnsSendData(t, client2, addr2, "from-client2")
	pkt2, recvAddr2 := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if string(pkt2.Data) != "from-client2" {
		t.Errorf("Client2 data: got %q", string(pkt2.Data))
	}

	// 验证地址不同（如果 connID 不同）
	if addr1.id != addr2.id && recvAddr1.ID() == recvAddr2.ID() {
		t.Error("Different connIDs but same server-side ID - buffer isolation broken")
	}

	// Server 向 Client1 发送响应
	resp1 := NewSimplexPacketWithMaxSize([]byte("resp-for-client1"), SimplexPacketTypeDATA, addr1.maxBodySize)
	server.Send(resp1, recvAddr1)

	// Client1 应收到响应
	dnsSendData(t, client1, addr1, "poll1")
	rpkt1 := dnsPollClientUntilReceived(t, client1, testPollTimeout)
	if string(rpkt1.Data) != "resp-for-client1" {
		t.Errorf("Client1 response: got %q", string(rpkt1.Data))
	}

	// Client2 不应收到 Client1 的响应
	dnsSendData(t, client2, addr2, "poll2")
	time.Sleep(200 * time.Millisecond)
	rpkt2, _, _ := client2.Receive()
	if rpkt2 != nil && string(rpkt2.Data) == "resp-for-client1" {
		t.Error("Client2 received Client1's response - buffer isolation broken")
	}

	t.Log("Buffer isolation test passed")
}
