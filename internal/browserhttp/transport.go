// Package browserhttp provides an HTTP transport whose TLS handshake looks like
// Chrome's.
//
// Go's crypto/tls ClientHello (JA3/JA4) is a well-known bot signature, and
// pairing it with a Chrome User-Agent is a mismatch that web application
// firewalls flag. This transport performs the handshake with uTLS using a
// Chrome ClientHello, then speaks HTTP/2 or HTTP/1.1 depending on the protocol
// the server negotiates, exactly as a browser would.
package browserhttp

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// DialFunc dials a plain network connection. Callers pass their guarded dialer
// so address policies (such as SSRF protection) still apply to every socket.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Transport is an http.RoundTripper with a Chrome-like TLS fingerprint.
type Transport struct {
	dial             DialFunc
	handshakeTimeout time.Duration
	rootCAs          *x509.CertPool

	h1 *http.Transport
	h2 *http2.Transport

	mu      sync.Mutex
	h2Conns map[string]*http2.ClientConn
	h1Hosts map[string]bool
}

// Option configures a Transport.
type Option func(*Transport)

// WithRootCAs overrides the system trust store (used by tests).
func WithRootCAs(pool *x509.CertPool) Option {
	return func(t *Transport) { t.rootCAs = pool }
}

// New returns a Transport that dials with dial and bounds each TLS handshake.
func New(dial DialFunc, handshakeTimeout time.Duration, opts ...Option) *Transport {
	t := &Transport{dial: dial, handshakeTimeout: handshakeTimeout,
		h2Conns: map[string]*http2.ClientConn{}, h1Hosts: map[string]bool{}}
	for _, opt := range opts {
		opt(t)
	}
	t.h1 = &http.Transport{
		Proxy:       nil,
		DialContext: dial,
		DialTLSContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			return t.dialTLS(ctx, address)
		},
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	t.h2 = &http2.Transport{ReadIdleTimeout: 30 * time.Second, PingTimeout: 15 * time.Second}
	return t
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return t.h1.RoundTrip(req)
	}
	address := canonicalAddress(req.URL.Hostname(), req.URL.Port())
	t.mu.Lock()
	cc := t.h2Conns[address]
	if cc != nil && !cc.CanTakeNewRequest() {
		delete(t.h2Conns, address)
		cc = nil
	}
	h1 := t.h1Hosts[address]
	t.mu.Unlock()
	if cc != nil {
		return cc.RoundTrip(req)
	}
	if h1 {
		return t.h1.RoundTrip(req)
	}
	conn, err := t.dialTLS(req.Context(), address)
	if err != nil {
		return nil, err
	}
	if conn.ConnectionState().NegotiatedProtocol != http2.NextProtoTLS {
		// The server chose HTTP/1.1: remember it and use the pooled HTTP/1.1
		// transport, which dials with the same Chrome ClientHello.
		_ = conn.Close()
		t.mu.Lock()
		t.h1Hosts[address] = true
		t.mu.Unlock()
		return t.h1.RoundTrip(req)
	}
	cc, err = t.h2.NewClientConn(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("http2 client connection: %w", err)
	}
	t.mu.Lock()
	if existing := t.h2Conns[address]; existing != nil && existing.CanTakeNewRequest() {
		// A concurrent request already established a connection; keep it and
		// let this one serve only the current request.
		t.mu.Unlock()
		resp, err := cc.RoundTrip(req)
		cc.SetDoNotReuse()
		return resp, err
	}
	t.h2Conns[address] = cc
	t.mu.Unlock()
	return cc.RoundTrip(req)
}

// CloseIdleConnections closes idle HTTP/1.1 and HTTP/2 connections.
func (t *Transport) CloseIdleConnections() {
	t.h1.CloseIdleConnections()
	t.mu.Lock()
	defer t.mu.Unlock()
	for address, cc := range t.h2Conns {
		if cc.State().StreamsActive == 0 {
			_ = cc.Close()
			delete(t.h2Conns, address)
		}
	}
}

// dialTLS connects with the caller's dialer and performs a Chrome-like
// handshake that offers h2 and http/1.1 via ALPN. Certificates are verified
// against the system roots (or the configured pool) for the request host.
func (t *Transport) dialTLS(ctx context.Context, address string) (*utls.UConn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", address, err)
	}
	raw, err := t.dial(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	conn := utls.UClient(raw, &utls.Config{ServerName: host, RootCAs: t.rootCAs}, utls.HelloChrome_Auto)
	handshakeCtx := ctx
	if t.handshakeTimeout > 0 {
		var cancel context.CancelFunc
		handshakeCtx, cancel = context.WithTimeout(ctx, t.handshakeTimeout)
		defer cancel()
	}
	if err := conn.HandshakeContext(handshakeCtx); err != nil {
		_ = raw.Close()
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("tls handshake timeout: %w", err)
		}
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	return conn, nil
}

func canonicalAddress(host, port string) string {
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(host, port)
}

// SetChromeHeaders adds the request headers Chrome sends on a top-level
// navigation that the caller has not set already. User-Agent, Accept and
// Accept-Language stay under the caller's control.
func SetChromeHeaders(req *http.Request) {
	for key, value := range map[string]string{
		"Sec-Ch-Ua":                 `"Chromium";v="147", "Not.A/Brand";v="24", "Google Chrome";v="147"`,
		"Sec-Ch-Ua-Mobile":          "?0",
		"Sec-Ch-Ua-Platform":        `"Linux"`,
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
	} {
		if req.Header.Get(key) == "" {
			req.Header.Set(key, value)
		}
	}
}
