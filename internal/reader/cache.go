package reader

import (
	"sync"
	"time"
)

// defaultPageCacheTTL is how long rendered pages stay fresh before they are
// revalidated with a conditional GET.
const defaultPageCacheTTL = 15 * time.Minute

// maxPageCacheEntries hard-caps the cache; when full and lazy expiry frees
// nothing, the whole map is reset (same policy as the search result cache).
const maxPageCacheEntries = 256

type pageCacheEntry struct {
	content      string
	etag         string
	lastModified string
	expires      time.Time
}

type pageCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]*pageCacheEntry
	// dir, when non-empty, persists entries to disk so the cache survives
	// process restarts.
	dir string
}

// webPageCache caches rendered Markdown per URL so repeated reads of the same
// page (chunked reads especially) fetch at most once per TTL.
var webPageCache = &pageCache{
	ttl:     defaultPageCacheTTL,
	entries: make(map[string]*pageCacheEntry),
}

// SetPageCacheTTL sets the freshness window of the web page cache. A TTL of 0
// (or negative) disables caching entirely.
func SetPageCacheTTL(ttl time.Duration) {
	webPageCache.mu.Lock()
	defer webPageCache.mu.Unlock()
	webPageCache.ttl = ttl
	webPageCache.entries = make(map[string]*pageCacheEntry)
}

// lookup returns the in-memory entry, falling back to the disk store (which
// may hold a stale entry whose validators still enable a conditional GET).
// Caller must hold c.mu.
func (c *pageCache) lookup(url string) (*pageCacheEntry, bool) {
	if entry, ok := c.entries[url]; ok {
		return entry, true
	}
	if entry := c.loadFromDisk(url); entry != nil {
		return entry, true
	}
	return nil, false
}

// getFresh returns cached content that is still within its TTL.
func (c *pageCache) getFresh(url string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		return "", false
	}
	entry, ok := c.lookup(url)
	if !ok || time.Now().After(entry.expires) {
		return "", false
	}
	return entry.content, true
}

// validators returns the stored ETag/Last-Modified for a stale entry so the
// caller can issue a conditional GET.
func (c *pageCache) validators(url string) (etag, lastModified string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		return "", ""
	}
	if entry, ok := c.lookup(url); ok {
		return entry.etag, entry.lastModified
	}
	return "", ""
}

// refresh extends a stale entry's freshness after a 304 Not Modified and
// returns its content.
func (c *pageCache) refresh(url string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		return "", false
	}
	entry, ok := c.lookup(url)
	if !ok {
		return "", false
	}
	entry.expires = time.Now().Add(c.ttl)
	c.persist(url, entry)
	return entry.content, true
}

// store caches rendered content with its validators.
func (c *pageCache) store(url, content, etag, lastModified string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		return
	}
	if len(c.entries) >= maxPageCacheEntries {
		now := time.Now()
		for key, entry := range c.entries {
			if now.After(entry.expires) {
				delete(c.entries, key)
			}
		}
		if len(c.entries) >= maxPageCacheEntries {
			c.entries = make(map[string]*pageCacheEntry)
		}
	}
	entry := &pageCacheEntry{
		content:      content,
		etag:         etag,
		lastModified: lastModified,
		expires:      time.Now().Add(c.ttl),
	}
	c.entries[url] = entry
	c.persist(url, entry)
}

// expireAll marks every entry stale; used by tests to exercise revalidation.
func (c *pageCache) expireAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		entry.expires = time.Time{}
	}
}
