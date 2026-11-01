package browserhttp

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// isGREASE reports a GREASE value (RFC 8701): Chrome always sends them and
// Go's crypto/tls never does, so they distinguish the two handshakes.
func isGREASE(v uint16) bool { return v&0x0f0f == 0x0a0a && v>>8 == v&0xff }

type helloRecorder struct {
	mu     sync.Mutex
	hellos []*tls.ClientHelloInfo
}

func (r *helloRecorder) record(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hellos = append(r.hellos, hello)
	return nil, nil
}

func newServer(t *testing.T, http2 bool) (*httptest.Server, *helloRecorder, *x509.CertPool) {
	t.Helper()
	recorder := &helloRecorder{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto+" "+r.Header.Get("Sec-Fetch-Mode"))
	}))
	server.EnableHTTP2 = http2
	server.TLS = &tls.Config{GetConfigForClient: recorder.record}
	server.StartTLS()
	t.Cleanup(server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	return server, recorder, pool
}

// dialer routes example.com (the httptest certificate's name) to the server.
func dialer(server *httptest.Server) DialFunc {
	target := server.Listener.Addr().String()
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, target)
	}
}

func exampleURL(server *httptest.Server) string {
	return strings.Replace(server.URL, "127.0.0.1", "example.com", 1)
}

func get(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	SetChromeHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestChromeHandshakeNegotiatesHTTP2(t *testing.T) {
	server, recorder, pool := newServer(t, true)
	client := &http.Client{Transport: New(dialer(server), 5*time.Second, WithRootCAs(pool))}
	for range 2 {
		if got := get(t, client, exampleURL(server)); got != "HTTP/2.0 navigate" {
			t.Fatalf("response = %q; want HTTP/2 with Chrome navigation headers", got)
		}
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.hellos) != 1 {
		t.Fatalf("handshakes = %d; want one reused HTTP/2 connection", len(recorder.hellos))
	}
	hello := recorder.hellos[0]
	if !slices.ContainsFunc(hello.CipherSuites, isGREASE) || !slices.ContainsFunc(hello.SupportedVersions, isGREASE) {
		t.Fatalf("ClientHello has no GREASE values; it is not Chrome-like: %v", hello.CipherSuites)
	}
	if !slices.Equal(hello.SupportedProtos, []string{"h2", "http/1.1"}) {
		t.Fatalf("ALPN = %v; want Chrome's [h2 http/1.1]", hello.SupportedProtos)
	}
	if hello.ServerName != "example.com" {
		t.Fatalf("SNI = %q", hello.ServerName)
	}
}

func TestChromeHandshakeFallsBackToHTTP1(t *testing.T) {
	server, recorder, pool := newServer(t, false)
	client := &http.Client{Transport: New(dialer(server), 5*time.Second, WithRootCAs(pool))}
	if got := get(t, client, exampleURL(server)); got != "HTTP/1.1 navigate" {
		t.Fatalf("response = %q; want HTTP/1.1", got)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, hello := range recorder.hellos {
		if !slices.ContainsFunc(hello.CipherSuites, isGREASE) {
			t.Fatal("HTTP/1.1 connection did not use the Chrome ClientHello")
		}
	}
}

func TestChromeHandshakeVerifiesCertificates(t *testing.T) {
	server, _, _ := newServer(t, true)
	client := &http.Client{Transport: New(dialer(server), 5*time.Second)} // system roots only
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, exampleURL(server), nil)
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("untrusted certificate accepted")
	}
}

func TestDialerPolicyStillApplies(t *testing.T) {
	server, _, pool := newServer(t, true)
	refuse := func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: io.ErrClosedPipe}
	}
	client := &http.Client{Transport: New(refuse, 5*time.Second, WithRootCAs(pool))}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, exampleURL(server), nil)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("transport bypassed the caller's dialer")
	}
}

func TestSetChromeHeadersKeepsCallerValues(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	SetChromeHeaders(req)
	if req.Header.Get("Sec-Fetch-Site") != "same-origin" || req.Header.Get("Sec-Ch-Ua-Mobile") != "?0" {
		t.Fatalf("headers = %v", req.Header)
	}
}

// Servers compress HTML; the HTTP/2 path must request and transparently
// decompress gzip like net/http does, or the reader would see binary data.
func TestHTTP2DecompressesGzip(t *testing.T) {
	recorder := &helloRecorder{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			_, _ = io.WriteString(w, "plain: no gzip requested")
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = io.WriteString(gz, r.Proto+" compressed page")
		_ = gz.Close()
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{GetConfigForClient: recorder.record}
	server.StartTLS()
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	client := &http.Client{Transport: New(dialer(server), 5*time.Second, WithRootCAs(pool))}
	if got := get(t, client, exampleURL(server)); got != "HTTP/2.0 compressed page" {
		t.Fatalf("body = %q; want the decompressed HTTP/2 page", got)
	}
}
