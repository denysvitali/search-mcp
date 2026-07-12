package reader

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// maxDiskCacheEntries caps how many page files the on-disk cache keeps;
// beyond it, the oldest files are pruned.
const maxDiskCacheEntries = 512

// diskCacheEntry is the JSON layout of one cached page on disk.
type diskCacheEntry struct {
	URL          string    `json:"url"`
	Content      string    `json:"content"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Expires      time.Time `json:"expires"`
}

// SetPageCacheDir enables persisting the web page cache under dir so it
// survives process restarts (an MCP server restarts with every agent
// session). An empty dir disables persistence. Expired files are pruned on
// enable. All disk I/O is best-effort: failures fall back to network fetches.
func SetPageCacheDir(dir string) {
	webPageCache.mu.Lock()
	defer webPageCache.mu.Unlock()
	webPageCache.dir = ""
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	webPageCache.dir = dir
	pruneDiskCache(dir)
}

// cachePath maps a URL to its cache file.
func cachePath(dir, url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
}

// loadFromDisk pulls a persisted entry (fresh or stale — stale entries still
// carry validators for conditional GETs) into the in-memory map. Caller must
// hold c.mu.
func (c *pageCache) loadFromDisk(url string) *pageCacheEntry {
	if c.dir == "" {
		return nil
	}
	data, err := os.ReadFile(cachePath(c.dir, url))
	if err != nil {
		return nil
	}
	var stored diskCacheEntry
	if err := json.Unmarshal(data, &stored); err != nil || stored.URL != url {
		return nil
	}
	entry := &pageCacheEntry{
		content:      stored.Content,
		etag:         stored.ETag,
		lastModified: stored.LastModified,
		expires:      stored.Expires,
	}
	c.entries[url] = entry
	return entry
}

// persist writes an entry to disk and enforces the file cap. Caller must hold
// c.mu.
func (c *pageCache) persist(url string, entry *pageCacheEntry) {
	if c.dir == "" {
		return
	}
	data, err := json.Marshal(diskCacheEntry{
		URL:          url,
		Content:      entry.content,
		ETag:         entry.etag,
		LastModified: entry.lastModified,
		Expires:      entry.expires,
	})
	if err != nil {
		return
	}
	target := cachePath(c.dir, url)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return
	}
	// Only pay for a full prune when the cheap name listing shows we are over
	// the cap; expired files are otherwise cleaned at startup.
	if files, err := os.ReadDir(c.dir); err == nil && len(files) > maxDiskCacheEntries {
		pruneDiskCache(c.dir)
	}
}

// pruneDiskCache removes expired cache files and, when still over the cap,
// the oldest files by modification time.
func pruneDiskCache(dir string) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	now := time.Now()
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, file.Name())
		// The path is built from the configured cache dir and a ReadDir entry,
		// not caller input.
		if data, err := os.ReadFile(path); err == nil { //nolint:gosec // see above
			var stored diskCacheEntry
			if json.Unmarshal(data, &stored) == nil && now.After(stored.Expires) {
				_ = os.Remove(path)
				continue
			}
		}
		info, err := file.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{path: path, modTime: info.ModTime()})
	}
	if len(candidates) <= maxDiskCacheEntries {
		return
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modTime.Before(candidates[j].modTime) })
	for _, c := range candidates[:len(candidates)-maxDiskCacheEntries] {
		_ = os.Remove(c.path)
	}
}
