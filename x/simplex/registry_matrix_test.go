package simplex

import "testing"

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
