//go:build !tinygo && !wasm

package http

import (
	"net/http"

	"github.com/chainreactors/rem/protocol/core"
)

func newHTTPTransport(dial core.ContextDialer) http.RoundTripper {
	return &http.Transport{
		DialContext: dial.DialContext,
	}
}
