package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiskCacheSurvivesMemoryReset(t *testing.T) {
	dir := t.TempDir()
	SetPageCacheTTL(time.Minute)
	SetPageCacheDir(dir)
	t.Cleanup(func() {
		SetPageCacheDir("")
		SetPageCacheTTL(0)
	})

	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>persisted body</p></body></html>"))
	}))
	defer server.Close()

	if _, err := Read(context.Background(), server.URL); err != nil {
		t.Fatalf("first Read: %v", err)
	}

	// Simulate a process restart: wipe memory but keep the disk store.
	SetPageCacheTTL(time.Minute)
	SetPageCacheDir(dir)

	got, err := Read(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if !strings.Contains(got, "persisted body") {
		t.Fatalf("content = %q", got)
	}
	if hits.Load() != 1 {
		t.Errorf("origin hits = %d, want 1 (disk cache should serve the restart)", hits.Load())
	}

	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Errorf("cache dir empty (err=%v)", err)
	}
}

func TestDiskCachePrunesExpiredOnEnable(t *testing.T) {
	dir := t.TempDir()
	SetPageCacheTTL(time.Minute)
	SetPageCacheDir(dir)
	t.Cleanup(func() {
		SetPageCacheDir("")
		SetPageCacheTTL(0)
	})

	// Persist an already-expired entry directly.
	webPageCache.mu.Lock()
	webPageCache.persist("https://expired.test/x", &pageCacheEntry{
		content: "old",
		expires: time.Now().Add(-time.Hour),
	})
	webPageCache.mu.Unlock()

	SetPageCacheDir(dir) // re-enable triggers the prune

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expired entries not pruned: %d files remain", len(files))
	}
}
