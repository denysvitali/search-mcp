package cmd

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxHTTPBodyBytes = 1 << 20

// serveHTTP exposes the MCP server over the streamable HTTP transport until
// ctx is cancelled. Non-loopback listeners require an explicit bearer token so
// web_read cannot accidentally become an unauthenticated network pivot.
func serveHTTP(ctx context.Context, s *mcp.Server, addr, token string) error {
	if token == "" && httpAddressRequiresAuth(addr) {
		return fmt.Errorf("refusing unauthenticated non-loopback HTTP listener %q; set --http-token", addr)
	}

	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	if token != "" {
		handler = bearerTokenHandler(token, handler)
	}
	// Bound request bodies before the MCP decoder sees them. Search queries and
	// URLs are tiny; accepting megabytes here only creates an avoidable memory
	// and parsing DoS surface.
	inner := handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxHTTPBodyBytes)
		inner.ServeHTTP(w, r)
	})
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// bearerTokenHandler accepts a standard Authorization bearer token and a
// purpose-specific header for clients that cannot conveniently set it. Token
// comparisons use constant time to avoid making the listener an oracle.
func bearerTokenHandler(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := ""
		if auth := strings.Fields(r.Header.Get("Authorization")); len(auth) == 2 && strings.EqualFold(auth[0], "Bearer") {
			provided = auth[1]
		}
		if provided == "" {
			provided = r.Header.Get("X-Search-MCP-Token")
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="search-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// httpAddressRequiresAuth reports whether addr can listen beyond loopback.
// An empty host in ":8080" means all interfaces and therefore requires auth.
func httpAddressRequiresAuth(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}
