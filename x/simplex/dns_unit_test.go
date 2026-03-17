//go:build dns
// +build dns

package simplex

import (
	"context"
	"net/url"
	"strings"
	"testing"

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
	addr, err := ResolveDNSAddr("doh", "doh://10.0.0.1:8443?domain=evil.com&interval=200&max=100&timeout=10&path=/custom&method=GET")
	if err != nil {
		t.Fatalf("ResolveDNSAddr error: %v", err)
	}

	config := getDNSConfig(addr)
	if config.Domain != "evil.com" {
		t.Errorf("Domain: got %q, want %q", config.Domain, "evil.com")
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
	available := MaxDomainLength - domainLen - 9             // connID(8) + dot(1)
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
