//go:build dns
// +build dns

package simplex

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/rem/x/encoders"
	"github.com/miekg/dns"
)

// ============================================================
// DNS Unit Tests — 纯函数测试，无需网络
//
// 运行:
//   go test -v -tags dns -run "TestDNS_" ./x/simplex/ -timeout 30s
// ============================================================

// --- Helper: 创建测试用 DNSServer（不启动网络监听） ---

func testDNSServer(domain string) *DNSServer {
	u, _ := url.Parse("dns://127.0.0.1:53?domain=" + domain)
	addr := &SimplexAddr{
		URL:         u,
		id:          "testconn",
		maxBodySize: MaxUDPMessageSize,
		options:     u.Query(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DNSServer{
		addr:   addr,
		domain: domain,
		ctx:    ctx,
		cancel: cancel,
	}
}

func testDNSServerFromURL(t *testing.T, rawURL string) *DNSServer {
	t.Helper()
	addr, err := ResolveDNSAddr("dns", rawURL)
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}
	config := getDNSConfig(addr)
	ctx, cancel := context.WithCancel(context.Background())
	return &DNSServer{
		addr:            addr,
		domain:          config.Domain,
		authoritativeNS: config.AuthoritativeNS,
		ctx:             ctx,
		cancel:          cancel,
	}
}

// --- Helper: 创建测试用 DNSClient（不启动网络连接） ---

func testDNSClient(domain, connID string, maxBodySize int) *DNSClient {
	addr := &SimplexAddr{
		id:          connID,
		maxBodySize: maxBodySize,
	}
	config := &DNSConfig{
		Domain: domain,
	}
	return &DNSClient{
		addr:   addr,
		config: config,
	}
}

// ============================================================
// 1. ResolveDNSAddr 测试
// ============================================================

func TestDNS_ResolveDNSAddr_Basic(t *testing.T) {
	addr, err := ResolveDNSAddr("dns", "dns://127.0.0.1:5353?domain=test.com&interval=100")
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}

	config := getDNSConfig(addr)
	if config.Domain != "test.com" {
		t.Errorf("Domain: got %q, want %q", config.Domain, "test.com")
	}
	if config.Protocol != "udp" {
		t.Errorf("Protocol: got %q, want %q", config.Protocol, "udp")
	}
	if config.Interval.Milliseconds() != 100 {
		t.Errorf("Interval: got %dms, want 100ms", config.Interval.Milliseconds())
	}
	if addr.Host != "127.0.0.1:5353" {
		t.Errorf("Host: got %q, want %q", addr.Host, "127.0.0.1:5353")
	}
}

func TestDNS_ResolveDNSAddr_DoH(t *testing.T) {
	addr, err := ResolveDNSAddr("doh", "doh://127.0.0.1:443?domain=test.com")
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}

	config := getDNSConfig(addr)
	if config.Protocol != "doh" {
		t.Errorf("Protocol: got %q, want %q", config.Protocol, "doh")
	}
	if config.HTTPPath != "/dns-query" {
		t.Errorf("HTTPPath: got %q, want %q", config.HTTPPath, "/dns-query")
	}
	if config.HTTPMethod != "POST" {
		t.Errorf("HTTPMethod: got %q, want %q", config.HTTPMethod, "POST")
	}
	if config.TLSConfig == nil {
		t.Error("TLSConfig should not be nil for DoH")
	}
}

func TestDNS_ResolveDNSAddr_DoT(t *testing.T) {
	addr, err := ResolveDNSAddr("dot", "dot://127.0.0.1:853?domain=test.com")
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}

	config := getDNSConfig(addr)
	if config.Protocol != "dot" {
		t.Errorf("Protocol: got %q, want %q", config.Protocol, "dot")
	}
	if config.TLSConfig == nil {
		t.Error("TLSConfig should not be nil for DoT")
	}
}

func TestDNS_ResolveDNSAddr_CustomParams(t *testing.T) {
	addr, err := ResolveDNSAddr("doh", "doh://10.0.0.1:8443?domain=evil.com&ns=ns.evil.com&interval=200&max=100&timeout=10&path=/custom&method=GET")
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}

	config := getDNSConfig(addr)
	if config.Domain != "evil.com" {
		t.Errorf("Domain: got %q, want %q", config.Domain, "evil.com")
	}
	if config.AuthoritativeNS != "ns.evil.com" {
		t.Errorf("AuthoritativeNS: got %q, want %q", config.AuthoritativeNS, "ns.evil.com")
	}
	if config.Interval.Milliseconds() != 200 {
		t.Errorf("Interval: got %dms, want 200ms", config.Interval.Milliseconds())
	}
	if config.MaxSize != 100 {
		t.Errorf("MaxSize: got %d, want 100", config.MaxSize)
	}
	if config.Timeout.Seconds() != 10 {
		t.Errorf("Timeout: got %vs, want 10s", config.Timeout.Seconds())
	}
	if config.HTTPPath != "/custom" {
		t.Errorf("HTTPPath: got %q, want %q", config.HTTPPath, "/custom")
	}
	if config.HTTPMethod != "GET" {
		t.Errorf("HTTPMethod: got %q, want %q", config.HTTPMethod, "GET")
	}
}

func TestDNS_ResolveDNSAddr_MissingDomain(t *testing.T) {
	_, err := ResolveDNSAddr("dns", "dns://127.0.0.1:53?interval=100")
	if err == nil {
		t.Fatal("Expected error for missing domain")
	}
	if !strings.Contains(err.Error(), "domain is required") {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestDNS_ResolveDNSAddr_DefaultMaxBodySize(t *testing.T) {
	addr, err := ResolveDNSAddr("dns", "dns://127.0.0.1:53?domain=example.com")
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}

	domainLen := len("example.com")
	available := MaxDomainLength - domainLen - 9 // connID(8) + dot(1)
	b58Chars := available * MaxLabelLength / (MaxLabelLength + 1)
	expected := int(float64(b58Chars) / 1.37)
	if addr.maxBodySize != expected {
		t.Errorf("MaxBodySize: got %d, want %d", addr.maxBodySize, expected)
	}
}

func TestDNS_ResolveDNSAddr_TCP(t *testing.T) {
	addr, err := ResolveDNSAddr("tcp", "tcp://127.0.0.1:53?domain=test.com")
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}

	config := getDNSConfig(addr)
	if config.Protocol != "tcp" {
		t.Errorf("Protocol: got %q, want %q", config.Protocol, "tcp")
	}
}

// ============================================================
// 2. getDNSConfig 测试
// ============================================================

func TestDNS_GetDNSConfig_FromAddr(t *testing.T) {
	addr, err := ResolveDNSAddr("dns", "dns://127.0.0.1:53?domain=test.com&interval=200&max=100")
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}

	config := getDNSConfig(addr)
	if config.Domain != "test.com" {
		t.Errorf("Domain: got %q, want %q", config.Domain, "test.com")
	}
	if config.MaxSize != 100 {
		t.Errorf("MaxSize: got %d, want 100", config.MaxSize)
	}
}

func TestDNS_GetDNSConfig_FallbackFromOptions(t *testing.T) {
	// 创建一个没有 config 的 addr，但 options 中有参数
	addr := &SimplexAddr{
		options: map[string][]string{
			"interval": {"300"},
			"max":      {"200"},
			"timeout":  {"15"},
			"cert":     {"/tmp/cert.pem"},
			"key":      {"/tmp/key.pem"},
			"ca":       {"/tmp/ca.pem"},
			"path":     {"/custom-path"},
			"method":   {"GET"},
		},
	}

	config := getDNSConfig(addr)
	if config.Interval.Milliseconds() != 300 {
		t.Errorf("Interval: got %dms, want 300ms", config.Interval.Milliseconds())
	}
	if config.MaxSize != 200 {
		t.Errorf("MaxSize: got %d, want 200", config.MaxSize)
	}
	if config.Timeout.Seconds() != 15 {
		t.Errorf("Timeout: got %vs, want 15s", config.Timeout.Seconds())
	}
	if config.CertFile != "/tmp/cert.pem" {
		t.Errorf("CertFile: got %q, want %q", config.CertFile, "/tmp/cert.pem")
	}
	if config.KeyFile != "/tmp/key.pem" {
		t.Errorf("KeyFile: got %q, want %q", config.KeyFile, "/tmp/key.pem")
	}
	if config.CAFile != "/tmp/ca.pem" {
		t.Errorf("CAFile: got %q, want %q", config.CAFile, "/tmp/ca.pem")
	}
	if config.HTTPPath != "/custom-path" {
		t.Errorf("HTTPPath: got %q, want %q", config.HTTPPath, "/custom-path")
	}
	if config.HTTPMethod != "GET" {
		t.Errorf("HTTPMethod: got %q, want %q", config.HTTPMethod, "GET")
	}
}

// ============================================================
// 3. splitIntoChunks 测试
// ============================================================

func TestDNS_SplitIntoChunks_Basic(t *testing.T) {
	client := testDNSClient("test.com", "c1", 200)
	input := strings.Repeat("a", 180)
	chunks := client.splitIntoChunks(input, MaxLabelLength)

	if len(chunks) != 3 {
		t.Fatalf("Expected 3 chunks, got %d", len(chunks))
	}
	if len(chunks[0]) != 63 {
		t.Errorf("Chunk 0 len: got %d, want 63", len(chunks[0]))
	}
	if len(chunks[1]) != 63 {
		t.Errorf("Chunk 1 len: got %d, want 63", len(chunks[1]))
	}
	if len(chunks[2]) != 54 {
		t.Errorf("Chunk 2 len: got %d, want 54", len(chunks[2]))
	}
}

func TestDNS_SplitIntoChunks_ExactMultiple(t *testing.T) {
	client := testDNSClient("test.com", "c1", 200)
	input := strings.Repeat("b", 126) // 63 * 2
	chunks := client.splitIntoChunks(input, MaxLabelLength)

	if len(chunks) != 2 {
		t.Fatalf("Expected 2 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) != 63 {
			t.Errorf("Chunk %d len: got %d, want 63", i, len(c))
		}
	}
}

func TestDNS_SplitIntoChunks_Empty(t *testing.T) {
	client := testDNSClient("test.com", "c1", 200)
	chunks := client.splitIntoChunks("", MaxLabelLength)

	if len(chunks) != 0 {
		t.Errorf("Expected 0 chunks for empty string, got %d", len(chunks))
	}
}

func TestDNS_SplitIntoChunks_ShortString(t *testing.T) {
	client := testDNSClient("test.com", "c1", 200)
	input := "hello"
	chunks := client.splitIntoChunks(input, MaxLabelLength)

	if len(chunks) != 1 {
		t.Fatalf("Expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != "hello" {
		t.Errorf("Chunk: got %q, want %q", chunks[0], "hello")
	}
}

// ============================================================
// 4. createMultiLabelQuery 测试
// ============================================================

func TestDNS_CreateMultiLabelQuery_SingleChunk(t *testing.T) {
	client := testDNSClient("test.com", "conn1", 200)
	query := client.createMultiLabelQuery([]string{"abc123"})

	expected := "abc123.conn1.test.com"
	if query != expected {
		t.Errorf("Query: got %q, want %q", query, expected)
	}
}

func TestDNS_CreateMultiLabelQuery_MultiChunks(t *testing.T) {
	client := testDNSClient("test.com", "conn1", 200)
	query := client.createMultiLabelQuery([]string{"chunk1", "chunk2", "chunk3"})

	expected := "chunk1.chunk2.chunk3.conn1.test.com"
	if query != expected {
		t.Errorf("Query: got %q, want %q", query, expected)
	}
}

func TestDNS_CreateMultiLabelQuery_EmptyChunk(t *testing.T) {
	client := testDNSClient("test.com", "conn1", 200)
	query := client.createMultiLabelQuery([]string{""})

	// TrimLeft removes the leading dot from ".conn1.test.com"
	expected := "conn1.test.com"
	if query != expected {
		t.Errorf("Query: got %q, want %q", query, expected)
	}
}

// ============================================================
// 5. processDNSQuery 测试
// ============================================================

func TestDNS_ProcessDNSQuery_DataExtraction(t *testing.T) {
	server := testDNSServer("test.com")
	defer server.Close()

	// 构造数据：编码 "hello" 为 B58，作为 DNS 查询标签
	rawData := NewSimplexPacket(SimplexPacketTypeDATA, []byte("hello"))
	encoded := encoders.B58Encode(rawData.Marshal())

	// 构造 DNS 查询: encoded.connid.test.com.
	queryName := encoded + ".myconn.test.com."
	msg := new(dns.Msg)
	msg.SetQuestion(queryName, dns.TypeTXT)

	resp := server.processDNSQuery(msg)
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	// 验证数据到达了 ReadBuf
	value, ok := server.buffers.Load("myconn")
	if !ok {
		t.Fatal("Expected buffer for 'myconn' connection ID")
	}
	buf := value.(*AsymBuffer)
	pkt, err := buf.ReadBuf().GetPacket()
	if err != nil {
		t.Fatalf("GetPacket error: %v", err)
	}
	if pkt == nil {
		t.Fatal("Expected packet in ReadBuf")
	}
	if string(pkt.Data) != "hello" {
		t.Errorf("Data: got %q, want %q", string(pkt.Data), "hello")
	}
}

func TestDNS_ProcessDNSQuery_PollingEmptyData(t *testing.T) {
	server := testDNSServer("test.com")
	defer server.Close()

	// 空数据轮询: connid.test.com.（没有数据标签）
	msg := new(dns.Msg)
	msg.SetQuestion("myconn.test.com.", dns.TypeTXT)

	resp := server.processDNSQuery(msg)
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	// pathAndDataLabels = ["myconn"], last = "myconn" (path), dataLabels = []
	// 没有数据标签，应该没有异常
	if len(resp.Answer) > 0 {
		t.Log("Response has answers (pending data if any)")
	}
}

func TestDNS_ProcessDNSQuery_DomainMismatch(t *testing.T) {
	server := testDNSServer("test.com")
	defer server.Close()

	msg := new(dns.Msg)
	msg.SetQuestion("data.connid.wrong.com.", dns.TypeTXT)

	resp := server.processDNSQuery(msg)
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	// 域名不匹配，应返回空响应
	if len(resp.Answer) > 0 {
		t.Error("Expected empty response for domain mismatch")
	}
}

func TestDNS_ProcessDNSQuery_ResponseDelivery(t *testing.T) {
	server := testDNSServer("test.com")
	defer server.Close()

	// 先在 WriteBuf 中预存数据
	addr := generateAddrFromPath("myconn", server.addr)
	buf := NewAsymBuffer(addr)
	server.buffers.Store("myconn", buf)

	respData := NewSimplexPacket(SimplexPacketTypeDATA, []byte("response"))
	respPkts := NewSimplexPackets()
	respPkts.Append(respData)
	buf.WriteBuf().PutPackets(respPkts)

	// 发送查询，触发响应
	msg := new(dns.Msg)
	msg.SetQuestion("myconn.test.com.", dns.TypeTXT)

	resp := server.processDNSQuery(msg)
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	// 验证 TXT 记录包含 B58 编码的响应数据
	if len(resp.Answer) == 0 {
		t.Fatal("Expected TXT answer in response")
	}

	txt, ok := resp.Answer[0].(*dns.TXT)
	if !ok {
		t.Fatal("Expected TXT record type")
	}
	if len(txt.Txt) == 0 {
		t.Fatal("Expected non-empty TXT data")
	}

	// 解码 TXT 数据
	combined := strings.Join(txt.Txt, "")
	decoded := encoders.B58Decode(combined)
	if len(decoded) == 0 {
		t.Fatal("Failed to B58 decode response")
	}

	// 解析为 SimplexPacket
	pkt, err := ParseSimplexPacket(decoded)
	if err != nil {
		t.Fatalf("ParseSimplexPacket error: %v", err)
	}
	if pkt == nil {
		t.Fatal("Expected parsed packet")
	}
	if string(pkt.Data) != "response" {
		t.Errorf("Response data: got %q, want %q", string(pkt.Data), "response")
	}
}

func TestDNS_ProcessDNSQuery_MultiLabelData(t *testing.T) {
	server := testDNSServer("test.com")
	defer server.Close()

	// 构造多标签数据
	rawData := NewSimplexPacket(SimplexPacketTypeDATA, []byte("multi-label-test"))
	encoded := encoders.B58Encode(rawData.Marshal())

	// 手动分割成多个标签（假设每个标签 10 字符）
	var labels []string
	for i := 0; i < len(encoded); i += 10 {
		end := i + 10
		if end > len(encoded) {
			end = len(encoded)
		}
		labels = append(labels, encoded[i:end])
	}

	// 构造查询: label1.label2...labelN.connid.test.com.
	queryName := strings.Join(labels, ".") + ".myconn.test.com."
	msg := new(dns.Msg)
	msg.SetQuestion(queryName, dns.TypeTXT)

	resp := server.processDNSQuery(msg)
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	// 验证数据正确到达
	value, ok := server.buffers.Load("myconn")
	if !ok {
		t.Fatal("Expected buffer for 'myconn'")
	}
	buf := value.(*AsymBuffer)
	pkt, err := buf.ReadBuf().GetPacket()
	if err != nil {
		t.Fatalf("GetPacket error: %v", err)
	}
	if pkt == nil {
		t.Fatal("Expected packet in ReadBuf")
	}
	if string(pkt.Data) != "multi-label-test" {
		t.Errorf("Data: got %q, want %q", string(pkt.Data), "multi-label-test")
	}
}

func TestDNS_ProcessDNSQuery_NSQuery(t *testing.T) {
	server := testDNSServer("test.com")
	defer server.Close()

	msg := new(dns.Msg)
	msg.SetQuestion("sub.test.com.", dns.TypeNS)

	resp := server.processDNSQuery(msg)
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	if len(resp.Answer) == 0 {
		t.Fatal("Expected NS record in answer")
	}

	ns, ok := resp.Answer[0].(*dns.NS)
	if !ok {
		t.Fatal("Expected NS record type")
	}
	if ns.Ns != dns.Fqdn("test.com") {
		t.Errorf("NS: got %q, want %q", ns.Ns, dns.Fqdn("test.com"))
	}
}

func TestDNS_ProcessDNSQuery_NSQueryConfiguredAuthority(t *testing.T) {
	server := testDNSServerFromURL(t, "dns://127.0.0.1:53?domain=cdn2.spacex666.tech&ns=cdn1.spacex666.tech")
	defer server.Close()

	msg := new(dns.Msg)
	msg.SetQuestion("cdn2.spacex666.tech.", dns.TypeNS)

	resp := server.processDNSQuery(msg)
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Answer) == 0 {
		t.Fatal("Expected NS record in answer")
	}
	ns, ok := resp.Answer[0].(*dns.NS)
	if !ok {
		t.Fatal("Expected NS record type")
	}
	if ns.Ns != dns.Fqdn("cdn1.spacex666.tech") {
		t.Errorf("NS: got %q, want %q", ns.Ns, dns.Fqdn("cdn1.spacex666.tech"))
	}
}

// ============================================================
// 6. B58 DNS 上下文 round-trip 测试
// ============================================================

func TestDNS_B58_RoundTrip_DNSContext(t *testing.T) {
	client := testDNSClient("test.com", "c1", 200)

	testCases := [][]byte{
		[]byte("hello world"),
		[]byte{0x00, 0x01, 0x02, 0xff, 0xfe},
		[]byte(strings.Repeat("A", 100)),
		[]byte{0x00, 0x00, 0x00}, // leading zeros
	}

	for i, data := range testCases {
		encoded := encoders.B58Encode(data)
		chunks := client.splitIntoChunks(encoded, MaxLabelLength)
		joined := strings.Join(chunks, "")
		decoded := encoders.B58Decode(joined)

		if len(decoded) != len(data) {
			t.Errorf("Case %d: length mismatch: got %d, want %d", i, len(decoded), len(data))
			continue
		}
		for j := range data {
			if decoded[j] != data[j] {
				t.Errorf("Case %d: byte %d: got 0x%02x, want 0x%02x", i, j, decoded[j], data[j])
				break
			}
		}
	}
}

func TestDNS_MaxBodySize_Calculation(t *testing.T) {
	domains := []string{"t.co", "test.com", "example.com", "very-long-domain-name.example.org"}

	for _, domain := range domains {
		addr, err := ResolveDNSAddr("dns", "dns://127.0.0.1:53?domain="+domain)
		if err != nil {
			t.Fatalf("ResolveDNSAddr error for %q: %v", domain, err)
		}

		available := MaxDomainLength - len(domain) - 9
		b58Chars := available * MaxLabelLength / (MaxLabelLength + 1)
		expected := int(float64(b58Chars) / 1.37)
		if addr.maxBodySize != expected {
			t.Errorf("Domain %q: maxBodySize got %d, want %d", domain, addr.maxBodySize, expected)
		}
		t.Logf("Domain %q: maxBodySize=%d (domain_len=%d)", domain, addr.maxBodySize, len(domain))
	}
}

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
	domain := "t.co" // 4 chars
	connID := "c1"   // 2 chars
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

func TestDNS_E2E_UDP_MultiPacketBatch(t *testing.T) {
	server, client, addr := dnsSetupUDP(t)

	clientBatch := NewSimplexPackets()
	clientBatch.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("client-one")))
	clientBatch.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("client-two")))
	if _, err := client.Send(clientBatch, addr); err != nil {
		t.Fatalf("client Send batch error: %v", err)
	}

	first, recvAddr := dnsPollServerUntilReceived(t, server, testPollTimeout)
	second, _ := dnsPollServerUntilReceived(t, server, testPollTimeout)
	if string(first.Data) != "client-one" {
		t.Fatalf("server first packet = %q, want %q", string(first.Data), "client-one")
	}
	if string(second.Data) != "client-two" {
		t.Fatalf("server second packet = %q, want %q", string(second.Data), "client-two")
	}

	serverBatch := NewSimplexPackets()
	serverBatch.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("server-one")))
	serverBatch.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("server-two")))
	if _, err := server.Send(serverBatch, recvAddr); err != nil {
		t.Fatalf("server Send batch error: %v", err)
	}

	if _, err := client.Send(NewSimplexPackets(), addr); err != nil {
		t.Fatalf("client polling Send error: %v", err)
	}
	respFirst := dnsPollClientUntilReceived(t, client, testPollTimeout)
	respSecond := dnsPollClientUntilReceived(t, client, testPollTimeout)
	if string(respFirst.Data) != "server-one" {
		t.Fatalf("client first packet = %q, want %q", string(respFirst.Data), "server-one")
	}
	if string(respSecond.Data) != "server-two" {
		t.Fatalf("client second packet = %q, want %q", string(respSecond.Data), "server-two")
	}
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

	// Client Receive should issue an empty polling query to trigger response delivery.
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
				data[i] = byte((i*7 + 13) % 256)
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

// ============================================================
// DNS Semantics Tests
// ============================================================

func TestDNSClientCloseReturnsClosedPipe(t *testing.T) {
	addr, err := ResolveDNSAddr("dns", "dns://127.0.0.1:53?domain=test.com&interval=50&timeout=1")
	if err != nil {
		t.Fatalf("ResolveDNSAddr: %v", err)
	}

	client, err := NewDNSClient(addr)
	if err != nil {
		t.Fatalf("NewDNSClient: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := client.Send(NewSimplexPacketWithMaxSize([]byte("x"), SimplexPacketTypeDATA, addr.MaxBodySize()), addr); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Send after close = %v, want %v", err, io.ErrClosedPipe)
	}
	if _, _, err := client.Receive(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Receive after close = %v, want %v", err, io.ErrClosedPipe)
	}
}

// ============================================================
// DNS Registry Matrix Tests
// ============================================================

func TestRegisteredDNSSimplexResolvers(t *testing.T) {
	cases := []struct {
		scheme  string
		address string
	}{
		{scheme: "dns", address: "dns://127.0.0.1:53/?domain=test.local&interval=100&max=128&wrapper=raw"},
		{scheme: "doh", address: "doh://dns.google/dns-query?domain=test.local&interval=100&max=128&wrapper=raw"},
		{scheme: "dot", address: "dot://1.1.1.1:853/?domain=test.local&interval=100&max=128&wrapper=raw"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.scheme, func(t *testing.T) {
			if _, err := GetSimplexClient(tc.scheme); err != nil {
				t.Fatalf("GetSimplexClient(%q) error: %v", tc.scheme, err)
			}
			if _, err := GetSimplexServer(tc.scheme); err != nil {
				t.Fatalf("GetSimplexServer(%q) error: %v", tc.scheme, err)
			}

			addr, err := ResolveSimplexAddr(tc.scheme, tc.address)
			if err != nil {
				t.Fatalf("ResolveSimplexAddr(%q) error: %v", tc.scheme, err)
			}
			if got := addr.Network(); got != tc.scheme {
				t.Fatalf("unexpected simplex addr network: got %q want %q", got, tc.scheme)
			}
		})
	}
}
