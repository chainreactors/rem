package simplex

import (
	"context"
	"errors"
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
	if !strings.HasSuffix(prefix, "/") {
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

// failingStorageOps wraps mockStorageOps with controllable failures
type failingStorageOps struct {
	*mockStorageOps
	mu            sync.Mutex
	readFailCount int
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

func TestFileTransport_Client_SendQueuesToPending(t *testing.T) {
	addr := testAddr()
	cfg := testFileTransportConfig("[Test]")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ftc := NewFileTransportClient(nil, cfg, NewSimplexBuffer(addr), "s.txt", "r.txt", addr, ctx, cancel)

	data := []byte("send test")
	pkts := NewSimplexPacketWithMaxSize(data, SimplexPacketTypeDATA, cfg.MaxBodySize)

	n, err := ftc.Send(pkts, addr)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if n == 0 {
		t.Error("expected non-zero bytes")
	}

	ftc.pendingMu.Lock()
	hasPending := len(ftc.pendingData) > 0
	ftc.pendingMu.Unlock()
	if !hasPending {
		t.Error("expected pendingData to be non-empty")
	}
}

func TestFileTransport_Client_Monitoring_SendCycle(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[Test]")
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr),
		"dir/test_send.txt", "dir/test_recv.txt", addr, ctx, cancel)

	// Queue data
	pkts := NewSimplexPacketWithMaxSize([]byte("monitoring test"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	ftc.Send(pkts, addr)

	go ftc.monitoring()
	defer cancel()

	deadline := time.Now().Add(2 * time.Second)
	for !mock.hasFile("dir/test_send.txt") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if !mock.hasFile("dir/test_send.txt") {
		t.Fatal("monitoring did not write send file")
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

func TestFileTransport_Client_Monitoring_RecvCycle(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[Test]")
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	buffer := NewSimplexBuffer(addr)
	ftc := NewFileTransportClient(mock, cfg, buffer,
		"dir/test_send.txt", "dir/test_recv.txt", addr, ctx, cancel)

	// Place recv file
	recvData := []byte("response from server")
	recvPkts := NewSimplexPacketWithMaxSize(recvData, SimplexPacketTypeDATA, cfg.MaxBodySize)
	mock.putFile("dir/test_recv.txt", recvPkts.Marshal())

	go ftc.monitoring()
	defer cancel()

	deadline := time.Now().Add(2 * time.Second)
	var pkt *SimplexPacket
	for time.Now().Before(deadline) {
		pkt, _ = buffer.GetPacket()
		if pkt != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if pkt == nil {
		t.Fatal("monitoring did not process recv file")
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

// TestFileTransport_Zombie_StaleSendFile verifies Fix #1:
// Client sendFile exists (server dead), sendStallCount increments → cancel.
func TestFileTransport_Zombie_StaleSendFile(t *testing.T) {
	mock := newMockStorageOps()
	cfg := testFileTransportConfig("[Test]")
	cfg.Interval = 20 * time.Millisecond
	addr := testAddr()

	// Pre-place send file (simulating dead server)
	mock.putFile("dir/test_send.txt", []byte("old data"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr),
		"dir/test_send.txt", "dir/test_recv.txt", addr, ctx, cancel)

	// Queue new data
	pkts := NewSimplexPacketWithMaxSize([]byte("new data"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	ftc.Send(pkts, addr)

	go ftc.monitoring()

	// sendStallCount should trigger cancel after ~10 ticks (200ms)
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(2 * time.Second):
		t.Fatal("TIMEOUT: monitoring did not cancel ctx after stale sendFile")
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

// TestFileTransport_Zombie_RecvFailure verifies Fix #3:
// Client recv PullFile fails with non-404 error → recvFailures → cancel.
func TestFileTransport_Zombie_RecvFailure(t *testing.T) {
	baseMock := newMockStorageOps()
	mock := &recvFailStorageOps{
		mockStorageOps: baseMock,
		failPath:       "dir/test_recv.txt",
	}

	cfg := testFileTransportConfig("[Test]")
	cfg.Interval = 20 * time.Millisecond
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr),
		"dir/test_send.txt", "dir/test_recv.txt", addr, ctx, cancel)

	go ftc.monitoring()

	// recvFailures should trigger cancel after ~10 ticks (200ms)
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(2 * time.Second):
		t.Fatal("TIMEOUT: monitoring did not cancel ctx after persistent recv failures")
	}

	// Verify the error is not os.ErrNotExist
	_, err := mock.ReadFile("dir/test_recv.txt")
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("recvFailStorageOps should NOT return os.ErrNotExist")
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

// TestFileTransport_Client_ErrorPropagation verifies consecutive failures cancel context.
func TestFileTransport_Client_ErrorPropagation(t *testing.T) {
	mock := newFailingStorageOps()
	mock.mu.Lock()
	mock.writeFailCount = 100
	mock.mu.Unlock()

	cfg := testFileTransportConfig("[Test]")
	cfg.Interval = 10 * time.Millisecond
	addr := testAddr()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ftc := NewFileTransportClient(mock, cfg, NewSimplexBuffer(addr),
		"dir/errprop_send.txt", "dir/errprop_recv.txt", addr, ctx, cancel)

	pkts := NewSimplexPacketWithMaxSize([]byte("will fail"), SimplexPacketTypeDATA, cfg.MaxBodySize)
	ftc.Send(pkts, addr)

	go ftc.monitoring()

	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout: monitoring did not cancel context after persistent failures")
	}
}
