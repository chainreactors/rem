//go:build !wasm || tinygo

package http

import (
	"net/http"

	"github.com/chainreactors/rem/protocol/core"
)

func newHTTPTransport(dial core.ContextDialer) http.RoundTripper {
	t := &http.Transport{}
	// TinyGo's http.Transport doesn't support DialContext field.
	// Standard Go needs it for routing through the tunnel dialer.
	if dial != nil {
		t.DialContext = dial.DialContext
	}
	return t
}
