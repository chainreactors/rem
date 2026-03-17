//go:build oss

package simplex

import "testing"

func TestRegisteredOSSSimplexResolvers(t *testing.T) {
	const scheme = "oss"
	const address = "oss://bucket.oss-cn-shanghai.aliyuncs.com/rem?bucket=test-bucket&server=:8080&interval=1000&wrapper=raw"

	if _, err := GetSimplexClient(scheme); err != nil {
		t.Fatalf("GetSimplexClient(%q) error: %v", scheme, err)
	}
	if _, err := GetSimplexServer(scheme); err != nil {
		t.Fatalf("GetSimplexServer(%q) error: %v", scheme, err)
	}

	addr, err := ResolveSimplexAddr(scheme, address)
	if err != nil {
		t.Fatalf("ResolveSimplexAddr(%q) error: %v", scheme, err)
	}
	if got := addr.Network(); got != scheme {
		t.Fatalf("unexpected simplex addr network: got %q want %q", got, scheme)
	}
}
