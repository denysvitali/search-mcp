package provider

import (
	"net/http"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// defaultHTTPTimeout is the request timeout used by all providers unless
// overridden.
const defaultHTTPTimeout = 15 * time.Second

// newHTTPClient builds an *http.Client shared per provider instance. A timeout
// of <= 0 falls back to defaultHTTPTimeout.
func newHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &http.Client{Timeout: timeout}
}

// applyExtraHeaders sets any caller-supplied request.ExtraHeaders on the
// outgoing request, overriding provider defaults of the same name.
func applyExtraHeaders(req *http.Request, r search.Request) {
	for k, v := range r.ExtraHeaders {
		req.Header.Set(k, v)
	}
}
