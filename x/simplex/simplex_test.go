package simplex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/rem/x/arq"
)

type retryTestSimplex struct {
	addr      *SimplexAddr
	failures  atomic.Int32
	attempts  atomic.Int32
	attemptCh chan struct{}

	mu          sync.Mutex
	sent        [][]byte
	sentBatches [][]batchPacket
}

type batchPacket struct {
	packetType SimplexPacketType
	data       []byte
}

type retryTestConfig struct {
	cfg arq.ARQConfig
}

func (c *retryTestConfig) GetARQConfig() arq.ARQConfig {
	return c.cfg
}

func newRetryTestSimplex(addr *SimplexAddr, failCount int32) *retryTestSimplex {
	sx := &retryTestSimplex{
		addr:      addr,
		attemptCh: make(chan struct{}, 16),
	}
	sx.failures.Store(failCount)
	return sx
}

func (s *retryTestSimplex) Receive() (*SimplexPacket, *SimplexAddr, error) {
	return nil, s.addr, nil
}

func (s *retryTestSimplex) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	s.attempts.Add(1)
	select {
	case s.attemptCh <- struct{}{}:
	default:
	}
	if s.failures.Load() > 0 {
		s.failures.Add(-1)
		return 0, errors.New("transient send failure")
	}

	s.mu.Lock()
	batch := make([]batchPacket, 0, len(pkts.Packets))
	for _, pkt := range pkts.Packets {
		cp := make([]byte, len(pkt.Data))
		copy(cp, pkt.Data)
		s.sent = append(s.sent, cp)
		batch = append(batch, batchPacket{
			packetType: pkt.PacketType,
			data:       cp,
		})
	}
	s.sentBatches = append(s.sentBatches, batch)
	s.mu.Unlock()
	return pkts.Size(), nil
}

func (s *retryTestSimplex) Addr() *SimplexAddr {
	return s.addr
}

func (s *retryTestSimplex) Close() error {
	return nil
}

func (s *retryTestSimplex) Sent() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.sent))
	for i := range s.sent {
		out[i] = append([]byte(nil), s.sent[i]...)
	}
	return out
}

func (s *retryTestSimplex) SentBatches() [][]batchPacket {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([][]batchPacket, len(s.sentBatches))
	for i := range s.sentBatches {
		out[i] = make([]batchPacket, len(s.sentBatches[i]))
		for j := range s.sentBatches[i] {
			out[i][j] = batchPacket{
				packetType: s.sentBatches[i][j].packetType,
				data:       append([]byte(nil), s.sentBatches[i][j].data...),
			}
		}
	}
	return out
}

func (s *retryTestSimplex) WaitAttempt(timeout time.Duration) bool {
	select {
	case <-s.attemptCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

func newTestSimplexClientWrapper(sx Simplex, addr *SimplexAddr) *SimplexClient {
	ctx, cancel := context.WithCancel(context.Background())
	buf := NewSimplexBuffer(addr)
	client := &SimplexClient{
		Simplex: sx,
		buf:     buf,
		ctx:     ctx,
		cancel:  cancel,
		isCtrl: func([]byte) bool {
			return false
		},
		failed: make(chan struct{}),
	}
	go client.polling()
	return client
}

func newTestSimplexClientWrapperWithCtrl(sx Simplex, addr *SimplexAddr, isCtrl func([]byte) bool) *SimplexClient {
	client := newTestSimplexClientWrapper(sx, addr)
	client.isCtrl = isCtrl
	return client
}

func newTestSimplexServerWrapper(sx Simplex) *SimplexServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &SimplexServer{
		Simplex: sx,
		recvCh:  make(chan recvEntry, 4),
		ctx:     ctx,
		cancel:  cancel,
		isCtrl: func([]byte) bool {
			return false
		},
	}
}

func waitForRetryCondition(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func newRetryTestSimplexAddr(interval time.Duration) *SimplexAddr {
	return newTestSimplexAddr(interval, 4096)
}

func newRetryTestSimplexAddrWithARQ(interval time.Duration, cfg arq.ARQConfig) *SimplexAddr {
	addr := newRetryTestSimplexAddr(interval)
	addr.SetConfig(&retryTestConfig{cfg: cfg})
	return addr
}

func TestSimplexClientRetriesBufferedPacketsAfterSendFailure(t *testing.T) {
	addr := newRetryTestSimplexAddr(10 * time.Millisecond)
	sx := newRetryTestSimplex(addr, 1)
	client := newTestSimplexClientWrapper(sx, addr)
	defer client.Close()

	payload := []byte("retry-client-payload")
	if _, err := client.WriteTo(payload, addr); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	if !waitForRetryCondition(time.Second, func() bool { return len(sx.Sent()) == 1 }) {
		t.Fatal("client retry did not succeed before timeout")
	}

	sent := sx.Sent()
	if got := string(sent[0]); got != string(payload) {
		t.Fatalf("sent payload = %q, want %q", got, string(payload))
	}
	if attempts := sx.attempts.Load(); attempts < 2 {
		t.Fatalf("send attempts = %d, want at least 2", attempts)
	}
}

func TestSimplexServerRetriesBufferedPacketsAfterSendFailure(t *testing.T) {
	addr := newRetryTestSimplexAddr(10 * time.Millisecond)
	sx := newRetryTestSimplex(addr, 1)
	server := newTestSimplexServerWrapper(sx)
	defer server.Close()

	buf := server.GetBuffer(addr)
	if err := buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte("retry-server-payload"))); err != nil {
		t.Fatalf("PutPacket failed: %v", err)
	}

	if !waitForRetryCondition(time.Second, func() bool { return len(sx.Sent()) == 1 }) {
		t.Fatal("server retry did not succeed before timeout")
	}

	sent := sx.Sent()
	if got := string(sent[0]); got != "retry-server-payload" {
		t.Fatalf("sent payload = %q, want %q", got, "retry-server-payload")
	}
	if attempts := sx.attempts.Load(); attempts < 2 {
		t.Fatalf("send attempts = %d, want at least 2", attempts)
	}
}

func TestSimplexClientPreemptsPendingDataForControlPackets(t *testing.T) {
	addr := newRetryTestSimplexAddr(10 * time.Millisecond)
	sx := newRetryTestSimplex(addr, 2)
	client := newTestSimplexClientWrapperWithCtrl(sx, addr, func(data []byte) bool {
		return bytes.HasPrefix(data, []byte("ctrl:"))
	})
	defer client.Close()

	if _, err := client.WriteTo([]byte("data:payload"), addr); err != nil {
		t.Fatalf("WriteTo(data) failed: %v", err)
	}
	if !sx.WaitAttempt(time.Second) {
		t.Fatal("timed out waiting for initial data send attempt")
	}
	if _, err := client.WriteTo([]byte("ctrl:ack"), addr); err != nil {
		t.Fatalf("WriteTo(ctrl) failed: %v", err)
	}

	if !waitForRetryCondition(time.Second, func() bool { return len(sx.SentBatches()) >= 1 }) {
		t.Fatal("client preempted send did not succeed before timeout")
	}

	batches := sx.SentBatches()
	first := batches[0]
	if len(first) == 0 {
		t.Fatal("first successful batch is empty")
	}
	if first[0].packetType != SimplexPacketTypeCTRL || string(first[0].data) != "ctrl:ack" {
		t.Fatalf("first successful packet = (%d,%q), want CTRL ctrl:ack", first[0].packetType, string(first[0].data))
	}
}

func TestSimplexServerPreemptsPendingDataForControlPackets(t *testing.T) {
	addr := newRetryTestSimplexAddr(10 * time.Millisecond)
	sx := newRetryTestSimplex(addr, 2)
	server := newTestSimplexServerWrapper(sx)
	defer server.Close()

	buf := server.GetBuffer(addr)
	if err := buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte("data:payload"))); err != nil {
		t.Fatalf("PutPacket(data) failed: %v", err)
	}
	if !sx.WaitAttempt(time.Second) {
		t.Fatal("timed out waiting for initial server data send attempt")
	}
	if err := buf.PutPacket(NewSimplexPacket(SimplexPacketTypeCTRL, []byte("ctrl:ack"))); err != nil {
		t.Fatalf("PutPacket(ctrl) failed: %v", err)
	}

	if !waitForRetryCondition(time.Second, func() bool { return len(sx.SentBatches()) >= 1 }) {
		t.Fatal("server preempted send did not succeed before timeout")
	}

	batches := sx.SentBatches()
	first := batches[0]
	if len(first) == 0 {
		t.Fatal("first successful batch is empty")
	}
	if first[0].packetType != SimplexPacketTypeCTRL || string(first[0].data) != "ctrl:ack" {
		t.Fatalf("first successful packet = (%d,%q), want CTRL ctrl:ack", first[0].packetType, string(first[0].data))
	}
}

func TestSimplexClientFailsTransportAfterAcceptanceStall(t *testing.T) {
	addr := newRetryTestSimplexAddrWithARQ(10*time.Millisecond, arq.ARQConfig{
		MTU:                128,
		RTO:                20,
		MaxRetransmissions: 1,
	})
	sx := newRetryTestSimplex(addr, 1<<30)
	client := newTestSimplexClientWrapper(sx, addr)
	defer client.Close()

	if _, err := client.WriteTo([]byte("stall-client"), addr); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	if !waitForRetryCondition(2*time.Second, func() bool {
		return client.transportFailure() != nil
	}) {
		t.Fatal("client transport did not fail after repeated acceptance stalls")
	}
	if err := client.transportFailure(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("transportFailure = %v, want io.ErrClosedPipe", err)
	}
	if _, err := client.WriteTo([]byte("after-stall"), addr); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("WriteTo after stalled transport = %v, want %v", err, io.ErrClosedPipe)
	}
}

func TestSimplexServerFailsPeerAfterAcceptanceStall(t *testing.T) {
	addr := newRetryTestSimplexAddrWithARQ(10*time.Millisecond, arq.ARQConfig{
		MTU:                128,
		RTO:                20,
		MaxRetransmissions: 1,
	})
	sx := newRetryTestSimplex(addr, 1<<30)
	server := newTestSimplexServerWrapper(sx)
	defer server.Close()

	peerErr := make(chan error, 1)
	server.OnDisconnect(func(net.Addr) {
		select {
		case peerErr <- io.ErrClosedPipe:
		default:
		}
	})

	buf := server.GetBuffer(addr)
	if err := buf.PutPacket(NewSimplexPacket(SimplexPacketTypeDATA, []byte("stall-server"))); err != nil {
		t.Fatalf("PutPacket failed: %v", err)
	}

	select {
	case <-peerErr:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not propagate peer disconnect after stalled transport")
	}
	if _, ok := server.buffers.Load(addr.ID()); ok {
		t.Fatal("server buffer still present after stalled transport failure")
	}
}

var _ net.PacketConn = (*SimplexClient)(nil)
var _ net.PacketConn = (*SimplexServer)(nil)

// ============================================================
// Utils Tests
// ============================================================

func TestValidateSeed(t *testing.T) {
	tests := []struct {
		name    string
		seed    string
		wantErr bool
	}{
		{"valid lowercase", "abcd", false},
		{"valid uppercase", "ABCD", false},
		{"valid mixed", "aBcD1234", false},
		{"valid 32 chars", "abcdefghijklmnopqrstuvwxyz123456", false},
		{"too short", "abc", true},
		{"too long", "abcdefghijklmnopqrstuvwxyz1234567", true},
		{"empty", "", true},
		{"contains underscore", "ab_cd", true},
		{"contains dash", "ab-cd", true},
		{"contains space", "ab cd", true},
		{"contains dot", "ab.cd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSeed(tt.seed)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSeed(%q) error = %v, wantErr %v", tt.seed, err, tt.wantErr)
			}
		})
	}
}

func TestFormatSeedTimestamp(t *testing.T) {
	result := formatSeedTimestamp("abcd1234", 1710374400)
	if result != "abcd1234_1710374400" {
		t.Errorf("formatSeedTimestamp got %q, want %q", result, "abcd1234_1710374400")
	}
}

func TestParseSeedTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		wantSeed string
		wantTS   int64
		wantErr  bool
	}{
		{"normal", "abcd1234_1710374400", "abcd1234", 1710374400, false},
		{"seed with underscore", "ab_cd_1710374400", "ab_cd", 1710374400, false},
		{"no underscore", "abcd1234", "", 0, true},
		{"empty seed", "_1710374400", "", 0, true},
		{"bad timestamp", "abcd_notanumber", "", 0, true},
		{"empty string", "", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seed, ts, err := parseSeedTimestamp(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSeedTimestamp(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if seed != tt.wantSeed {
					t.Errorf("seed = %q, want %q", seed, tt.wantSeed)
				}
				if ts != tt.wantTS {
					t.Errorf("ts = %d, want %d", ts, tt.wantTS)
				}
			}
		})
	}
}

func TestParseSeedTimestamp_Roundtrip(t *testing.T) {
	seed := "testSeed1234"
	var ts int64 = 1710374400
	formatted := formatSeedTimestamp(seed, ts)
	gotSeed, gotTS, err := parseSeedTimestamp(formatted)
	if err != nil {
		t.Fatalf("parseSeedTimestamp(%q) unexpected error: %v", formatted, err)
	}
	if gotSeed != seed || gotTS != ts {
		t.Errorf("roundtrip failed: got (%q, %d), want (%q, %d)", gotSeed, gotTS, seed, ts)
	}
}

// ============================================================
// Registry Matrix Tests
// ============================================================

func TestRegisteredSimplexResolvers(t *testing.T) {
	cases := []struct {
		scheme  string
		address string
	}{
		{scheme: "http", address: "http://127.0.0.1:8080/rem?interval=50&max=1024&wrapper=raw"},
		{scheme: "https", address: "https://127.0.0.1:8443/rem?interval=50&max=1024&wrapper=raw"},
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
			if addr == nil {
				t.Fatal("ResolveSimplexAddr returned nil addr")
			}
			if got := addr.Network(); got != tc.scheme {
				t.Fatalf("unexpected simplex addr network: got %q want %q", got, tc.scheme)
			}
		})
	}
}

// ============================================================
// Merged from simplex_close_test.go
// ============================================================

type testSimplex struct {
	addr       *SimplexAddr
	closeCalls atomic.Int32
}

func (s *testSimplex) Receive() (*SimplexPacket, *SimplexAddr, error) { return nil, nil, nil }
func (s *testSimplex) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	if pkts == nil {
		return 0, nil
	}
	return pkts.Size(), nil
}
func (s *testSimplex) Addr() *SimplexAddr { return s.addr }
func (s *testSimplex) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func newTestSimplexAddr(interval time.Duration, maxBodySize int) *SimplexAddr {
	return &SimplexAddr{
		URL:         &url.URL{Scheme: "test", Host: "local"},
		id:          "test-simplex-client",
		interval:    interval,
		maxBodySize: maxBodySize,
		options:     url.Values{},
	}
}

func TestSimplexClientClose_UnblocksReadFromAndStopsPolling(t *testing.T) {
	addr := newTestSimplexAddr(20*time.Millisecond, 4096)
	transport := &testSimplex{addr: addr}
	ctx, cancel := context.WithCancel(context.Background())
	client := &SimplexClient{
		Simplex: transport,
		buf:     NewSimplexBuffer(addr),
		ctx:     ctx,
		cancel:  cancel,
	}

	pollDone := make(chan struct{})
	go func() {
		client.polling()
		close(pollDone)
	}()

	time.Sleep(60 * time.Millisecond)

	client.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if n, _, err := client.ReadFrom(make([]byte, 64)); n != 0 || err == nil {
		t.Fatalf("ReadFrom with deadline before Close = (%d, %v), want timeout", n, err)
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("ReadFrom with deadline error = %v, want timeout net.Error", err)
	}
	client.SetReadDeadline(time.Time{})

	readDone := make(chan error, 1)
	go func() {
		_, _, err := client.ReadFrom(make([]byte, 64))
		readDone <- err
	}()

	select {
	case err := <-readDone:
		t.Fatalf("ReadFrom returned before Close: %v", err)
	case <-time.After(80 * time.Millisecond):
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	select {
	case err := <-readDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("ReadFrom after Close = %v, want %v", err, io.ErrClosedPipe)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadFrom remained blocked after Close")
	}

	select {
	case <-pollDone:
	case <-time.After(time.Second):
		t.Fatal("polling goroutine did not exit after Close")
	}

	if transport.closeCalls.Load() != 1 {
		t.Fatalf("underlying transport Close calls = %d, want 1", transport.closeCalls.Load())
	}
}

func TestSimplexServerReadFrom_BlocksUntilDataDeadlineAndClose(t *testing.T) {
	addr := newTestSimplexAddr(20*time.Millisecond, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	server := &SimplexServer{
		recvCh: make(chan recvEntry, 1),
		ctx:    ctx,
		cancel: cancel,
	}

	type readResult struct {
		n    int
		addr net.Addr
		err  error
		data []byte
	}

	readDone := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 64)
		n, raddr, err := server.ReadFrom(buf)
		readDone <- readResult{n: n, addr: raddr, err: err, data: append([]byte(nil), buf[:n]...)}
	}()

	select {
	case result := <-readDone:
		t.Fatalf("ReadFrom returned before data/close: %+v", result)
	case <-time.After(80 * time.Millisecond):
	}

	server.recvCh <- recvEntry{data: []byte("server-packet"), addr: addr}

	select {
	case result := <-readDone:
		if result.err != nil {
			t.Fatalf("ReadFrom returned error: %v", result.err)
		}
		if result.n != len("server-packet") {
			t.Fatalf("ReadFrom n = %d, want %d", result.n, len("server-packet"))
		}
		if string(result.data) != "server-packet" {
			t.Fatalf("ReadFrom data = %q, want %q", string(result.data), "server-packet")
		}
		if result.addr != addr {
			t.Fatalf("ReadFrom addr = %v, want %v", result.addr, addr)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadFrom did not return after packet arrival")
	}

	server.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if n, _, err := server.ReadFrom(make([]byte, 64)); n != 0 || err == nil {
		t.Fatalf("ReadFrom with deadline = (%d, %v), want timeout", n, err)
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("ReadFrom with deadline error = %v, want timeout net.Error", err)
	}
	server.SetReadDeadline(time.Time{})

	readDone = make(chan readResult, 1)
	go func() {
		buf := make([]byte, 64)
		n, raddr, err := server.ReadFrom(buf)
		readDone <- readResult{n: n, addr: raddr, err: err, data: append([]byte(nil), buf[:n]...)}
	}()

	select {
	case result := <-readDone:
		t.Fatalf("ReadFrom returned before cancel: %+v", result)
	case <-time.After(80 * time.Millisecond):
	}

	cancel()

	select {
	case result := <-readDone:
		if !errors.Is(result.err, io.ErrClosedPipe) {
			t.Fatalf("ReadFrom after cancel = %v, want %v", result.err, io.ErrClosedPipe)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadFrom remained blocked after cancel")
	}
}
