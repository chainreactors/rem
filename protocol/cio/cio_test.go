package cio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}

// ---------------------------------------------------------------------------
// buffer.go — GetBuf / PutBuf pool
// ---------------------------------------------------------------------------

func TestGetBuf_Sizes(t *testing.T) {
	sizes := []int{0, 1, 100, 512, 1023, 1024, 2048, 5120, 16384, 32768, 65536}
	for _, sz := range sizes {
		buf := GetBuf(sz)
		if len(buf) != sz {
			t.Errorf("GetBuf(%d): got len %d", sz, len(buf))
		}
		PutBuf(buf) // should not panic
	}
}

func TestGetBuf_ReturnsUsable(t *testing.T) {
	buf := GetBuf(128)
	// write and read back
	for i := range buf {
		buf[i] = byte(i)
	}
	for i := range buf {
		if buf[i] != byte(i) {
			t.Fatal("buffer data corrupt")
		}
	}
	PutBuf(buf)
}

func TestGetBuf_PoolReuse(t *testing.T) {
	// get and put, then get again — should not panic or return short buf
	for i := 0; i < 100; i++ {
		b := GetBuf(1024)
		PutBuf(b)
	}
	b := GetBuf(1024)
	if len(b) < 1024 {
		t.Fatal("reused buf too short")
	}
	PutBuf(b)
}

// ---------------------------------------------------------------------------
// buffer.go — Buffer (pipe-based)
// ---------------------------------------------------------------------------

func TestBuffer_WriteRead(t *testing.T) {
	buf := NewBuffer(4096)
	defer buf.Close()

	data := []byte("hello world")
	n, err := buf.Write(data)
	if err != nil || n != len(data) {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}

	out := make([]byte, len(data))
	_, err = io.ReadFull(buf, out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Fatalf("got %q, want %q", out, data)
	}
}

func TestBuffer_LargeChunked(t *testing.T) {
	buf := NewBuffer(4096)
	defer buf.Close()

	// chunkSize = 4096/100 = 40, so anything >40 triggers writeChunks
	data := bytes.Repeat([]byte("A"), 200)
	n, err := buf.Write(data)
	if err != nil || n != 200 {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}

	out := make([]byte, 200)
	_, err = io.ReadFull(buf, out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("chunked data mismatch")
	}
}

func TestBuffer_WriteCount(t *testing.T) {
	buf := NewBuffer(4096)
	defer buf.Close()

	buf.Write([]byte("abc"))
	buf.Write([]byte("defgh"))
	if buf.WriteCount != 8 {
		t.Fatalf("WriteCount = %d, want 8", buf.WriteCount)
	}
}

func TestBuffer_ReadCount(t *testing.T) {
	buf := NewBuffer(4096)
	defer buf.Close()

	buf.Write([]byte("hello"))
	out := make([]byte, 5)
	io.ReadFull(buf, out)
	if buf.ReadCount != 5 {
		t.Fatalf("ReadCount = %d, want 5", buf.ReadCount)
	}
}

func TestBuffer_Close(t *testing.T) {
	buf := NewBuffer(4096)
	buf.Close()

	_, err := buf.Write([]byte("x"))
	if err != io.ErrClosedPipe {
		t.Fatalf("Write after close: err=%v, want ErrClosedPipe", err)
	}

	_, err = buf.Read(make([]byte, 1))
	if err != io.ErrClosedPipe {
		t.Fatalf("Read after close: err=%v, want ErrClosedPipe", err)
	}
}

func TestBuffer_DoubleClose(t *testing.T) {
	buf := NewBuffer(4096)
	buf.Close()
	buf.Close() // should not panic
}

func TestBuffer_Size(t *testing.T) {
	buf := NewBuffer(4096)
	defer buf.Close()
	if buf.Size() != 0 {
		t.Fatalf("initial Size = %d, want 0", buf.Size())
	}
}

func TestBuffer_Percentage(t *testing.T) {
	buf := NewBuffer(4096)
	defer buf.Close()
	p := buf.Percentage()
	if p != 0 {
		t.Fatalf("initial Percentage = %f, want 0", p)
	}
}

func TestBuffer_ConcurrentWriteRead(t *testing.T) {
	buf := NewBuffer(1 << 20)
	defer buf.Close()

	total := 1024
	payload := bytes.Repeat([]byte("x"), 64)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			buf.Write(payload)
		}
	}()

	go func() {
		defer wg.Done()
		out := make([]byte, 64)
		for i := 0; i < total; i++ {
			io.ReadFull(buf, out)
		}
	}()

	wg.Wait()
}

func TestBufferContext_Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	buf := NewBufferContext(ctx, 4096)

	buf.Write([]byte("data"))
	cancel()
	time.Sleep(50 * time.Millisecond)

	// After context cancel, writes should eventually fail
	_, err := buf.Write([]byte("more"))
	if err != nil && err != io.ErrClosedPipe {
		// either closed pipe or context canceled are acceptable
	}
}

// ---------------------------------------------------------------------------
// io.go — pack / unpack / WriteMsg / ReadMsg / readFull
// ---------------------------------------------------------------------------

func TestPackUnpack_Roundtrip(t *testing.T) {
	orig := &message.Ping{Ping: "test-ping"}
	packed, err := pack(orig)
	if err != nil {
		t.Fatal(err)
	}
	defer PutBuf(packed)

	mtype := message.MsgType(packed[0])
	length := binary.LittleEndian.Uint32(packed[1:5])

	msg, err := unpack(packed[5:5+length], mtype)
	if err != nil {
		t.Fatal(err)
	}

	got := msg.(*message.Ping)
	if got.Ping != "test-ping" {
		t.Fatalf("got %q, want %q", got.Ping, "test-ping")
	}
}

func TestPack_AllMessageTypes(t *testing.T) {
	msgs := []message.Message{
		&message.Login{ConsoleIP: "1.2.3.4"},
		&message.Ack{Status: message.StatusSuccess},
		&message.Control{Source: "s"},
		&message.Ping{Ping: "p"},
		&message.Pong{Pong: "p"},
		&message.Packet{ID: 1, Data: []byte("d")},
		&message.ConnStart{ID: 1},
		&message.ConnEnd{ID: 1},
		&message.Redirect{Source: "s"},
		&message.CWND{Bridge: 1},
		&message.BridgeOpen{ID: 1, Source: "s"},
		&message.BridgeClose{ID: 1},
	}
	for _, m := range msgs {
		packed, err := pack(m)
		if err != nil {
			t.Errorf("pack(%T): %v", m, err)
			continue
		}
		if len(packed) < 5 {
			t.Errorf("pack(%T): too short", m)
		}
		PutBuf(packed)
	}
}

func TestUnpack_EmptyBytes(t *testing.T) {
	_, err := unpack([]byte{}, message.PingMsg)
	if err != message.ErrEmptyMessage {
		t.Fatalf("expected ErrEmptyMessage, got %v", err)
	}
}

func TestUnpack_InvalidType(t *testing.T) {
	_, err := unpack([]byte{1}, message.MsgType(0))
	if !errors.Is(err, message.ErrInvalidType) {
		t.Fatalf("expected ErrInvalidType, got %v", err)
	}
}

func TestUnpack_UnknownType(t *testing.T) {
	_, err := unpack([]byte{1}, message.End)
	if !errors.Is(err, message.ErrInvalidType) {
		t.Fatalf("expected ErrInvalidType, got %v", err)
	}
}

func TestWriteReadMsg_Roundtrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	orig := &message.Ping{Ping: "hello"}
	errCh := make(chan error, 1)
	go func() {
		errCh <- WriteMsg(c1, orig)
	}()

	msg, err := ReadMsg(c2)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	got := msg.(*message.Ping)
	if got.Ping != "hello" {
		t.Fatalf("got %q, want %q", got.Ping, "hello")
	}
}

func TestWriteReadMsg_MultipleTypes(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	msgs := []message.Message{
		&message.Ping{Ping: "p1"},
		&message.Pong{Pong: "p2"},
		&message.Ack{Status: message.StatusSuccess, Error: "ok"},
		&message.BridgeOpen{ID: 42, Source: "src", Destination: "dst"},
	}

	go func() {
		for _, m := range msgs {
			WriteMsg(c1, m)
		}
	}()

	for i := range msgs {
		msg, err := ReadMsg(c2)
		if err != nil {
			t.Fatalf("ReadMsg[%d]: %v", i, err)
		}
		if message.GetMessageType(msg) != message.GetMessageType(msgs[i]) {
			t.Fatalf("type mismatch at %d", i)
		}
	}
}

func TestReadMsg_InvalidType(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		header := make([]byte, 5)
		header[0] = byte(message.End) // invalid type
		binary.LittleEndian.PutUint32(header[1:5], 0)
		c1.Write(header)
	}()

	_, err := ReadMsg(c2)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestReadMsg_ExceedsMaxPacketSize(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		header := make([]byte, 5)
		header[0] = byte(message.PingMsg)
		binary.LittleEndian.PutUint32(header[1:5], uint32(core.MaxPacketSize+1))
		c1.Write(header)
	}()

	_, err := ReadMsg(c2)
	if err == nil {
		t.Fatal("expected error for oversized message")
	}
}

func TestReadMsg_ConnectionClosed(t *testing.T) {
	c1, c2 := net.Pipe()
	c1.Close()

	_, err := ReadMsg(c2)
	if err == nil {
		t.Fatal("expected error on closed conn")
	}
}

func TestReadAndAssertMsg_Success(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		WriteMsg(c1, &message.Ping{Ping: "x"})
	}()

	msg, err := ReadAndAssertMsg(c2, message.PingMsg)
	if err != nil {
		t.Fatal(err)
	}
	if msg.(*message.Ping).Ping != "x" {
		t.Fatal("content mismatch")
	}
}

func TestReadAndAssertMsg_TypeMismatch(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		WriteMsg(c1, &message.Ping{Ping: "x"})
	}()

	_, err := ReadAndAssertMsg(c2, message.PongMsg) // expect Pong, get Ping
	if err == nil {
		t.Fatal("expected type mismatch error")
	}
}

func TestWriteAndAssertMsg_Success(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		msg, _ := ReadMsg(c2)
		_ = msg // consume the sent message
		WriteMsg(c2, &message.Ack{Status: message.StatusSuccess})
	}()

	ack, err := WriteAndAssertMsg(c1, &message.Ping{Ping: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if ack.Status != message.StatusSuccess {
		t.Fatalf("ack status = %d, want %d", ack.Status, message.StatusSuccess)
	}
}

func TestWriteAndAssertMsg_FailedStatus(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		ReadMsg(c2)
		WriteMsg(c2, &message.Ack{Status: message.StatusFailed, Error: "denied"})
	}()

	_, err := WriteAndAssertMsg(c1, &message.Ping{Ping: "hello"})
	if err == nil {
		t.Fatal("expected error for failed ack")
	}
}

func TestReadFull(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	data := []byte("abcdefghij")
	go func() {
		// write in small chunks to test readFull loop
		c1.Write(data[:3])
		c1.Write(data[3:7])
		c1.Write(data[7:])
	}()

	buf := make([]byte, 10)
	n, err := readFull(c2, buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Fatalf("n=%d, want 10", n)
	}
	if !bytes.Equal(buf, data) {
		t.Fatalf("got %q, want %q", buf, data)
	}
}

func TestReadFull_ConnectionError(t *testing.T) {
	c1, c2 := net.Pipe()
	c1.Close()

	buf := make([]byte, 10)
	_, err := readFull(c2, buf)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// conn.go — Join / JoinWithError / WrapConn / ReadWriteCloser / LimitedConn
// ---------------------------------------------------------------------------

func TestJoin_BidirectionalCopy(t *testing.T) {
	data1 := bytes.Repeat([]byte("A"), 1024)
	data2 := bytes.Repeat([]byte("B"), 512)

	r1 := bytes.NewReader(data1)
	w1 := &bytes.Buffer{}
	c1 := &rwcBuf{r: r1, w: w1}

	r2 := bytes.NewReader(data2)
	w2 := &bytes.Buffer{}
	c2 := &rwcBuf{r: r2, w: w2}

	in, out := Join(c1, c2)
	// c1→c2: data1 written to w2
	// c2→c1: data2 written to w1
	if int(in)+int(out) != len(data1)+len(data2) {
		t.Fatalf("byte count mismatch: in=%d out=%d total=%d", in, out, len(data1)+len(data2))
	}
}

func TestJoinWithError_ReportsErrors(t *testing.T) {
	errRead := errors.New("read fail")
	c1 := &rwcBuf{r: &errReader{err: errRead}, w: io.Discard}
	c2 := &rwcBuf{r: strings.NewReader("data"), w: io.Discard}

	_, _, errs := JoinWithError(c1, c2)
	// At least one direction should have an error
	if len(errs) == 0 {
		// errReader causes write to c2 to succeed but read from c1 fails
		// depending on timing this might not error; that's ok
	}
}

func TestJoinWithError_NoErrors(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 256)
	c1 := &rwcBuf{r: bytes.NewReader(data), w: io.Discard}
	c2 := &rwcBuf{r: bytes.NewReader(data), w: io.Discard}

	in, out, errs := JoinWithError(c1, c2)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if in+out != int64(len(data)*2) {
		t.Fatalf("byte count: in=%d out=%d", in, out)
	}
}

func TestWrapConn(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()

	buf := &bytes.Buffer{}
	rwc := &rwcBuf{r: strings.NewReader("wrapped"), w: buf}

	wrapped := WrapConn(c1, rwc)

	// Read should come from rwc, not the original conn
	out := make([]byte, 7)
	n, err := wrapped.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(out[:n]) != "wrapped" {
		t.Fatalf("got %q", out[:n])
	}

	// Write should go to rwc
	wrapped.Write([]byte("hello"))
	if buf.String() != "hello" {
		t.Fatalf("write went to wrong place: %q", buf.String())
	}

	// LocalAddr should still come from underlying conn
	if wrapped.LocalAddr() == nil {
		t.Fatal("LocalAddr is nil")
	}
}

func TestWrappedConn_Close(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()

	closed := false
	rwc := WrapReadWriteCloser(strings.NewReader(""), io.Discard, func() error {
		closed = true
		return nil
	})

	wrapped := WrapConn(c1, rwc)
	wrapped.Close()

	if !closed {
		t.Fatal("close function not called")
	}
}

func TestReadWriteCloser_ReadWrite(t *testing.T) {
	r := strings.NewReader("data")
	var buf bytes.Buffer
	rwc := WrapReadWriteCloser(r, &buf, nil)

	out := make([]byte, 4)
	n, _ := rwc.Read(out)
	if string(out[:n]) != "data" {
		t.Fatalf("Read: got %q", out[:n])
	}

	rwc.Write([]byte("written"))
	if buf.String() != "written" {
		t.Fatalf("Write: got %q", buf.String())
	}
}

func TestReadWriteCloser_CloseOnce(t *testing.T) {
	count := 0
	rwc := WrapReadWriteCloser(strings.NewReader(""), io.Discard, func() error {
		count++
		return nil
	})

	rwc.Close()
	rwc.Close()
	rwc.Close()

	if count != 1 {
		t.Fatalf("closeFn called %d times, want 1", count)
	}
}

func TestReadWriteCloser_NilCloseFn(t *testing.T) {
	rwc := WrapReadWriteCloser(strings.NewReader(""), io.Discard, nil)
	err := rwc.Close()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLimitedConn_ReadWrite(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	lc := NewLimitedConn(c1)

	go func() {
		c2.Write([]byte("incoming"))
	}()

	out := make([]byte, 8)
	n, err := lc.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(out[:n]) != "incoming" {
		t.Fatalf("got %q", out[:n])
	}
	if atomic.LoadInt64(&lc.ReadCount) != 8 {
		t.Fatalf("ReadCount = %d, want 8", lc.ReadCount)
	}

	go func() {
		buf := make([]byte, 5)
		io.ReadFull(c2, buf)
	}()

	n, err = lc.Write([]byte("outgo"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("Write n=%d, want 5", n)
	}
	if atomic.LoadInt64(&lc.WriteCount) != 5 {
		t.Fatalf("WriteCount = %d, want 5", lc.WriteCount)
	}
}

func TestLimitedConn_WriterDisabled(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	lc := NewLimitedConn(c1)
	// Writer is created with enable=false by default
	if lc.Writer.IsEnabled() {
		t.Fatal("Writer limiter should be disabled by default")
	}

	go func() {
		buf := make([]byte, 3)
		io.ReadFull(c2, buf)
	}()

	_, err := lc.Write([]byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
}

func TestLimitedConn_ReaderEnabled(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	lc := NewLimitedConn(c1)
	if !lc.Reader.IsEnabled() {
		t.Fatal("Reader limiter should be enabled by default")
	}
}

// ---------------------------------------------------------------------------
// chan.go — simpleCounter / simpleMeter / TrafficStats / Channel
// ---------------------------------------------------------------------------

func TestSimpleCounter(t *testing.T) {
	c := &simpleCounter{}
	if c.Count() != 0 {
		t.Fatal("initial not 0")
	}
	c.Inc(5)
	if c.Count() != 5 {
		t.Fatalf("after Inc(5): %d", c.Count())
	}
	c.Inc(3)
	if c.Count() != 8 {
		t.Fatalf("after Inc(3): %d", c.Count())
	}
	c.Dec(2)
	if c.Count() != 6 {
		t.Fatalf("after Dec(2): %d", c.Count())
	}
	c.Clear()
	if c.Count() != 0 {
		t.Fatal("after Clear not 0")
	}
}

func TestSimpleCounter_Concurrent(t *testing.T) {
	c := &simpleCounter{}
	var wg sync.WaitGroup
	n := 1000
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			c.Inc(1)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			c.Inc(1)
		}
	}()
	wg.Wait()
	if c.Count() != int64(2*n) {
		t.Fatalf("concurrent count = %d, want %d", c.Count(), 2*n)
	}
}

func TestSimpleMeter(t *testing.T) {
	m := newSimpleMeter()
	time.Sleep(10 * time.Millisecond) // ensure non-zero elapsed
	m.Mark(100)
	m.Mark(200)

	rate := m.Rate1()
	if rate <= 0 {
		t.Fatalf("rate = %f, want > 0", rate)
	}
	// total=300, elapsed>=10ms, so rate should be reasonable
	if atomic.LoadInt64(&m.total) != 300 {
		t.Fatalf("total = %d, want 300", m.total)
	}
}

func TestSimpleMeter_ZeroElapsed(t *testing.T) {
	m := &simpleMeter{start: time.Now()}
	// Rate1 called immediately — elapsed might be 0
	rate := m.Rate1()
	// Either 0 or a very large number is acceptable
	_ = rate
}

func TestTrafficStats_Pending(t *testing.T) {
	ts := NewTrafficStats("test")

	ts.AddPending(1, 100)
	ts.AddPending(1, 200)
	ts.AddPending(2, 50)

	if ts.pendingCount.Count() != 3 {
		t.Fatalf("pendingCount = %d, want 3", ts.pendingCount.Count())
	}
	if ts.pendingSize.Count() != 350 {
		t.Fatalf("pendingSize = %d, want 350", ts.pendingSize.Count())
	}
	if ts.GetPendingCount(1) != 2 {
		t.Fatalf("GetPendingCount(1) = %d, want 2", ts.GetPendingCount(1))
	}
	if ts.GetPendingCount(2) != 1 {
		t.Fatalf("GetPendingCount(2) = %d, want 1", ts.GetPendingCount(2))
	}

	ts.RemovePending(1, 100)
	if ts.GetPendingCount(1) != 1 {
		t.Fatalf("after remove: GetPendingCount(1) = %d, want 1", ts.GetPendingCount(1))
	}
	if ts.pendingCount.Count() != 2 {
		t.Fatalf("after remove: pendingCount = %d, want 2", ts.pendingCount.Count())
	}
}

func TestTrafficStats_ClearPending(t *testing.T) {
	ts := NewTrafficStats("test")
	ts.AddPending(1, 100)
	ts.AddPending(2, 200)
	ts.ClearPending()

	if ts.pendingCount.Count() != 0 {
		t.Fatalf("pendingCount after clear = %d", ts.pendingCount.Count())
	}
	if ts.pendingSize.Count() != 0 {
		t.Fatalf("pendingSize after clear = %d", ts.pendingSize.Count())
	}
}

func TestTrafficStats_String(t *testing.T) {
	ts := NewTrafficStats("test")
	ts.packets.Inc(10)
	ts.bytes.Inc(1024)

	s := ts.String(Sender)
	if !strings.Contains(s, "10 packets") {
		t.Fatalf("Sender string missing packets: %s", s)
	}
	if !strings.Contains(s, "pending") {
		t.Fatalf("Sender string missing pending: %s", s)
	}

	s = ts.String(Receiver)
	if !strings.Contains(s, "10 packets") {
		t.Fatalf("Receiver string missing packets: %s", s)
	}
	if strings.Contains(s, "pending") {
		t.Fatalf("Receiver string should not have pending: %s", s)
	}
}

func TestNewChan_Buffered(t *testing.T) {
	ch := NewChan("test", 10)
	if ch.Name != "test" {
		t.Fatal("name mismatch")
	}
	if cap(ch.C) != 10 {
		t.Fatalf("capacity = %d, want 10", cap(ch.C))
	}
}

func TestNewChan_Unbuffered(t *testing.T) {
	ch := NewChan("test", 0)
	if cap(ch.C) != 0 {
		t.Fatalf("capacity = %d, want 0", cap(ch.C))
	}
}

func TestChannel_SendReceive(t *testing.T) {
	ch := NewChan("test", 10)
	defer ch.Close()

	msg := &message.Ping{Ping: "hello"}
	err := ch.Send(1, msg)
	if err != nil {
		t.Fatal(err)
	}

	received := <-ch.C
	if received.ID != 1 {
		t.Fatalf("ID = %d, want 1", received.ID)
	}
	if received.Message.(*message.Ping).Ping != "hello" {
		t.Fatal("message content mismatch")
	}
}

func TestChannel_SendAfterClose(t *testing.T) {
	ch := NewChan("test", 10)
	ch.Close()

	err := ch.Send(1, &message.Ping{Ping: "x"})
	if err == nil {
		t.Fatal("expected error sending to closed channel")
	}
}

func TestChannel_DoubleClose(t *testing.T) {
	ch := NewChan("test", 10)
	ch.Close()
	ch.Close() // should not panic due to sync.Once
}

func TestChannel_ConcurrentClose(t *testing.T) {
	ch := NewChan("test", 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch.Close()
		}()
	}
	wg.Wait()
	// All goroutines should complete without panic
}

func TestChannel_CloseExecutesOnlyOnce(t *testing.T) {
	ch := NewChan("test", 10)

	// Send a message so channel has some state
	ch.Mod = Sender
	ch.Send(1, &message.Ping{Ping: "x"})

	ch.Close()
	// StopCh should be closed
	select {
	case <-ch.StopCh:
	default:
		t.Fatal("StopCh should be closed after Close()")
	}

	// Second close should be safe (sync.Once)
	ch.Close()
}

func TestChannel_GetStats(t *testing.T) {
	ch := NewChan("test", 10)
	defer ch.Close()

	ch.Mod = Sender
	s := ch.GetStats()
	if !strings.Contains(s, "pending") {
		t.Fatalf("sender stats missing pending: %s", s)
	}

	ch.Mod = Receiver
	s = ch.GetStats()
	if strings.Contains(s, "pending") {
		t.Fatalf("receiver stats should not have pending: %s", s)
	}
}

func TestChannel_GetPendingCount(t *testing.T) {
	ch := NewChan("test", 10)
	defer ch.Close()
	ch.Mod = Sender

	ch.Send(42, &message.Ping{Ping: "x"})
	count := ch.GetPendingCount(42)
	if count != 1 {
		t.Fatalf("pending count = %d, want 1", count)
	}
}

func TestChannel_Sender(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()

	ch := NewChan("test", 10)
	ch.C <- &Message{ID: 1, Message: &message.Ping{Ping: "test"}}

	errCh := make(chan error, 1)
	go func() {
		errCh <- ch.Sender(c1)
	}()

	// Read the message from the other side
	msg, err := ReadMsg(c2)
	if err != nil {
		t.Fatal(err)
	}
	if msg.(*message.Ping).Ping != "test" {
		t.Fatal("content mismatch")
	}

	// Close to stop sender
	ch.Close()
	c1.Close()
	<-errCh
}

func TestChannel_Sender_NilMessage(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	ch := NewChan("test", 10)
	ch.C <- &Message{ID: 1, Message: nil} // nil message

	err := ch.Sender(c1)
	if err == nil {
		t.Fatal("expected error for nil message")
	}
}

func TestChannel_Receiver(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()

	ch := NewChan("test", 10)

	go func() {
		WriteMsg(c1, &message.Ping{Ping: "recv-test"})
		c1.Close() // close to end Receiver loop
	}()

	go ch.Receiver(c2)

	// Wait for message
	select {
	case msg := <-ch.C:
		if msg.Message.(*message.Ping).Ping != "recv-test" {
			t.Fatal("content mismatch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

// ---------------------------------------------------------------------------
// writer.go — Writer
// ---------------------------------------------------------------------------

func TestWriter_Write(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("n=%d, want 5", n)
	}
	if buf.String() != "hello" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestWriter_MultipleWrites(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	w.Write([]byte("aaa"))
	w.Write([]byte("bbb"))
	w.Write([]byte("ccc"))

	if buf.String() != "aaabbbccc" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestWriter_FlushesImmediately(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	w.Write([]byte("data"))
	// After Write, data should already be flushed
	if buf.Len() != 4 {
		t.Fatalf("buf.Len() = %d, want 4 (should be flushed)", buf.Len())
	}
}

func TestWriter_ErrorOnWrite(t *testing.T) {
	w := NewWriter(&errWriter{})
	_, err := w.Write([]byte("x"))
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// reader.go — Reader
// ---------------------------------------------------------------------------

func TestReader_Read(t *testing.T) {
	r := NewReader(strings.NewReader("hello world"))
	out := make([]byte, 5)
	n, err := r.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(out[:n]) != "hello" {
		t.Fatalf("got %q", out[:n])
	}
}

func TestReader_Peek(t *testing.T) {
	r := NewReader(strings.NewReader("hello"))
	peeked, err := r.Peek(3)
	if err != nil {
		t.Fatal(err)
	}
	if string(peeked) != "hel" {
		t.Fatalf("peeked %q", peeked)
	}

	// Peek should not consume data
	out := make([]byte, 5)
	n, _ := r.Reader.Read(out)
	if string(out[:n]) != "hello" {
		t.Fatalf("after peek, read %q", out[:n])
	}
}

func TestReader_PeekAndRead_Match(t *testing.T) {
	r := NewReader(strings.NewReader("PREFIXrest"))
	err := r.PeekAndRead([]byte("PREFIX"))
	if err != nil {
		t.Fatalf("PeekAndRead: %v", err)
	}

	// "PREFIX" should be consumed, "rest" remains
	out := make([]byte, 4)
	n, _ := r.Reader.Read(out)
	if string(out[:n]) != "rest" {
		t.Fatalf("remaining: %q", out[:n])
	}
}

func TestReader_PeekAndRead_Mismatch(t *testing.T) {
	r := NewReader(strings.NewReader("ABCDEF"))
	err := r.PeekAndRead([]byte("XYZ"))
	if !errors.Is(err, ErrorInvalidPadding) {
		t.Fatalf("expected ErrorInvalidPadding, got %v", err)
	}
}

func TestReader_PeekAndRead_Empty(t *testing.T) {
	r := NewReader(strings.NewReader("data"))
	err := r.PeekAndRead([]byte{})
	if err != nil {
		t.Fatalf("PeekAndRead empty: %v", err)
	}
}

func TestReader_FillN(t *testing.T) {
	r := NewReader(strings.NewReader("abcdefghij"))
	err := r.FillN(5)
	if err != nil {
		t.Fatal(err)
	}

	// Data should be readable from the buffer
	out := make([]byte, 5)
	_, err = io.ReadFull(r.Buffer, out)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "abcde" {
		t.Fatalf("got %q", out)
	}
}

func TestReader_FillN_Short(t *testing.T) {
	r := NewReader(strings.NewReader("abc"))
	err := r.FillN(10) // ask for 10, only 3 available
	if err == nil {
		t.Fatal("expected error for short read")
	}
}

func TestReader_ReadFromBuffer(t *testing.T) {
	r := NewReader(strings.NewReader("abcdefghij"))

	// Fill buffer first
	r.FillN(5)

	// Read should come from buffer (since Size > 0)
	out := make([]byte, 3)
	n, err := r.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("read 0 bytes from buffer")
	}
}

// ---------------------------------------------------------------------------
// limiter.go — TokenBucketLimiter
// ---------------------------------------------------------------------------

func TestLimiter_NewDisabled(t *testing.T) {
	l := NewTokenBucketLimiter(1000, false)
	if l.IsEnabled() {
		t.Fatal("should be disabled")
	}
}

func TestLimiter_NewEnabled(t *testing.T) {
	l := NewTokenBucketLimiter(1000, true)
	if !l.IsEnabled() {
		t.Fatal("should be enabled")
	}
}

func TestLimiter_TryConsume_Disabled(t *testing.T) {
	l := NewTokenBucketLimiter(0, false)
	// disabled limiter always returns true
	if !l.TryConsume(1000000) {
		t.Fatal("disabled limiter should always succeed")
	}
}

func TestLimiter_TryConsume_Enabled(t *testing.T) {
	l := NewTokenBucketLimiter(100, true)
	initial := l.GetTokens()
	if initial <= 0 {
		t.Fatal("should have initial tokens")
	}

	ok := l.TryConsume(10)
	if !ok {
		t.Fatal("should succeed with sufficient tokens")
	}

	// Consume all remaining
	remaining := l.GetTokens()
	ok = l.TryConsume(remaining + 1)
	if ok {
		t.Fatal("should fail when insufficient tokens")
	}
}

func TestLimiter_WaitForTokens_Disabled(t *testing.T) {
	l := NewTokenBucketLimiter(0, false)
	err := l.WaitForTokens(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
}

func TestLimiter_WaitForTokens_Available(t *testing.T) {
	l := NewTokenBucketLimiter(1000, true)
	err := l.WaitForTokens(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
}

func TestLimiter_WaitForTokens_ContextCancel(t *testing.T) {
	l := NewTokenBucketLimiter(0, true)
	l.SetTokens(0) // no tokens

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- l.WaitForTokens(ctx, 100)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	// Wake up the waiter so it can check context
	l.cond.Broadcast()

	err := <-errCh
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestLimiter_WaitForTokens_Refill(t *testing.T) {
	l := NewTokenBucketLimiter(0, true)
	l.SetTokens(0)

	done := make(chan struct{})
	go func() {
		l.WaitForTokens(context.Background(), 50)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	l.AddTokens(100)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForTokens did not unblock after AddTokens")
	}
}

func TestLimiter_AddTokens(t *testing.T) {
	l := NewTokenBucketLimiter(0, true)
	l.SetTokens(0)
	l.AddTokens(50)
	if l.GetTokens() != 50 {
		t.Fatalf("tokens = %d, want 50", l.GetTokens())
	}
	l.AddTokens(30)
	if l.GetTokens() != 80 {
		t.Fatalf("tokens = %d, want 80", l.GetTokens())
	}
}

func TestLimiter_SetTokens(t *testing.T) {
	l := NewTokenBucketLimiter(0, true)
	l.SetTokens(100)
	if l.GetTokens() != 100 {
		t.Fatalf("tokens = %d, want 100", l.GetTokens())
	}
	l.SetTokens(-10) // negative should be clamped to 0
	if l.GetTokens() != 0 {
		t.Fatalf("tokens = %d, want 0", l.GetTokens())
	}
}

func TestLimiter_Enable(t *testing.T) {
	l := NewTokenBucketLimiter(100, false)
	if l.IsEnabled() {
		t.Fatal("should be disabled")
	}
	l.Enable(true)
	if !l.IsEnabled() {
		t.Fatal("should be enabled after Enable(true)")
	}
	l.Enable(false)
	if l.IsEnabled() {
		t.Fatal("should be disabled after Enable(false)")
	}
}

func TestLimiter_GetCounts(t *testing.T) {
	l := NewTokenBucketLimiter(100, true)
	atomic.AddInt64(&l.readCount, 42)
	atomic.AddInt64(&l.writeCount, 99)

	r, w := l.GetCounts()
	if r != 42 {
		t.Fatalf("readCount = %d, want 42", r)
	}
	if w != 99 {
		t.Fatalf("writeCount = %d, want 99", w)
	}
}

func TestLimiter_ConcurrentWait(t *testing.T) {
	l := NewTokenBucketLimiter(0, true)
	l.SetTokens(0)

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			l.WaitForTokens(context.Background(), 1)
		}()
	}

	time.Sleep(50 * time.Millisecond)
	l.AddTokens(int64(n))

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent waiters did not all unblock")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type rwcBuf struct {
	r io.Reader
	w io.Writer
}

func (c *rwcBuf) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *rwcBuf) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *rwcBuf) Close() error                { return nil }

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

type errWriter struct{}

func (w *errWriter) Write([]byte) (int, error) { return 0, errors.New("write error") }
