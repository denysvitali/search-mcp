package common

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
func LimitedBody(r io.Reader) io.Reader {
	return io.LimitReader(r, maxResponseBodyBytes)
}

// newHTTPClient builds the *http.Client shared per provider instance, with
// defaultHTTPTimeout applied.
func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultHTTPTimeout}
}

// applyExtraHeaders sets any caller-supplied request.ExtraHeaders on the
// outgoing request, overriding provider defaults of the same name.
func ApplyExtraHeaders(req *http.Request, r search.Request) {
	for k, v := range r.ExtraHeaders {
		req.Header.Set(k, v)
	}
}

// URLWithQuery joins query parameters onto a configured endpoint without
// producing a second '?' when the endpoint already carries fixed options.
// Values supplied by the request replace same-named endpoint values so caller
// filters cannot be silently ignored.
func URLWithQuery(endpoint string, values url.Values) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Host == "" || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") {
		return "", fmt.Errorf("endpoint must be an absolute http(s) URL: %q", endpoint)
	}
	query := u.Query()
	for key, value := range values {
		query[key] = append([]string(nil), value...)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}
