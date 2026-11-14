// SSRF-guarded HTTP client shared by every reader.

package reader

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/denysvitali/search-mcp/internal/browserhttp"
)

func newHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: defaultHTTPTimeout, Control: guardDialAddress}
	// A Chrome-like TLS handshake: Go's own ClientHello under a Chrome
	// User-Agent is a bot signature that web application firewalls block.
	// Every socket still goes through the guarded dialer, and ambient
	// HTTP(S)_PROXY is never honored: a proxy could otherwise fetch a private
	// redirect target while the client only connects to the proxy's address.
	transport := browserhttp.New(dialer.DialContext, 10*time.Second)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: defaultHTTPTimeout, Transport: transport, Jar: jar}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxHTTPRedirectCount {
			return fmt.Errorf("too many redirects")
		}
		if req.URL == nil || !slices.Contains(supportedSchemes, strings.ToLower(req.URL.Scheme)) {
			return fmt.Errorf("redirected to unsupported URL scheme")
		}
		// validateURL checks the initial destination, but redirects are new
		// caller-controlled destinations too. Reapply the domain policy here so
		// an allowed page cannot bounce the reader onto a blocked host.
		if err := checkDomainPolicy(req.URL); err != nil {
			return fmt.Errorf("redirect destination rejected: %w", err)
		}
		return nil
	}
	return client
}

// guardDialAddress runs after DNS resolution, just before the socket connects,
// so it sees the concrete IP the request will hit — including addresses reached
// via redirects or DNS rebinding. It refuses any non-public destination so the
// reader (which fetches caller-supplied URLs) cannot be turned into an SSRF
// vector against loopback, private, or link-local services such as the cloud
// metadata endpoint (169.254.169.254).
func guardDialAddress(_, address string, _ syscall.RawConn) error {
	if allowPrivateHosts {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("could not parse dial ip %q", host)
	}
	if isDisallowedIP(ip) {
		return fmt.Errorf("refusing to connect to non-public address %s", ip)
	}
	return nil
}

// isDisallowedIP reports whether ip is a loopback, private, link-local,
// unspecified, or multicast address that the reader must never connect to.
func isDisallowedIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast()
}

// readErrorBody reads up to maxErrorBodyBytes from a non-OK response body and
// returns it trimmed, for embedding in error messages. A read failure yields
// an empty string rather than masking the original status error.
func readErrorBody(r io.Reader) string {
	body, _ := io.ReadAll(io.LimitReader(r, maxErrorBodyBytes))
	return strings.TrimSpace(string(body))
}

func newRequest(ctx context.Context, urlStr, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept-Language", defaultAcceptLang)
	browserhttp.SetChromeHeaders(req)
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else {
		req.Header.Set("Accept", defaultAccept)
	}
	return req, nil
}

// limitedBody wraps a response body with an io.LimitReader capped at
// maxResponseBodyBytes so that downstream readers/parsers cannot be forced to
// consume an unbounded amount of memory.
func limitedBody(r io.Reader) io.Reader {
	return io.LimitReader(r, maxResponseBodyBytes)
}
