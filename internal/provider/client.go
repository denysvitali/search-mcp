package provider

import (
	"io"
	"net/http"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// defaultHTTPTimeout is the request timeout used by all providers unless
// overridden.
const defaultHTTPTimeout = 15 * time.Second

// maxResponseBodyBytes caps how many bytes any provider reads from a remote
// response body, protecting against unbounded/malicious payloads.
const maxResponseBodyBytes = 10 << 20 // 10 MiB

// limitedBody wraps a response body with an io.LimitReader capped at
// maxResponseBodyBytes so a provider cannot be forced to consume an unbounded
// amount of memory.
func limitedBody(r io.Reader) io.Reader {
	return io.LimitReader(r, maxResponseBodyBytes)
}

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
