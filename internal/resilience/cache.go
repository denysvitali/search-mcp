package resilience

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// defaultCacheMaxEntries bounds the in-memory cache so a pathological caller
// cannot grow it without limit. When exceeded, expired entries are swept and,
// if still over budget, the whole map is reset.
const defaultCacheMaxEntries = 1024

// CacheOptions configures CachingProvider.
type CacheOptions struct {
	// TTL is how long a successful response stays valid. A zero or negative TTL
	// disables caching (pass-through).
	TTL time.Duration

	// MaxEntries bounds the cache size. Values <= 0 use a default.
	MaxEntries int

	// now, if set, replaces time.Now for testing.
	now func() time.Time
}

type cacheEntry struct {
	resp      search.Response
	expiresAt time.Time
}

// CachingProvider is an in-memory TTL cache around an inner provider. Only
// successful responses are cached; errors are always propagated.
type CachingProvider struct {
	inner search.Provider
	opts  CacheOptions

	mu      sync.Mutex
	entries map[string]cacheEntry
}

// NewCachingProvider wraps inner with a TTL cache. A non-positive TTL yields a
// transparent pass-through.
func NewCachingProvider(inner search.Provider, opts CacheOptions) *CachingProvider {
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = defaultCacheMaxEntries
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	return &CachingProvider{
		inner:   inner,
		opts:    opts,
		entries: make(map[string]cacheEntry),
	}
}

// Name delegates to the inner provider.
func (p *CachingProvider) Name() string { return p.inner.Name() }

// Search returns a cached response when fresh, otherwise calls the inner
// provider and caches a successful result.
func (p *CachingProvider) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if p.opts.TTL <= 0 {
		// Caching disabled: pass through.
		return p.inner.Search(ctx, req)
	}

	key := cacheKey(req)

	if resp, ok := p.get(key); ok {
		recordCacheEvent(ctx, p.Name(), "hit")
		return resp, nil
	}
	recordCacheEvent(ctx, p.Name(), "miss")

	resp, err := p.inner.Search(ctx, req)
	if err != nil {
		// Never cache errors.
		return search.Response{}, err
	}
	p.set(key, resp)
	return resp, nil
}

// get returns a non-expired cached response, applying lazy expiry.
func (p *CachingProvider) get(key string) (search.Response, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.entries[key]
	if !ok {
		return search.Response{}, false
	}
	if !p.opts.now().Before(entry.expiresAt) {
		delete(p.entries, key)
		return search.Response{}, false
	}
	return entry.resp, true
}

// set stores resp under key, enforcing the size guard.
func (p *CachingProvider) set(key string, resp search.Response) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.entries) >= p.opts.MaxEntries {
		p.evictExpiredLocked()
		if len(p.entries) >= p.opts.MaxEntries {
			// Still over budget after sweeping expired entries: reset to keep
			// memory bounded. Simpler and adequate for a SERP cache.
			p.entries = make(map[string]cacheEntry)
		}
	}
	p.entries[key] = cacheEntry{
		resp:      resp,
		expiresAt: p.opts.now().Add(p.opts.TTL),
	}
}

// evictExpiredLocked removes expired entries. Caller must hold p.mu.
func (p *CachingProvider) evictExpiredLocked() {
	now := p.opts.now()
	for k, e := range p.entries {
		if !now.Before(e.expiresAt) {
			delete(p.entries, k)
		}
	}
}

// cacheKey builds a stable key from the request fields that affect results.
// ExtraHeaders is intentionally excluded as it carries transport concerns
// (e.g. user agent) rather than query semantics.
func cacheKey(req search.Request) string {
	return fmt.Sprintf("q=%s\x00n=%d\x00c=%s\x00l=%s\x00s=%s\x00f=%s\x00p=%s",
		req.Query,
		req.Count,
		req.Country,
		req.Language,
		req.SafeSearch,
		req.Freshness,
		req.Provider,
	)
}
