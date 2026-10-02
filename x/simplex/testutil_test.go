package simplex

import (
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	hsimplex "github.com/chainreactors/rem/harness/simplex"
	"github.com/chainreactors/rem/x/arq"
)

// ============================================================
// Simplex 层共享测试 helpers
//
// 本文件不含 build tag，所有 simplex 测试均可使用。
// ============================================================

// makePayload 生成确定性的测试数据。
func makePayload(size int) []byte {
	p := make([]byte, size)
	for i := range p {
		p[i] = byte((i*7 + 13) % 251)
	}
	return p
}

// readExact 从 conn 读取恰好 n 字节，带超时。
func readExact(conn interface{ Read([]byte) (int, error) }, n int, timeout time.Duration) ([]byte, error) {
	return readExactChunked(conn, n, timeout, 65536)
}

func readExactChunked(conn interface{ Read([]byte) (int, error) }, n int, timeout time.Duration, chunkSize int) ([]byte, error) {
	if chunkSize <= 0 {
		chunkSize = 65536
	}

	result := make([]byte, 0, n)
	buf := make([]byte, chunkSize)
	deadline := time.Now().Add(timeout)
	if d, ok := conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = d.SetReadDeadline(deadline)
		defer d.SetReadDeadline(time.Time{})
	}

	for len(result) < n {
		if time.Now().After(deadline) {
			return result, io.ErrNoProgress
		}
		remaining := n - len(result)
		readSize := len(buf)
		if readSize > remaining {
			readSize = remaining
		}
		nr, err := conn.Read(buf[:readSize])
		if nr > 0 {
			result = append(result, buf[:nr]...)
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func writeInChunks(conn interface{ Write([]byte) (int, error) }, payload []byte, chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = 64 * 1024
	}
	written := 0
	for written < len(payload) {
		chunk := chunkSize
		if chunk > len(payload)-written {
			chunk = len(payload) - written
		}
		n, err := conn.Write(payload[written : written+chunk])
		if err != nil {
			return err
		}
		written += n
	}
	return nil
}

// sizeLabel 格式化大小标签 (e.g. "1MB", "512KB")。
func sizeLabel(size int) string {
	if size >= 1024*1024 {
		return fmt.Sprintf("%dMB", size/(1024*1024))
	}
	return fmt.Sprintf("%dKB", size/1024)
}

// withTimeout 包装测试函数，确保在超时内完成，防止死锁。
func withTimeout(t *testing.T, timeout time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("测试超时 (%v)，可能存在死锁", timeout)
	}
}

// countSyncMap 统计 sync.Map 中的 entry 数量。
func countSyncMap(m *sync.Map) int {
	count := 0
	m.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// ── 通用 Simplex Pipeline (任意 transport) ──────────────────

// simplexPipelineFromURLs 从 server/client URL 创建 ARQ+Simplex pipeline。
// 适用于任何已注册的 simplex transport (http, dns, oss, sharepoint, onedrive)。
func simplexPipelineFromURLs(t *testing.T, scheme, serverURL, clientURL string) (serverConn, clientConn net.Conn, stop func()) {
	t.Helper()

	simplexServer, err := NewSimplexServer(scheme, serverURL)
	if err != nil {
		t.Fatalf("NewSimplexServer(%s): %v", scheme, err)
	}

	clientAddr, err := ResolveSimplexAddr(scheme, clientURL)
	if err != nil {
		simplexServer.cancel()
		t.Fatalf("ResolveSimplexAddr: %v", err)
	}
	simplexClient, err := NewSimplexClient(clientAddr)
	if err != nil {
		simplexServer.cancel()
		t.Fatalf("NewSimplexClient: %v", err)
	}

	arqCfg := simplexServer.Addr().ARQConfig()
	arqListener, err := arq.ServeConnWithConfig(simplexServer, arqCfg, false)
	if err != nil {
		simplexClient.Close()
		simplexServer.cancel()
		t.Fatalf("ServeConnWithConfig: %v", err)
	}

	clientSession := arq.NewARQSessionWithConfig(simplexClient, simplexClient.Addr(), arqCfg)

	srvConn, err := arqListener.Accept()
	if err != nil {
		clientSession.Close()
		arqListener.Close()
		simplexClient.Close()
		simplexServer.cancel()
		t.Fatalf("Accept: %v", err)
	}

	return srvConn, clientSession, func() {
		clientSession.Close()
		arqListener.Close()
		simplexClient.Close()
		simplexServer.Close()
	}
}

// simplexHarnessMakePipeline 创建 harness/simplex 兼容的 MakePipeline 工厂。
func simplexHarnessMakePipeline(scheme, serverURL, clientURL string) hsimplex.MakePipeline {
	return func(t *testing.T) (net.Conn, net.Conn, func(), error) {
		s, c, stop := simplexPipelineFromURLs(t, scheme, serverURL, clientURL)
		return s, c, stop, nil
	}
}

// simplexPipelineFactory 创建 harness/simplex 兼容的 ARQ net.Conn factory。
func simplexPipelineFactory(scheme string, urlFn func() string) hsimplex.MakePipeline {
	return func(t *testing.T) (net.Conn, net.Conn, func(), error) {
		urlStr := urlFn()
		s, c, stop := simplexPipelineFromURLs(t, scheme, urlStr, urlStr)
		return s, c, stop, nil
	}
}

// simplexPairFromURL 从 URL 创建 SimplexServer + SimplexClient pair (PacketConn 层，无 ARQ)。
func simplexPairFromURL(t *testing.T, scheme, urlStr string) (server, client net.PacketConn, stop func()) {
	t.Helper()

	simplexServer, err := NewSimplexServer(scheme, urlStr)
	if err != nil {
		t.Fatalf("NewSimplexServer(%s): %v", scheme, err)
	}

	clientAddr, err := ResolveSimplexAddr(scheme, urlStr)
	if err != nil {
		simplexServer.cancel()
		t.Fatalf("ResolveSimplexAddr: %v", err)
	}
	simplexClient, err := NewSimplexClient(clientAddr)
	if err != nil {
		simplexServer.cancel()
		t.Fatalf("NewSimplexClient: %v", err)
	}

	return simplexServer, simplexClient, func() {
		simplexClient.Close()
		simplexServer.Close()
	}
}

// simplexPairFactory 创建 harness/simplex 兼容的 SimplexPairFactory。
func simplexPairFactory(scheme string, urlFn func() string) hsimplex.SimplexPairFactory {
	return func(t *testing.T) (net.PacketConn, net.PacketConn, func(), error) {
		s, c, stop := simplexPairFromURL(t, scheme, urlFn())
		return s, c, stop, nil
	}
}

func simplexTheoreticalMiB(server, client net.Conn) float64 {
	for _, conn := range []net.Conn{client, server} {
		if addr, ok := conn.LocalAddr().(*SimplexAddr); ok {
			sc := addr.SimplexConfig()
			if sc.Interval > 0 {
				return float64(sc.DataBudget()) / sc.Interval.Seconds() / 1024 / 1024
			}
		}
	}
	return 0
}

func simplexARQStatsForLog(conn net.Conn) string {
	if statsConn, ok := conn.(interface{ Stats() arq.ARQStats }); ok {
		st := statsConn.Stats()
		errText := "<nil>"
		if st.Err != nil {
			errText = st.Err.Error()
		}
		return fmt.Sprintf("{sndq=%d sndbuf=%d sndpending=%d sndbytes=%d rcvq=%d rcvbuf=%d snd=%d/%d rcvnxt=%d wnd=%d mtu=%d rto=%d err=%s}",
			st.SndQueue, st.SndBuf, st.SndPending, st.SndBytes, st.RcvQueue, st.RcvBuf,
			st.SndUna, st.SndNxt, st.RcvNxt, st.WndSize, st.MTU, st.RTO, errText)
	}
	return "{}"
}
