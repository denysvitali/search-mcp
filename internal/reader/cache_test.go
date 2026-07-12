package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPageCacheServesFreshHits(t *testing.T) {
	SetPageCacheTTL(time.Minute)
	t.Cleanup(func() { SetPageCacheTTL(0) })

	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>cached body</p></body></html>"))
	}))
	defer server.Close()

	for range 3 {
		got, err := Read(context.Background(), server.URL)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !strings.Contains(got, "cached body") {
			t.Fatalf("content = %q", got)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("origin hits = %d, want 1 (cache should serve repeats)", hits.Load())
	}
}

func TestPageCacheRevalidatesWithETag(t *testing.T) {
	SetPageCacheTTL(time.Minute)
	t.Cleanup(func() { SetPageCacheTTL(0) })

	var hits, notModified atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>etag body</p></body></html>"))
	}))
	defer server.Close()

	if _, err := Read(context.Background(), server.URL); err != nil {
		t.Fatalf("first Read: %v", err)
	}
	webPageCache.expireAll()

	got, err := Read(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("revalidated Read: %v", err)
	}
	if !strings.Contains(got, "etag body") {
		t.Fatalf("content = %q", got)
	}
	if notModified.Load() != 1 {
		t.Errorf("304 responses = %d, want 1", notModified.Load())
	}
	if hits.Load() != 2 {
		t.Errorf("origin hits = %d, want 2", hits.Load())
	}

	// The refreshed entry must now be fresh again — no third request.
	if _, err := Read(context.Background(), server.URL); err != nil {
		t.Fatalf("cached Read: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("origin hits after refresh = %d, want 2", hits.Load())
	}
}

func TestPageCacheDisabledByDefaultInTests(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>x</p></body></html>"))
	}))
	defer server.Close()

	for range 2 {
		if _, err := Read(context.Background(), server.URL); err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if hits.Load() != 2 {
		t.Errorf("hits = %d, want 2 with TTL 0", hits.Load())
	}
}
