//go:build dns

package simplex

import "testing"

func TestRegisteredDNSSimplexResolvers(t *testing.T) {
	cases := []struct {
		scheme  string
		address string
	}{
		{scheme: "dns", address: "dns://127.0.0.1:53/?domain=test.local&interval=100&max=128&wrapper=raw"},
		{scheme: "doh", address: "doh://dns.google/dns-query?domain=test.local&interval=100&max=128&wrapper=raw"},
		{scheme: "dot", address: "dot://1.1.1.1:853/?domain=test.local&interval=100&max=128&wrapper=raw"},
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
			if got := addr.Network(); got != tc.scheme {
				t.Fatalf("unexpected simplex addr network: got %q want %q", got, tc.scheme)
			}
		})
	}
}
