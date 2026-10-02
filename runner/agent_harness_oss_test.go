//go:build oss_integration && !tinygo

package runner

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	hagent "github.com/chainreactors/rem/harness/agent"
)

func ossAgentConfig(t *testing.T) (ak, sk, bucket, endpoint string) {
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
		t.Skipf("set %s to run OSS agent harness", strings.Join(missing, ", "))
	}
	return
}

func ossAgentServerURL(t *testing.T, seq bool) string {
	t.Helper()

	ak, sk, bucket, endpoint := ossAgentConfig(t)
	prefix := os.Getenv("OSS_PREFIX")
	if prefix == "" {
		prefix = fmt.Sprintf("rem-agent-%d", atomic.AddUint32(&testCounter, 1))
	}
	prefix = strings.Trim(prefix, "/")
	q := url.Values{}
	q.Set("ak", ak)
	q.Set("sk", sk)
	q.Set("mode", "file")
	q.Set("interval", "100")
	q.Set("wrapper", "raw")
	if seq {
		q.Set("seq", "true")
	}
	if max := os.Getenv("OSS_MAX"); max != "" {
		q.Set("max", max)
	}
	if proxy := os.Getenv("OSS_PROXY"); proxy != "" {
		q.Set("proxy", proxy)
	}
	return fmt.Sprintf("simplex+oss://%s.%s/%s/?%s", bucket, endpoint, prefix, q.Encode())
}

func TestOSSSeq_AgentHarness(t *testing.T) {
	makeEnv := func(t *testing.T) (*hagent.Env, func(), error) {
		env, cleanup := setupGenericAgentEnv(t, agentHarnessConfig{
			ServerURL: ossAgentServerURL(t, true),
		})
		return env, cleanup, nil
	}
	makeMultiEnv := func(t *testing.T, clientCount int) (*hagent.MultiClientEnv, func(), error) {
		env, cleanup := setupGenericMultiAgentEnv(t, agentHarnessConfig{
			ServerURL: ossAgentServerURL(t, true),
		}, clientCount)
		return env, cleanup, nil
	}

	hagent.TestAllWithMultiClient(t, makeEnv, makeMultiEnv)
}

func TestOSS_AgentHarness(t *testing.T) {
	makeEnv := func(t *testing.T) (*hagent.Env, func(), error) {
		env, cleanup := setupGenericAgentEnv(t, agentHarnessConfig{
			ServerURL: ossAgentServerURL(t, false),
		})
		return env, cleanup, nil
	}
	makeMultiEnv := func(t *testing.T, clientCount int) (*hagent.MultiClientEnv, func(), error) {
		env, cleanup := setupGenericMultiAgentEnv(t, agentHarnessConfig{
			ServerURL: ossAgentServerURL(t, false),
		}, clientCount)
		return env, cleanup, nil
	}

	hagent.TestProxyWithMultiClient(t, makeEnv, makeMultiEnv)
}
