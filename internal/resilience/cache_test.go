package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestCache_HitWithinTTL(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	stub := &stubProvider{resp: search.Response{Provider: "stub", Query: "go"}}
	c := NewCachingProvider(stub, CacheOptions{TTL: time.Minute, now: clk.now})

	req := search.Request{Query: "go"}
	if _, err := c.Search(context.Background(), req); err != nil {
		t.Fatalf("first: %v", err)
	}
	clk.advance(30 * time.Second) // still within TTL
	if _, err := c.Search(context.Background(), req); err != nil {
		t.Fatalf("second: %v", err)
	}
	if stub.callCount() != 1 {
		t.Fatalf("want 1 inner call (cache hit), got %d", stub.callCount())
	}
}

func TestCache_RefetchesAfterExpiry(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	stub := &stubProvider{resp: search.Response{Provider: "stub"}}
	c := NewCachingProvider(stub, CacheOptions{TTL: time.Minute, now: clk.now})

	req := search.Request{Query: "go"}
	_, _ = c.Search(context.Background(), req)
	clk.advance(2 * time.Minute) // past TTL
	_, _ = c.Search(context.Background(), req)

	if stub.callCount() != 2 {
		t.Fatalf("want 2 inner calls after expiry, got %d", stub.callCount())
	}
}

func TestCache_DoesNotCacheErrors(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	boom := errors.New("boom")
	stub := &stubProvider{errs: []error{boom, boom}}
	c := NewCachingProvider(stub, CacheOptions{TTL: time.Minute, now: clk.now})

	req := search.Request{Query: "go"}
	if _, err := c.Search(context.Background(), req); err == nil {
		t.Fatal("want error")
	}
	if _, err := c.Search(context.Background(), req); err == nil {
		t.Fatal("want error")
	}
	if stub.callCount() != 2 {
		t.Fatalf("errors must not be cached: want 2 calls, got %d", stub.callCount())
	}
}

func TestCache_DisabledWhenTTLZero(t *testing.T) {
	stub := &stubProvider{resp: search.Response{Provider: "stub"}}
	c := NewCachingProvider(stub, CacheOptions{TTL: 0})

	req := search.Request{Query: "go"}
	for i := 0; i < 3; i++ {
		_, _ = c.Search(context.Background(), req)
	}
	if stub.callCount() != 3 {
		t.Fatalf("TTL=0 disables caching: want 3 calls, got %d", stub.callCount())
	}
}

func TestCache_DistinctKeysMiss(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	stub := &stubProvider{resp: search.Response{Provider: "stub"}}
	c := NewCachingProvider(stub, CacheOptions{TTL: time.Minute, now: clk.now})

	_, _ = c.Search(context.Background(), search.Request{Query: "go"})
	_, _ = c.Search(context.Background(), search.Request{Query: "rust"})
	_, _ = c.Search(context.Background(), search.Request{Query: "go", Count: 5})

	if stub.callCount() != 3 {
		t.Fatalf("distinct keys should all miss: want 3 calls, got %d", stub.callCount())
	}
}

func TestCache_SizeGuard(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	stub := &stubProvider{
		fn: func(call int, _ context.Context, req search.Request) (search.Response, error) {
			return search.Response{Query: req.Query}, nil
		},
	}
	c := NewCachingProvider(stub, CacheOptions{TTL: time.Hour, MaxEntries: 4, now: clk.now})

	// Insert more than MaxEntries distinct keys; should stay bounded.
	for i := 0; i < 20; i++ {
		_, _ = c.Search(context.Background(), search.Request{Query: string(rune('a' + i))})
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > 4 {
		t.Fatalf("cache exceeded MaxEntries: %d", n)
	}
}

func TestCache_KeyStableAcrossRelevantFields(t *testing.T) {
	a := search.Request{Query: "q", Count: 3, Country: "us", Language: "en", SafeSearch: "off", Freshness: "d", Provider: "ddg"}
	b := a
	if cacheKey(a) != cacheKey(b) {
		t.Fatal("identical requests must share a key")
	}
	// ExtraHeaders must not affect the key.
	a.ExtraHeaders = map[string]string{"User-Agent": "x"}
	if cacheKey(a) != cacheKey(b) {
		t.Fatal("ExtraHeaders must not affect the cache key")
	}
}

func TestWrap_Compose(t *testing.T) {
	// Smoke test: Wrap composes without panicking and serves a cache hit.
	clk := time.Now
	_ = clk
	stub := &stubProvider{resp: search.Response{Provider: "stub"}}
	p := Wrap(stub, Config{
		RetryMaxAttempts: 2,
		RetryBaseDelay:   time.Millisecond,
		BreakerThreshold: 3,
		BreakerCooldown:  time.Second,
		CacheTTL:         time.Minute,
	})
	req := search.Request{Query: "go"}
	if _, err := p.Search(context.Background(), req); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := p.Search(context.Background(), req); err != nil {
		t.Fatalf("second: %v", err)
	}
	if stub.callCount() != 1 {
		t.Fatalf("composed cache hit expected: want 1 call, got %d", stub.callCount())
	}
	if p.Name() != "stub" {
		t.Fatalf("Name should delegate through the stack, got %q", p.Name())
	}
}
