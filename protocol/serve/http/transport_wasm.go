//go:build wasm && !tinygo

package http

import (
	"net/http"

	"github.com/chainreactors/rem/protocol/core"
)

func newHTTPTransport(_ core.ContextDialer) http.RoundTripper {
	return nil
}
