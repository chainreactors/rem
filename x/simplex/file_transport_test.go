package simplex

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================
// mockStorageOps — in-memory FileStorageOps for testing
// ============================================================

type mockStorageOps struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newMockStorageOps() *mockStorageOps {
	return &mockStorageOps{files: make(map[string][]byte)}
}

func (m *mockStorageOps) ReadFile(path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found %s: %w", path, os.ErrNotExist)
	}
	return append([]byte(nil), data...), nil
}

func (m *mockStorageOps) WriteFile(path string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = append([]byte(nil), data...)
	return nil
}

func (m *mockStorageOps) DeleteFile(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, path)
	return nil
}

func (m *mockStorageOps) ListFiles(folderPath string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Treat folderPath as a directory: ensure trailing "/"
	prefix := folderPath
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var result []string
	seen := make(map[string]bool)
	for path := range m.files {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		name := path[len(prefix):]
		if strings.Contains(name, "/") {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, name)
	}
	return result, nil
}

func (m *mockStorageOps) ListDirs(folderPath string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := folderPath
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var result []string
	seen := make(map[string]bool)
	for path := range m.files {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		name := path[len(prefix):]
		idx := strings.Index(name, "/")
		if idx < 0 {
			continue
		}
		dir := name[:idx+1]
		if seen[dir] {
			continue
		}
		seen[dir] = true
		result = append(result, dir)
	}
	return result, nil
}

func (m *mockStorageOps) FileExists(path string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[path]
	return ok, nil
}

// helper methods for tests
func (m *mockStorageOps) hasFile(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[path]
	return ok
}

func (m *mockStorageOps) getFile(path string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.files[path]
}

func (m *mockStorageOps) putFile(path string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = append([]byte(nil), data...)
}

func waitNoFile(t *testing.T, mock *mockStorageOps, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !mock.hasFile(path) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s should have been deleted", path)
}

// failingStorageOps wraps mockStorageOps with controllable failures
type failingStorageOps struct {
	*mockStorageOps
	mu             sync.Mutex
	readFailCount  int
	writeFailCount int
}

func newFailingStorageOps() *failingStorageOps {
	return &failingStorageOps{mockStorageOps: newMockStorageOps()}
}

func (f *failingStorageOps) ReadFile(path string) ([]byte, error) {
	f.mu.Lock()
	if f.readFailCount > 0 {
		f.readFailCount--
		f.mu.Unlock()
		return nil, fmt.Errorf("simulated read failure")
	}
	f.mu.Unlock()
	return f.mockStorageOps.ReadFile(path)
}

func (f *failingStorageOps) WriteFile(path string, data []byte) error {
	f.mu.Lock()
	if f.writeFailCount > 0 {
		f.writeFailCount--
		f.mu.Unlock()
		return fmt.Errorf("simulated write failure")
	}
	f.mu.Unlock()
	return f.mockStorageOps.WriteFile(path, data)
}

func (f *failingStorageOps) DeleteFile(path string) error {
	return f.mockStorageOps.DeleteFile(path)
}

func (f *failingStorageOps) ListFiles(prefix string) ([]string, error) {
	return f.mockStorageOps.ListFiles(prefix)
}

func (f *failingStorageOps) FileExists(path string) (bool, error) {
	return f.mockStorageOps.FileExists(path)
}

// recvFailStorageOps makes ReadFile fail for a specific path with a non-404 error
type recvFailStorageOps struct {
	*mockStorageOps
	failPath string
}

func (m *recvFailStorageOps) ReadFile(path string) ([]byte, error) {
	if path == m.failPath {
		return nil, fmt.Errorf("simulated recv read failure for %s", path)
	}
	return m.mockStorageOps.ReadFile(path)
}

func (m *recvFailStorageOps) WriteFile(path string, data []byte) error {
	return m.mockStorageOps.WriteFile(path, data)
}

func (m *recvFailStorageOps) DeleteFile(path string) error {
	return m.mockStorageOps.DeleteFile(path)
}

func (m *recvFailStorageOps) ListFiles(prefix string) ([]string, error) {
	return m.mockStorageOps.ListFiles(prefix)
}

func (m *recvFailStorageOps) FileExists(path string) (bool, error) {
	return m.mockStorageOps.FileExists(path)
}

type trackingStorageOps struct {
	*mockStorageOps
	mu         sync.Mutex
	listCalls  int
	readPaths  []string
	deletePath []string
}

func newTrackingStorageOps() *trackingStorageOps {
	return &trackingStorageOps{mockStorageOps: newMockStorageOps()}
}

func (m *trackingStorageOps) ReadFile(path string) ([]byte, error) {
	m.mu.Lock()
	m.readPaths = append(m.readPaths, path)
	m.mu.Unlock()
	return m.mockStorageOps.ReadFile(path)
}

func (m *trackingStorageOps) DeleteFile(path string) error {
	m.mu.Lock()
	m.deletePath = append(m.deletePath, path)
	m.mu.Unlock()
	return m.mockStorageOps.DeleteFile(path)
}

func (m *trackingStorageOps) ListFiles(prefix string) ([]string, error) {
	m.mu.Lock()
	m.listCalls++
	m.mu.Unlock()
	return m.mockStorageOps.ListFiles(prefix)
}

func (m *trackingStorageOps) stats() (int, []string, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listCalls, append([]string(nil), m.readPaths...), append([]string(nil), m.deletePath...)
}

// ============================================================
// Test config / helpers
// ============================================================

func testFileTransportConfig(logPrefix string) FileTransportConfig {
	return FileTransportConfig{
		SendSuffix:     "_send.txt",
		RecvSuffix:     "_recv.txt",
		Interval:       50 * time.Millisecond,
		MaxBodySize:    2 * 1024 * 1024,
		IdleMultiplier: fileHandlerIdleMultiplier,
		MaxFailures:    10,
		LogPrefix:      logPrefix,
	}
}

func testAddr() *SimplexAddr {
	u, _ := url.Parse("test:///testdir")
	return &SimplexAddr{
		URL:         u,
		maxBodySize: 2 * 1024 * 1024,
		options:     u.Query(),
	}
}

// ============================================================
// Server Tests
// ============================================================

func TestFileTransport_Server_ScanForNewClients(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[Test]")
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewFileTransportServer(mock, "dir/", cfg, addr, ctx, cancel)

	// Place send files for two clients
	mock.putFile("dir/clientA_send.txt", []byte("dummy"))
	mock.putFile("dir/clientB_send.txt", []byte("dummy"))

	srv.scanForNewClients()

	count := 0
	srv.clients.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	if count != 2 {
		t.Errorf("expected 2 clients, got %d", count)
	}

	// Re-scan should not duplicate
	srv.scanForNewClients()
	count2 := 0
	srv.clients.Range(func(_, _ interface{}) bool {
		count2++
		return true
	})
	if count2 != 2 {
		t.Errorf("repeated scan created duplicates, got %d", count2)
	}
}

func TestFileTransport_Server_HandleClient_SendRecvCycle(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[Test]")
	addr := testAddr()

	clientID := "testcli"
	clientAddr := generateAddrFromPath(clientID, addr)
	state := &fileClientState{
		inBuffer:     NewSimplexBuffer(clientAddr),
		outBuffer:    NewSimplexBuffer(clientAddr),
		addr:         clientAddr,
		lastActivity: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewFileTransportServer(mock, "testdir/", cfg, addr, ctx, cancel)

	// Simulate client uploading send file
	clientData := []byte("client payload")
	clientPkts := NewSimplexPacketWithMaxSize(clientData, SimplexPacketTypeDATA, cfg.MaxBodySize)
	mock.putFile(fmt.Sprintf("testdir/%s_send.txt", clientID), clientPkts.Marshal())

	go srv.handleClient(ctx, clientID, state)

	// Wait for inBuffer to have data
	deadline := time.Now().Add(2 * time.Second)
	var pkt *SimplexPacket
	for time.Now().Before(deadline) {
		pkt, _ = state.inBuffer.GetPacket()
		if pkt != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if pkt == nil {
		t.Fatal("handleClient did not process send file")
	}
	if string(pkt.Data) != "client payload" {
		t.Errorf("got %q, want %q", string(pkt.Data), "client payload")
	}

	// Put data into outBuffer, verify handleClient writes recv file
	serverPkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("server response"))
	state.outBuffer.PutPacket(serverPkt)

	recvPath := fmt.Sprintf("testdir/%s_recv.txt", clientID)
	deadline = time.Now().Add(2 * time.Second)
	for !mock.hasFile(recvPath) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if !mock.hasFile(recvPath) {
		t.Fatal("handleClient did not write recv file")
	}

	recvContent := mock.getFile(recvPath)
	recvParsed, err := ParseSimplexPackets(recvContent)
	if err != nil {
		t.Fatalf("parse recv file error: %v", err)
	}
	if len(recvParsed.Packets) != 1 || string(recvParsed.Packets[0].Data) != "server response" {
		t.Errorf("unexpected recv file content")
	}
}

func TestFileTransport_Server_Receive(t *testing.T) {
	addr := testAddr()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := testFileTransportConfig("[Test]")
	srv := NewFileTransportServer(nil, "", cfg, addr, ctx, cancel)

	clientAddr := &SimplexAddr{maxBodySize: 2 * 1024 * 1024}
	state := &fileClientState{
		inBuffer:  NewSimplexBuffer(clientAddr),
		outBuffer: NewSimplexBuffer(clientAddr),
		addr:      clientAddr,
	}
	srv.clients.Store("test", state)

	// Empty inBuffer → nil
	pkt, _, err := srv.Receive()
	if err != nil {
		t.Fatalf("Receive error: %v", err)
	}
	if pkt != nil {
		t.Error("expected nil from empty inBuffer")
	}

	// Data in inBuffer → should receive
	state.inBuffer.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte("in-data")))
	pkt, recvAddr, _ := srv.Receive()
	if pkt == nil || string(pkt.Data) != "in-data" {
		t.Errorf("expected 'in-data', got %v", pkt)
	}
	if recvAddr != clientAddr {
		t.Error("wrong addr returned")
	}
}

func TestFileTransport_Server_Send(t *testing.T) {
	addr := testAddr()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := testFileTransportConfig("[Test]")
	srv := NewFileTransportServer(nil, "", cfg, addr, ctx, cancel)

	clientAddr := generateAddrFromPath("test-client", addr)
	state := &fileClientState{
		inBuffer:  NewSimplexBuffer(clientAddr),
		outBuffer: NewSimplexBuffer(clientAddr),
		addr:      clientAddr,
	}
	srv.clients.Store(clientAddr.Path, state)

	pkts := NewSimplexPacketWithMaxSize([]byte("server-msg"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	n, err := srv.Send(pkts, clientAddr)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if n == 0 {
		t.Error("expected non-zero bytes")
	}

	pkt, _ := state.outBuffer.GetPacket()
	if pkt == nil || string(pkt.Data) != "server-msg" {
		t.Errorf("outBuffer: expected 'server-msg', got %v", pkt)
	}
}

// ============================================================
// Client Tests
// ============================================================

func TestFileTransport_Client_SendWritesFile(t *testing.T) {
	mock := newMockStorageOps()
	addr := testAddr()
	cfg := testFileTransportConfig("[Test]")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr), "s.txt", "r.txt", addr, ctx, cancel)

	data := []byte("send test")
	pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeDATA, cfg.MaxBodySize)

	n, err := ftc.Send(pkts, addr)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if n == 0 {
		t.Error("expected non-zero bytes")
	}
	if !mock.hasFile("s.txt") {
		t.Error("expected send file to be written")
	}
}

func TestFileTransport_Client_Monitoring_SendCycle(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[Test]")
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr),
		"dir/test_send.txt", "dir/test_recv.txt", addr, ctx, cancel)

	pkts := NewSimplexPacketWithMaxSize([]byte("monitoring test"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	if _, err := ftc.Send(pkts, addr); err != nil {
		t.Fatalf("Send error: %v", err)
	}

	if !mock.hasFile("dir/test_send.txt") {
		t.Fatal("Send did not write send file")
	}

	content := mock.getFile("dir/test_send.txt")
	parsed, err := ParseSimplexPackets(content)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(parsed.Packets) != 1 || string(parsed.Packets[0].Data) != "monitoring test" {
		t.Errorf("unexpected file content")
	}
}

func TestFileTransport_Client_SinglePrioritizesControlBeforeData(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[ClientPriority]")
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sendFile := "dir/priority_send.txt"
	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr),
		sendFile, "dir/priority_recv.txt", addr, ctx, cancel)

	// In inline I/O mode, each Send() writes immediately.
	// Send CTRL first — it should write the file.
	ctrlPkts := NewSimplexPackets()
	ctrlPkts.Append(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("window-update")))
	if _, err := ftc.Send(ctrlPkts, addr); err != nil {
		t.Fatalf("Send CTRL: %v", err)
	}

	if !mock.hasFile(sendFile) {
		t.Fatal("Send did not write send file")
	}

	parsed, err := ParseSimplexPackets(mock.getFile(sendFile))
	if err != nil {
		t.Fatalf("ParseSimplexPackets: %v", err)
	}
	if len(parsed.Packets) != 1 {
		t.Fatalf("packet count = %d, want 1", len(parsed.Packets))
	}
	if got := parsed.Packets[0]; got.PacketType != SimplexPacketTypeCTRL || string(got.Data) != "window-update" {
		t.Fatalf("first uploaded packet = (%d,%q), want CTRL window-update", got.PacketType, string(got.Data))
	}
}

func TestFileTransport_Client_Monitoring_RecvCycle(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[Test]")
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buffer := NewSimplexBuffer(addr)
	ftc := NewFileTransportClient(mock, cfg, buffer,
		"dir/test_send.txt", "dir/test_recv.txt", addr, ctx, cancel)

	recvData := []byte("response from server")
	recvPkts := NewSimplexPacketWithMaxSize(recvData, SimplexPacketTypeDATA, cfg.MaxBodySize)
	mock.putFile("dir/test_recv.txt", recvPkts.Marshal())

	pkt, _, err := ftc.Receive()
	if err != nil {
		t.Fatalf("Receive error: %v", err)
	}
	if pkt == nil {
		t.Fatal("Receive did not return packet")
	}
	if string(pkt.Data) != "response from server" {
		t.Errorf("got %q, want %q", string(pkt.Data), "response from server")
	}

	if mock.hasFile("dir/test_recv.txt") {
		t.Error("recv file should have been deleted")
	}
}

// ============================================================
// Zombie Fix Tests — verify all 3 zombie fixes work
// ============================================================

// TestFileTransport_Zombie_StaleSendFile verifies that Send() returns an error
// when the sendFile already exists (server not consuming).
func TestFileTransport_Zombie_StaleSendFile(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[Test]")
	addr := testAddr()

	mock.putFile("dir/test_send.txt", []byte("old data"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr),
		"dir/test_send.txt", "dir/test_recv.txt", addr, ctx, cancel)

	pkts := NewSimplexPacketWithMaxSize([]byte("new data"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	_, err := ftc.Send(pkts, addr)
	if err == nil {
		t.Fatal("Send should return error when sendFile exists (stall)")
	}
}

// TestFileTransport_Zombie_PutBack verifies Fix #2:
// Server handleClient puts back packets on WriteFile failure.
func TestFileTransport_Zombie_PutBack(t *testing.T) {
	mock := newFailingStorageOps()
	mock.mu.Lock()
	mock.writeFailCount = 100
	mock.mu.Unlock()

	cfg := testFileTransportConfig("[Test]")
	cfg.Interval = 20 * time.Millisecond
	addr := testAddr()

	clientAddr := generateAddrFromPath("zombie2", addr)
	state := &fileClientState{
		inBuffer:     NewSimplexBuffer(clientAddr),
		outBuffer:    NewSimplexBuffer(clientAddr),
		addr:         clientAddr,
		lastActivity: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	srv := NewFileTransportServer(mock, "testdir/", cfg, addr, ctx, cancel)
	_ = srv

	clientID := "zombie2"
	sendFile := fmt.Sprintf("testdir/%s_send.txt", clientID)

	// Keep sendFile updated to prevent idle timeout
	mock.putFile(sendFile, []byte("keepalive"))

	// Put 3 packets in outBuffer
	for i := 0; i < 3; i++ {
		pkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte(fmt.Sprintf("data-%d", i)))
		state.outBuffer.PutPacket(pkt)
	}

	go srv.handleClient(ctx, clientID, state)

	// Wait for handleClient to attempt WriteFile (and fail + put-back)
	time.Sleep(200 * time.Millisecond)

	cancel()
	time.Sleep(50 * time.Millisecond)

	// outBuffer should still have data (put-back worked)
	var recovered int
	for {
		pkt, _ := state.outBuffer.GetPacket()
		if pkt == nil {
			break
		}
		recovered++
	}
	if recovered == 0 {
		t.Fatal("outBuffer is empty — data was lost despite put-back fix")
	}
	t.Logf("Zombie #2 fixed: %d packets preserved in outBuffer", recovered)
}

// TestFileTransport_Zombie_RecvFailure verifies that Receive() returns nil
// when ReadFile fails with non-404 error (transient failure).
func TestFileTransport_Zombie_RecvFailure(t *testing.T) {
	baseMock := newMockStorageOps()
	mock := &recvFailStorageOps{
		mockStorageOps: baseMock,
		failPath:       "dir/test_recv.txt",
	}

	cfg := testFileTransportConfig("[Test]")
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr),
		"dir/test_send.txt", "dir/test_recv.txt", addr, ctx, cancel)

	pkt, _, err := ftc.Receive()
	if err != nil {
		t.Fatalf("Receive should not return error for transient failures: %v", err)
	}
	if pkt != nil {
		t.Fatal("Receive should return nil packet on failure")
	}
}

// TestFileTransport_Server_IdleCleanup verifies idle timeout cleanup.
func TestFileTransport_Server_IdleCleanup(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[Test]")
	cfg.Interval = 20 * time.Millisecond
	cfg.IdleMultiplier = 5 // 5 × 20ms = 100ms idle timeout

	addr := testAddr()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewFileTransportServer(mock, "dir/", cfg, addr, ctx, cancel)

	// Create a client manually (without placing send file to trigger activity)
	clientID := "idle-cli"
	clientAddr := generateAddrFromPath(clientID, addr)
	clientCtx, clientCancel := context.WithCancel(ctx)
	state := &fileClientState{
		inBuffer:     NewSimplexBuffer(clientAddr),
		outBuffer:    NewSimplexBuffer(clientAddr),
		addr:         clientAddr,
		cancel:       clientCancel,
		lastActivity: time.Now(),
	}
	srv.clients.Store(clientID, state)

	go srv.handleClient(clientCtx, clientID, state)

	// Wait for idle timeout + cleanup
	time.Sleep(300 * time.Millisecond)

	// Client should be removed from map
	if _, exists := srv.clients.Load(clientID); exists {
		t.Fatal("Expected handler to be cleaned up after idle timeout")
	}
}

// TestFileTransport_Client_ErrorPropagation verifies Send() returns error on write failure.
func TestFileTransport_Client_ErrorPropagation(t *testing.T) {
	mock := newFailingStorageOps()
	mock.mu.Lock()
	mock.writeFailCount = 100
	mock.mu.Unlock()

	cfg := testFileTransportConfig("[Test]")
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr),
		"dir/errprop_send.txt", "dir/errprop_recv.txt", addr, ctx, cancel)

	pkts := NewSimplexPacketWithMaxSize([]byte("will fail"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	_, err := ftc.Send(pkts, addr)
	if err == nil {
		t.Fatal("Send should return error when WriteFile fails")
	}
}

// ============================================================
// Sequence Mode Tests
// ============================================================

func testSeqFileTransportConfig(logPrefix string) FileTransportConfig {
	cfg := testFileTransportConfig(logPrefix)
	cfg.SequenceMode = true
	return cfg
}

func TestSeqFilename_Deterministic(t *testing.T) {
	a := seqFilename("dir/", "cli1", "seed123", "s", 0)
	b := seqFilename("dir/", "cli1", "seed123", "s", 0)
	if a != b {
		t.Fatalf("seqFilename not deterministic: %s != %s", a, b)
	}

	c := seqFilename("dir/", "cli1", "seed123", "s", 1)
	if a == c {
		t.Fatalf("different seq should give different filenames: %s == %s", a, c)
	}

	d := seqFilename("dir/", "cli1", "seed123", "r", 0)
	if a == d {
		t.Fatalf("different direction should give different filenames: %s == %s", a, d)
	}

	if !strings.HasPrefix(a, "dir/cli1.s.") {
		t.Fatalf("expected prefix dir/cli1.s., got %s", a)
	}

	ref, ok := parseSeqFilename(strings.TrimPrefix(a, "dir/"), "seed123")
	if !ok {
		t.Fatalf("parseSeqFilename failed for %s", a)
	}
	if ref.clientID != "cli1" || ref.direction != "s" || ref.seq != 0 {
		t.Fatalf("unexpected parsed seq file: %#v", ref)
	}
}

func TestSeqMode_Server_ScanForNewClients(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqTest]")
	addr := testAddr()
	addr.id = "testseed"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewFileTransportServer(mock, "dir/", cfg, addr, ctx, cancel)

	// Place sequenced files for two clients. Server sequence seed is its dir prefix.
	seed := "dir/"
	fname1 := seqFilename("dir/", "clientA", seed, "s", 0)
	fname2 := seqFilename("dir/", "clientB", seed, "s", 0)
	mock.putFile(fname1, []byte("dummy"))
	mock.putFile(fname2, []byte("dummy"))

	srv.scanForNewClients()

	count := 0
	srv.clients.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	if count != 2 {
		t.Errorf("expected 2 clients, got %d", count)
	}

	// Re-scan should not duplicate
	srv.scanForNewClients()
	count2 := 0
	srv.clients.Range(func(_, _ interface{}) bool {
		count2++
		return true
	})
	if count2 != 2 {
		t.Errorf("repeated scan created duplicates, got %d", count2)
	}
}

func TestSeqMode_Server_HandleClient_SendRecvCycle(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqTest]")
	addr := testAddr()

	clientID := "seqcli"
	seed := "test-seed-42"
	clientAddr := generateAddrFromPath(clientID, addr)
	state := &fileClientState{
		inBuffer:     NewSimplexBuffer(clientAddr),
		outBuffer:    NewSimplexBuffer(clientAddr),
		addr:         clientAddr,
		lastActivity: time.Now(),
		seed:         seed,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewFileTransportServer(mock, "testdir/", cfg, addr, ctx, cancel)

	// Simulate client uploading seq=0 and seq=1 send files
	fname := seqFilename("testdir/", clientID, seed, "s", 0)
	clientPkts := NewSimplexPacketWithMaxSize([]byte("seq payload 0"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	mock.putFile(fname, clientPkts.Marshal())

	fname1 := seqFilename("testdir/", clientID, seed, "s", 1)
	clientPkts1 := NewSimplexPacketWithMaxSize([]byte("seq payload 1"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	mock.putFile(fname1, clientPkts1.Marshal())

	go srv.handleClient(ctx, clientID, state)

	// Wait for both packets to arrive in inBuffer
	deadline := time.Now().Add(2 * time.Second)
	var received []string
	for time.Now().Before(deadline) && len(received) < 2 {
		pkt, _ := state.inBuffer.GetPacket()
		if pkt != nil {
			received = append(received, string(pkt.Data))
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 packets, got %d", len(received))
	}
	if received[0] != "seq payload 0" || received[1] != "seq payload 1" {
		t.Errorf("unexpected payloads: %v", received)
	}

	// Verify send files were deleted
	if mock.hasFile(fname) || mock.hasFile(fname1) {
		t.Error("send files should be deleted after processing")
	}

	// Put data into outBuffer, verify handleClient writes sequenced recv file
	serverPkt := NewSimplexPacket(SimplexPacketTypeDATA, []byte("seq response"))
	state.outBuffer.PutPacket(serverPkt)

	recvFname := seqFilename("testdir/", clientID, seed, "r", 0)
	deadline = time.Now().Add(2 * time.Second)
	for !mock.hasFile(recvFname) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if !mock.hasFile(recvFname) {
		t.Fatal("handleClient did not write sequenced recv file")
	}

	recvContent := mock.getFile(recvFname)
	recvParsed, err := ParseSimplexPackets(recvContent)
	if err != nil {
		t.Fatalf("parse recv file error: %v", err)
	}
	if len(recvParsed.Packets) != 1 || string(recvParsed.Packets[0].Data) != "seq response" {
		t.Errorf("unexpected recv file content")
	}
}

func TestSeqMode_Server_UploadsControlBeforePendingDataBacklog(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqPriority]")
	addr := testAddr()
	clientID := "srvcli"
	seed := "server-priority-seed"
	clientAddr := generateAddrFromPath(clientID, addr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewFileTransportServer(mock, "dir/", cfg, addr, ctx, cancel)
	state := &fileClientState{
		inBuffer:  NewSimplexBuffer(clientAddr),
		outBuffer: NewSimplexBuffer(clientAddr),
		addr:      clientAddr,
		seed:      seed,
	}

	for seq := 0; seq < seqMaxOpsPerTick; seq++ {
		state.pendingWrites = append(state.pendingWrites, seqPendingFile{
			seq:  seq,
			data: NewSimplexPacketWithMaxSize([]byte(fmt.Sprintf("data-%d", seq)), SimplexPacketTypeDATA, cfg.MaxBodySize).Marshal(),
		})
	}
	state.writeSeq = seqMaxOpsPerTick
	if err := state.outBuffer.PutPacket(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("window-update"))); err != nil {
		t.Fatalf("PutPacket CTRL: %v", err)
	}

	if wrote := srv.writeSequenceFiles(clientID, state); wrote == 0 {
		t.Fatal("writeSequenceFiles wrote no files")
	}

	ctrlFile := seqFilename("dir/", clientID, seed, "r", seqMaxOpsPerTick)
	if !mock.hasFile(ctrlFile) {
		t.Fatalf("CTRL seq file %s was not uploaded ahead of DATA backlog", ctrlFile)
	}
	parsed, err := ParseSimplexPackets(mock.getFile(ctrlFile))
	if err != nil {
		t.Fatalf("ParseSimplexPackets CTRL: %v", err)
	}
	if len(parsed.Packets) != 1 || parsed.Packets[0].PacketType != SimplexPacketTypeCTRL ||
		string(parsed.Packets[0].Data) != "window-update" {
		t.Fatalf("CTRL file content = %#v", parsed.Packets)
	}
}

func TestSeqMode_Client_Monitoring_SendCycle(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqTest]")
	addr := testAddr()
	seed := "client-seed"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buffer := NewSimplexBuffer(addr)
	ftc := NewFileTransportClient(mock, cfg, buffer, "", "", addr, ctx, cancel)
	ftc.prefix = "dir/"
	ftc.clientID = "seqcli"
	ftc.seed = seed

	pkts := NewSimplexPacketWithMaxSize([]byte("seq send data"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	if _, err := ftc.Send(pkts, addr); err != nil {
		t.Fatalf("Send error: %v", err)
	}

	expectedFile := seqFilename("dir/", "seqcli", seed, "s", 0)
	if !mock.hasFile(expectedFile) {
		t.Fatal("Send did not write sequenced send file")
	}

	content := mock.getFile(expectedFile)
	parsed, err := ParseSimplexPackets(content)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(parsed.Packets) != 1 || string(parsed.Packets[0].Data) != "seq send data" {
		t.Errorf("unexpected file content")
	}
}

func TestSeqMode_Client_UploadsControlBeforePendingDataBacklog(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqPriority]")
	addr := testAddr()
	seed := "client-priority-seed"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr), "", "", addr, ctx, cancel)
	ftc.prefix = "dir/"
	ftc.clientID = "seqcli"
	ftc.seed = seed

	// Send a mixed packet batch with CTRL and DATA
	mixed := NewSimplexPackets()
	mixed.Append(NewSimplexPacket(SimplexPacketTypeDATA, []byte("data-payload")))
	mixed.Append(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("window-update")))
	if _, err := ftc.Send(mixed, addr); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// CTRL should be seq 0 (written first), DATA should be seq 1
	ctrlFile := seqFilename("dir/", "seqcli", seed, "s", 0)
	if !mock.hasFile(ctrlFile) {
		t.Fatalf("CTRL seq file %s was not uploaded", ctrlFile)
	}
	parsed, err := ParseSimplexPackets(mock.getFile(ctrlFile))
	if err != nil {
		t.Fatalf("ParseSimplexPackets CTRL: %v", err)
	}
	if len(parsed.Packets) != 1 || parsed.Packets[0].PacketType != SimplexPacketTypeCTRL ||
		string(parsed.Packets[0].Data) != "window-update" {
		t.Fatalf("CTRL file content = %#v", parsed.Packets)
	}
}

func TestSeqMode_Client_Monitoring_RecvCycle(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqTest]")
	addr := testAddr()
	seed := "client-seed"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buffer := NewSimplexBuffer(addr)
	ftc := NewFileTransportClient(mock, cfg, buffer, "", "", addr, ctx, cancel)
	ftc.prefix = "dir/"
	ftc.clientID = "seqcli"
	ftc.seed = seed

	recvFname := seqFilename("dir/", "seqcli", seed, "r", 0)
	recvPkts := NewSimplexPacketWithMaxSize([]byte("seq response"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	mock.putFile(recvFname, recvPkts.Marshal())

	pkt, _, err := ftc.Receive()
	if err != nil {
		t.Fatalf("Receive error: %v", err)
	}
	if pkt == nil {
		t.Fatal("Receive did not return sequenced recv packet")
	}
	if string(pkt.Data) != "seq response" {
		t.Errorf("got %q, want %q", string(pkt.Data), "seq response")
	}

	waitNoFile(t, mock, recvFname)
}

func TestSeqMode_Client_ReadsOutOfOrderControl(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqTest]")
	addr := testAddr()
	seed := "ctrl-seed"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buffer := NewSimplexBuffer(addr)
	ftc := NewFileTransportClient(mock, cfg, buffer, "", "", addr, ctx, cancel)
	ftc.prefix = "dir/"
	ftc.clientID = "seqcli"
	ftc.seed = seed

	ctrlPkts := NewSimplexPackets()
	ctrlPkts.Append(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("ack")))
	ctrlFile := seqFilename("dir/", "seqcli", seed, "r", 1)
	mock.putFile(ctrlFile, ctrlPkts.Marshal())

	pkt, _, err := ftc.Receive()
	if err != nil {
		t.Fatalf("Receive CTRL: %v", err)
	}
	if pkt == nil || pkt.PacketType != SimplexPacketTypeCTRL || string(pkt.Data) != "ack" {
		t.Fatalf("CTRL packet = %#v", pkt)
	}
	if ftc.recvSeq != 0 {
		t.Fatalf("recvSeq advanced across missing data: got %d want 0", ftc.recvSeq)
	}
	if _, ok := ftc.recvSeen[1]; !ok {
		t.Fatal("out-of-order CTRL seq should be marked seen")
	}
	waitNoFile(t, mock, ctrlFile)

	dataFile := seqFilename("dir/", "seqcli", seed, "r", 0)
	dataPkts := NewSimplexPacketWithMaxSize([]byte("data"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	mock.putFile(dataFile, dataPkts.Marshal())

	pkt, _, err = ftc.Receive()
	if err != nil {
		t.Fatalf("Receive DATA: %v", err)
	}
	if pkt == nil || pkt.PacketType != SimplexPacketTypeDATA || string(pkt.Data) != "data" {
		t.Fatalf("DATA packet = %#v", pkt)
	}
	if ftc.recvSeq != 2 {
		t.Fatalf("recvSeq after filling gap = %d, want 2", ftc.recvSeq)
	}
}

// TestSeqMode_ReadPathUsesListAsReadiness verifies that readSeqFilesParallel
// uses LIST to discover available files (readiness check), only reads files
// that exist, and skips gaps (seq 2 missing → seq 3 skipped as out-of-order DATA).
func TestSeqMode_ReadPathUsesListAsReadiness(t *testing.T) {
	ops := newTrackingStorageOps()
	cfg := testSeqFileTransportConfig("[SeqReadiness]")
	addr := testAddr()
	prefix := "dir/"
	clientID := "seqcli"
	seed := "readiness-seed"

	existing := make(map[string]bool)
	for _, seq := range []int{0, 1, 3} {
		fname := seqFilename(prefix, clientID, seed, "r", seq)
		pkts := NewSimplexPacketWithMaxSize([]byte(fmt.Sprintf("msg%d", seq)), SimplexPacketTypeDATA, cfg.MaxBodySize)
		ops.putFile(fname, pkts.Marshal())
		existing[fname] = true
	}

	baseSeq := 0
	seen := make(map[int]bool)
	buf := NewSimplexBuffer(addr)
	processed := readSeqFilesParallel(ops, cfg.LogPrefix, prefix, clientID, seed, "r", &baseSeq, seen, buf)

	if processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
	if baseSeq != 2 {
		t.Fatalf("baseSeq = %d, want 2", baseSeq)
	}

	listCalls, readPaths, _ := ops.stats()
	if listCalls != 1 {
		t.Fatalf("ListFiles calls = %d, want 1", listCalls)
	}
	if len(readPaths) != 3 {
		t.Fatalf("ReadFile calls = %d, want 3: %v", len(readPaths), readPaths)
	}
	missingPredicted := seqFilename(prefix, clientID, seed, "r", 2)
	for _, path := range readPaths {
		if !existing[path] {
			t.Fatalf("read path %s was not returned by list", path)
		}
		if path == missingPredicted {
			t.Fatalf("read missing predicted file %s", path)
		}
	}
}

// TestSeqMode_ReadPathBatchesListedFiles verifies that readSeqFilesParallel
// respects seqMaxOpsPerTick and leaves remaining files for the next tick.
func TestSeqMode_ReadPathBatchesListedFiles(t *testing.T) {
	ops := newTrackingStorageOps()
	cfg := testSeqFileTransportConfig("[SeqBatch]")
	addr := testAddr()
	prefix := "dir/"
	clientID := "seqcli"
	seed := "batch-seed"

	for seq := 0; seq < seqMaxOpsPerTick+1; seq++ {
		fname := seqFilename(prefix, clientID, seed, "r", seq)
		pkts := NewSimplexPacketWithMaxSize([]byte(fmt.Sprintf("msg%d", seq)), SimplexPacketTypeDATA, cfg.MaxBodySize)
		ops.putFile(fname, pkts.Marshal())
	}

	baseSeq := 0
	seen := make(map[int]bool)
	buf := NewSimplexBuffer(addr)
	processed := readSeqFilesParallel(ops, cfg.LogPrefix, prefix, clientID, seed, "r", &baseSeq, seen, buf)

	if processed != seqMaxOpsPerTick {
		t.Fatalf("processed = %d, want %d", processed, seqMaxOpsPerTick)
	}
	if baseSeq != seqMaxOpsPerTick {
		t.Fatalf("baseSeq = %d, want %d", baseSeq, seqMaxOpsPerTick)
	}
	listCalls, readPaths, _ := ops.stats()
	if listCalls != 1 {
		t.Fatalf("ListFiles calls = %d, want 1", listCalls)
	}
	if len(readPaths) != seqMaxOpsPerTick {
		t.Fatalf("ReadFile calls = %d, want %d: %v", len(readPaths), seqMaxOpsPerTick, readPaths)
	}
	if !ops.hasFile(seqFilename(prefix, clientID, seed, "r", seqMaxOpsPerTick)) {
		t.Fatalf("seq %d should remain for next tick", seqMaxOpsPerTick)
	}
}

func TestSeqMode_Client_StallDetection(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqTest]")
	addr := testAddr()
	seed := "stall-seed"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buffer := NewSimplexBuffer(addr)
	ftc := NewFileTransportClient(mock, cfg, buffer, "", "", addr, ctx, cancel)
	ftc.prefix = "dir/"
	ftc.clientID = "stallcli"
	ftc.seed = seed

	ftc.sendSeq = seqMaxPendingFiles + 1
	ftc.highestUploadedSeq = seqMaxPendingFiles

	oldFile := seqFilename("dir/", "stallcli", seed, "s", 1)
	mock.putFile(oldFile, []byte("stale"))

	pkts := NewSimplexPacketWithMaxSize([]byte("new data"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	_, err := ftc.Send(pkts, addr)
	if err == nil {
		t.Fatal("Send should return error when stall detected (old files lingering)")
	}
}

func TestSeqMode_HandlerRestart_ResumesSeq(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqRestart]")
	cfg.Interval = 20 * time.Millisecond
	cfg.IdleMultiplier = 5 // 100ms idle timeout
	addr := testAddr()

	prefix := "restart/"
	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	srv := NewFileTransportServer(mock, prefix, cfg, addr, srvCtx, srvCancel)

	// Simulate client sending seq 0 and 1
	seed := prefix
	clientID := "restcli"
	fname0 := seqFilename(prefix, clientID, seed, "s", 0)
	fname1 := seqFilename(prefix, clientID, seed, "s", 1)
	mock.putFile(fname0, NewSimplexPacketWithMaxSize([]byte("msg0"), SimplexPacketTypeDATA, cfg.MaxBodySize).Marshal())
	mock.putFile(fname1, NewSimplexPacketWithMaxSize([]byte("msg1"), SimplexPacketTypeDATA, cfg.MaxBodySize).Marshal())

	// Trigger first handler
	srv.scanForNewClients()

	// Drain inBuffer so handler can idle out
	deadline := time.Now().Add(2 * time.Second)
	drained := 0
	for drained < 2 && time.Now().Before(deadline) {
		if pkt, _, _ := srv.Receive(); pkt != nil {
			drained++
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Wait for handler cleanup
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, exists := srv.clients.Load(clientID); !exists {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, exists := srv.clients.Load(clientID); exists {
		t.Fatal("handler should have cleaned up after idle")
	}

	// Verify seq progress was saved
	if _, ok := srv.seqState.Load(clientID); !ok {
		t.Fatal("seqProgress should be saved after handler cleanup")
	}

	// Now client sends seq 2
	fname2 := seqFilename(prefix, clientID, seed, "s", 2)
	mock.putFile(fname2, NewSimplexPacketWithMaxSize([]byte("msg2"), SimplexPacketTypeDATA, cfg.MaxBodySize).Marshal())

	// Trigger new handler — it should resume from readSeq=2
	srv.scanForNewClients()

	deadline = time.Now().Add(2 * time.Second)
	var pkt *SimplexPacket
	for time.Now().Before(deadline) {
		pkt, _, _ = srv.Receive()
		if pkt != nil && string(pkt.Data) == "msg2" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if pkt == nil || string(pkt.Data) != "msg2" {
		t.Fatal("restarted handler did not resume at correct seq")
	}
}

func TestSeqMode_EndToEnd_ClientServer(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testSeqFileTransportConfig("[SeqE2E]")
	cfg.Interval = 30 * time.Millisecond
	addr := testAddr()

	prefix := "e2e/"

	// Start server
	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	srv := NewFileTransportServer(mock, prefix, cfg, addr, srvCtx, srvCancel)
	srv.StartPolling()

	// Start client
	clientID := "e2ecli"
	cliCtx, cliCancel := context.WithCancel(context.Background())
	defer cliCancel()
	cliBuffer := NewSimplexBuffer(addr)
	ftc := NewFileTransportClient(mock, cfg, cliBuffer, "", "", addr, cliCtx, cliCancel)
	ftc.prefix = prefix
	ftc.clientID = clientID
	ftc.seed = prefix
	ftc.StartMonitoring()

	// Client sends data
	clientPkts := NewSimplexPacketWithMaxSize([]byte("hello from client"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	ftc.Send(clientPkts, addr)

	// Wait for server to discover client and process data
	deadline := time.Now().Add(3 * time.Second)
	var pkt *SimplexPacket
	var pktAddr *SimplexAddr
	for time.Now().Before(deadline) {
		pkt, pktAddr, _ = srv.Receive()
		if pkt != nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	if pkt == nil {
		t.Fatal("server did not receive client data")
	}
	if string(pkt.Data) != "hello from client" {
		t.Errorf("got %q, want %q", string(pkt.Data), "hello from client")
	}

	// Server sends response back
	serverPkts := &SimplexPackets{
		Packets: []*SimplexPacket{NewSimplexPacket(SimplexPacketTypeDATA, []byte("hello from server"))},
	}
	_, err := srv.Send(serverPkts, pktAddr)
	if err != nil {
		t.Fatalf("server Send error: %v", err)
	}

	// Client should receive response
	deadline = time.Now().Add(3 * time.Second)
	var recvPkt *SimplexPacket
	for time.Now().Before(deadline) {
		recvPkt, _, _ = ftc.Receive()
		if recvPkt != nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	if recvPkt == nil {
		t.Fatal("client did not receive server response")
	}
	if string(recvPkt.Data) != "hello from server" {
		t.Errorf("got %q, want %q", string(recvPkt.Data), "hello from server")
	}
}
