package simplex

import (
	"context"
	"crypto/tls"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
)

// testHTTPTransport 仅用于测试：非 nil 时 createHTTPClient 直接使用该 transport
var testHTTPTransport http.RoundTripper

const graphHTTPDefaultTimeout = 30 * time.Second

type graphBaseRewriteTransport struct {
	targetURL string
	upstream  http.RoundTripper
}

func (t *graphBaseRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.targetURL)
	if err != nil {
		return nil, err
	}

	clone := req.Clone(req.Context())
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	clone.Host = target.Host

	return t.upstream.RoundTrip(clone)
}

// createHTTPClient 根据配置创建 HTTP 客户端
// timeout=0 时使用默认值
func createHTTPClient(proxyURL string, timeout ...time.Duration) *http.Client {
	t := graphHTTPDefaultTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		t = timeout[0]
	}
	if testHTTPTransport != nil {
		return &http.Client{Timeout: t, Transport: testHTTPTransport}
	}

	transport := http.RoundTripper(&http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   t,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		// force HTTP/1.1: prevent "malformed HTTP version HTTP/2" errors when
		// servers (e.g. login.microsoftonline.com) respond with HTTP/2
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		TLSHandshakeTimeout:   t,
		ResponseHeaderTimeout: t,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		Proxy:                 http.ProxyFromEnvironment,
	})

	if proxyURL == "direct" {
		// proxy=direct: 强制直连，忽略 HTTP_PROXY / HTTPS_PROXY 环境变量
		if httpTransport, ok := transport.(*http.Transport); ok {
			httpTransport.Proxy = nil
		}
	} else if proxyURL != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			// 使用内部的 proxyclient 库，覆盖环境变量代理
			proxyDial, err := proxyclient.NewClient(parsedURL)
			if err == nil {
				if httpTransport, ok := transport.(*http.Transport); ok {
					httpTransport.DialContext = proxyDial
					httpTransport.Proxy = nil // explicit proxy takes precedence
				}
			}
		}
	}

	if graphBaseURL := os.Getenv("REM_GRAPH_BASE_URL"); graphBaseURL != "" {
		transport = &graphBaseRewriteTransport{
			targetURL: graphBaseURL,
			upstream:  transport,
		}
	}

	return &http.Client{
		Timeout:   t,
		Transport: transport,
	}
}

func generateAddrFromPath(id string, addr *SimplexAddr) *SimplexAddr {
	ip := net.IPv4(169, 254, byte(djb2Hash(addr.Host)), byte(djb2Hash(id)))
	newAddr := addr.Clone(ip.String())
	newAddr.Path = id
	newAddr.id = id
	return newAddr
}

func djb2Hash(input string) uint32 {
	var hash uint32 = 5381
	for _, char := range input {
		hash = ((hash << 5) + hash) + uint32(char)
	}
	return hash
}

// globalRand 是全局共享的随机数生成器，避免并发调用时 seed 相同导致重复。
var globalRand = rand.New(rand.NewSource(time.Now().UnixNano()))
var globalRandMu sync.Mutex

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	globalRandMu.Lock()
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[globalRand.Intn(len(charset))]
	}
	globalRandMu.Unlock()
	return string(result)
}

// 辅助函数
func getOption(addr *SimplexAddr, key string) string {
	if fromOptions := addr.options.Get(key); fromOptions != "" {
		return fromOptions
	}

	if fromEnv := os.Getenv(key); fromEnv != "" {
		return fromEnv
	}

	return ""
}

func normalizeDirectoryPrefix(prefix string, leadingSlash bool) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		if leadingSlash {
			return "/"
		}
		return ""
	}
	if leadingSlash {
		return "/" + prefix + "/"
	}
	return prefix + "/"
}

// validateSeed checks that a seed contains only alphanumeric characters and
// is between 4 and 32 characters long.
func validateSeed(seed string) error {
	if len(seed) < 4 || len(seed) > 32 {
		return fmt.Errorf("seed must be 4-32 characters, got %d", len(seed))
	}
	for _, c := range seed {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return fmt.Errorf("seed contains invalid character: %c", c)
		}
	}
	return nil
}

// parseSeedTimestamp splits an identifier like "abcd1234_1710374400" into
// seed="abcd1234" and timestamp=1710374400. It uses the last underscore as
// the separator so seeds themselves may contain underscores in the future.
func parseSeedTimestamp(id string) (seed string, ts int64, err error) {
	idx := strings.LastIndex(id, "_")
	if idx < 0 {
		return "", 0, fmt.Errorf("no underscore in id %q", id)
	}
	seed = id[:idx]
	if seed == "" {
		return "", 0, fmt.Errorf("empty seed in id %q", id)
	}
	ts, err = strconv.ParseInt(id[idx+1:], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("invalid timestamp in id %q: %v", id, err)
	}
	return seed, ts, nil
}

// formatSeedTimestamp produces a client identifier from seed and timestamp.
func formatSeedTimestamp(seed string, ts int64) string {
	return fmt.Sprintf("%s_%d", seed, ts)
}

// SessionTrackable is implemented by session entries that support idle cleanup.
type SessionTrackable interface {
	LastActive() time.Time
}

// StartSessionCleanup starts a background goroutine that periodically scans a sync.Map
// and deletes entries whose LastActive exceeds the timeout.
// Values in the map must implement SessionTrackable.
// onCleanup (optional) is called with (key, value) before deleting an idle entry,
// allowing transport-specific resource cleanup (e.g. deleting cloud storage lists).
func StartSessionCleanup(ctx context.Context, sessions *sync.Map,
	interval time.Duration, timeout time.Duration, logPrefix string,
	onCleanup func(key, value interface{})) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				sessions.Range(func(key, value interface{}) bool {
					if entry, ok := value.(SessionTrackable); ok {
						if now.Sub(entry.LastActive()) > timeout {
							if onCleanup != nil {
								onCleanup(key, value)
							}
							sessions.Delete(key)
							logs.Log.Infof("%s Session cleanup: %v idle for %v", logPrefix, key, timeout)
						}
					}
					return true
				})
			}
		}
	}()
}
