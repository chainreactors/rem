//go:build oss_integration
// +build oss_integration

package simplex

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	hsimplex "github.com/chainreactors/rem/harness/simplex"
)

// Real OSS integration tests — scheme-agnostic harness usage.
//
// Run:
//   go test -tags "oss,oss_integration" -run TestOSS -v -timeout 30m ./x/simplex/

const ossTestInterval = 100 // 100ms polling

func ossIntegrationConfig(t *testing.T) (ak, sk, bucket, endpoint string) {
	t.Helper()

	ak = os.Getenv("OSS_AK")
	sk = os.Getenv("OSS_SK")
	bucket = os.Getenv("OSS_BUCKET")
	endpoint = os.Getenv("OSS_ENDPOINT")
	var missing []string
	for _, item := range []struct {
		key   string
		value string
	}{
		{"OSS_AK", ak},
		{"OSS_SK", sk},
		{"OSS_BUCKET", bucket},
		{"OSS_ENDPOINT", endpoint},
	} {
		if item.value == "" {
			missing = append(missing, item.key)
		}
	}
	if len(missing) > 0 {
		t.Skipf("set %s to run live OSS harness", strings.Join(missing, ", "))
	}
	return
}

func ossIntegrationExtraQuery() string {
	q := url.Values{}
	if max := os.Getenv("OSS_MAX"); max != "" {
		q.Set("max", max)
	}
	if proxy := os.Getenv("OSS_PROXY"); proxy != "" {
		q.Set("proxy", proxy)
	}
	if len(q) == 0 {
		return ""
	}
	return "&" + q.Encode()
}

func ossIntegrationURL(t *testing.T, interval int) string {
	t.Helper()

	ak, sk, bucket, endpoint := ossIntegrationConfig(t)
	prefix := fmt.Sprintf("rem-harness-%s", randomString(8))
	return fmt.Sprintf("oss://%s.%s/%s/?ak=%s&sk=%s&mode=file&interval=%d%s",
		bucket, endpoint, prefix, ak, sk, interval, ossIntegrationExtraQuery())
}

func ossSeqIntegrationURL(t *testing.T, interval int) string {
	t.Helper()

	ak, sk, bucket, endpoint := ossIntegrationConfig(t)
	prefix := fmt.Sprintf("rem-seq-%s", randomString(8))
	return fmt.Sprintf("oss://%s.%s/%s/?ak=%s&sk=%s&mode=file&interval=%d&seq=true%s",
		bucket, endpoint, prefix, ak, sk, interval, ossIntegrationExtraQuery())
}

// ── Factories ────────────────────────────────────────────────

func ossPairFactory(t *testing.T) (net.PacketConn, net.PacketConn, func(), error) {
	t.Helper()
	s, c, stop := simplexPairFromURL(t, "oss", ossIntegrationURL(t, ossTestInterval))
	return s, c, stop, nil
}

func ossARQPipelineFactory(t *testing.T) (net.Conn, net.Conn, func(), error) {
	t.Helper()

	urlStr := ossIntegrationURL(t, ossTestInterval)
	s, c, stop := simplexPipelineFromURLs(t, "oss", urlStr, urlStr)
	return s, c, stop, nil
}

// ── Smoke ────────────────────────────────────────────────────

func TestOSS_Connectivity(t *testing.T) {
	ak, _, bucket, endpoint := ossIntegrationConfig(t)
	t.Logf("OSS: %s.%s (ak=%s...) interval=%dms", bucket, endpoint, ak[:8], ossTestInterval)
}

func TestOSS_Harness(t *testing.T) {
	ossIntegrationConfig(t)
	hsimplex.TestAll(t, hsimplex.Config{
		PacketConn:  ossPairFactory,
		ARQConn:     ossARQPipelineFactory,
		Full:        true,
		SkipNetConn: true,
	})
}

// ── Sequence Mode ───────────────────────────────────────────

func ossSeqPairFactory(t *testing.T) (net.PacketConn, net.PacketConn, func(), error) {
	t.Helper()
	s, c, stop := simplexPairFromURL(t, "oss", ossSeqIntegrationURL(t, ossTestInterval))
	return s, c, stop, nil
}

func ossSeqARQPipelineFactory(t *testing.T) (net.Conn, net.Conn, func(), error) {
	t.Helper()

	urlStr := ossSeqIntegrationURL(t, ossTestInterval)
	s, c, stop := simplexPipelineFromURLs(t, "oss", urlStr, urlStr)
	return s, c, stop, nil
}

func TestOSSSeq_Connectivity(t *testing.T) {
	ak, _, bucket, endpoint := ossIntegrationConfig(t)
	t.Logf("OSS-Seq: %s.%s (ak=%s...) interval=%dms", bucket, endpoint, ak[:8], ossTestInterval)
}

func TestOSSSeq_Harness(t *testing.T) {
	ossIntegrationConfig(t)
	hsimplex.TestAll(t, hsimplex.Config{
		PacketConn:  ossSeqPairFactory,
		ARQConn:     ossSeqARQPipelineFactory,
		Full:        true,
		SkipNetConn: true,
	})
}

func TestOSSSeq_PerfLargeTransfer(t *testing.T) {
	sizeMB, _ := strconv.Atoi(os.Getenv("OSS_PERF_MB"))
	if sizeMB <= 0 {
		t.Skip("set OSS_PERF_MB to run large OSS throughput test")
	}

	size := sizeMB * 1024 * 1024
	timeout := 15 * time.Minute
	if timeoutSec, _ := strconv.Atoi(os.Getenv("OSS_PERF_TIMEOUT_SEC")); timeoutSec > 0 {
		timeout = time.Duration(timeoutSec) * time.Second
	}
	direction := os.Getenv("OSS_PERF_DIRECTION")
	if direction == "" {
		direction = "c2s"
	}

	hsimplex.TestPerfLargeTransfer(t, ossSeqARQPipelineFactory, hsimplex.PerfOptions{
		Label:          fmt.Sprintf("PerfLargeTransfer_%s_%dMB", direction, sizeMB),
		Size:           size,
		Direction:      hsimplex.Direction(direction),
		Timeout:        timeout,
		ChunkSize:      1024 * 1024,
		TheoreticalMiB: simplexTheoreticalMiB,
		Stats:          simplexARQStatsForLog,
	})
}
