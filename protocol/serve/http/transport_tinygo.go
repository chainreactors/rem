//go:build tinygo

package http

import (
	"net/http"

	"github.com/chainreactors/rem/protocol/core"
)

// TinyGo's http.Transport doesn't support DialContext.
// This is fine because net.Dial goes through the netdev driver anyway.
func newHTTPTransport(_ core.ContextDialer) http.RoundTripper {
	return &http.Transport{}
}
