package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestCacheIsolatesResultsAndHeaders(t *testing.T) {
	original := search.Response{Results: []search.Result{{Title: "Original", URL: "https://example.test"}}, Degraded: []search.ProviderFailure{{Provider: "original"}}}
	stub := &stubProvider{resp: original}
	cache := NewCachingProvider(stub, CacheOptions{TTL: time.Minute})
	req := search.Request{Query: "q"}
	first, err := cache.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	first.Results[0].Title = "changed"
	first.Degraded[0].Provider = "changed"
	original.Results[0].Title = "inner mutation"
	second, err := cache.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Results[0].Title != "Original" || second.Degraded[0].Provider != "original" {
		t.Fatalf("cache mutated: %+v", second)
	}
	req.ExtraHeaders = map[string]string{"Accept-Language": "fr"}
	_, _ = cache.Search(context.Background(), req)
	if stub.callCount() != 2 {
		t.Fatal("headers shared a cached response")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = cache.Search(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cache hit: %v", err)
	}
}

func TestCacheKeyCannotCollideOnFieldDelimiters(t *testing.T) {
	a := search.Request{Query: "x", Country: "a\x00l=b", Language: "c"}
	b := search.Request{Query: "x", Country: "a", Language: "b\x00l=c"}
	if cacheKey(a) == cacheKey(b) {
		t.Fatal("cache key collision")
	}
	a.ExtraHeaders = map[string]string{"a": "1", "b": "2"}
	b = a
	b.ExtraHeaders = map[string]string{"b": "2", "a": "1"}
	if cacheKey(a) != cacheKey(b) {
		t.Fatal("map order changed cache key")
	}
}

func TestBreakerCancellationPreservesHealth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stub := &stubProvider{fn: func(_ int, ctx context.Context, _ search.Request) (search.Response, error) {
		cancel()
		return search.Response{}, ctx.Err()
	}}
	b := NewCircuitBreaker(stub, BreakerOptions{Threshold: 1})
	if _, err := b.Search(ctx, search.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if status := b.Status(); status.State != "closed" || status.ConsecutiveFailures != 0 || status.LastError != nil {
		t.Fatalf("cancellation poisoned status: %+v", status)
	}
	_, _ = b.Search(ctx, search.Request{})
	if stub.callCount() != 1 {
		t.Fatal("already canceled request reached provider")
	}
}

func TestBreakerOldCallCannotCloseNewOpenState(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	stub := &stubProvider{fn: func(call int, _ context.Context, _ search.Request) (search.Response, error) {
		if call == 1 {
			close(started)
			<-release
			return search.Response{}, nil
		}
		return search.Response{}, errors.New("upstream unavailable")
	}}
	b := NewCircuitBreaker(stub, BreakerOptions{Threshold: 1})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = b.Search(context.Background(), search.Request{}) }()
	<-started
	_, _ = b.Search(context.Background(), search.Request{})
	close(release)
	<-done
	if b.Status().State != "open" {
		t.Fatal("old success closed the new open state")
	}
}

func TestBreakerCanceledTrialReleasesSlot(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	stub := &stubProvider{fn: func(call int, ctx context.Context, _ search.Request) (search.Response, error) {
		if call == 1 {
			return search.Response{}, errors.New("blocked")
		}
		if call == 2 {
			cancel()
			return search.Response{}, ctx.Err()
		}
		return search.Response{}, nil
	}}
	b := NewCircuitBreaker(stub, BreakerOptions{Threshold: 1, Cooldown: time.Second, now: clk.now})
	_, _ = b.Search(context.Background(), search.Request{})
	clk.advance(time.Second)
	_, _ = b.Search(ctx, search.Request{})
	if b.Status().State != "half-open" {
		t.Fatal("canceled trial restarted cooldown")
	}
	if _, err := b.Search(context.Background(), search.Request{}); err != nil {
		t.Fatalf("trial slot stuck: %v", err)
	}
	if b.Status().State != "closed" {
		t.Fatal("fresh trial failed to close breaker")
	}
}
