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

// createHTTPClient 根据配置创建 HTTP 客户端
func createHTTPClient(proxyURL string) *http.Client {
	if testHTTPTransport != nil {
		return &http.Client{Timeout: 30 * time.Second, Transport: testHTTPTransport}
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		DisableKeepAlives: true,
		Proxy:             http.ProxyFromEnvironment,
	}

	if proxyURL != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			// 使用内部的 proxyclient 库，覆盖环境变量代理
			proxyDial, err := proxyclient.NewClient(parsedURL)
			if err == nil {
				transport.DialContext = proxyDial
				transport.Proxy = nil // explicit proxy takes precedence
			}
		}
	}

	return &http.Client{
		Timeout:   30 * time.Second,
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

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var seededRand = rand.New(rand.NewSource(time.Now().UnixNano()))
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[seededRand.Intn(len(charset))]
	}
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
func StartSessionCleanup(ctx context.Context, sessions *sync.Map,
	interval time.Duration, timeout time.Duration, logPrefix string) {
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
