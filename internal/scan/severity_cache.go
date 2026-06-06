// Package scan — severity gate in-memory cache.
//
// The block_on_severity gate runs on every Docker manifest GET and (when
// scanned) every RAW file GET. To avoid hot-path DB queries, decisions are
// cached in-memory keyed by (repoID, artifactKind, artifactID) with a
// configurable TTL (default 30s).
//
// The scan handler invalidates the cache entry in the same code path that
// marks the scan done so a stale "allow" entry never persists past a fresh
// high-severity scan.
package scan

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// CacheEntry is the cached severity decision for one artifact.
type CacheEntry struct {
	Blocked  bool   // true → gate returns ErrBlockedByScan
	Severity string // highest severity present (when Blocked) — "critical"|"high"|...
	CVECount int    // number of vulnerabilities at or above the threshold
	ScanID   int64  // scan id that produced this decision
}

type cacheRecord struct {
	entry     CacheEntry
	expiresAt time.Time
}

// SeverityCache is a TTL-bounded in-memory map of artifact decisions.
// Safe for concurrent use.
type SeverityCache struct {
	mu  sync.RWMutex
	m   map[string]cacheRecord
	ttl time.Duration
}

// NewSeverityCache returns an empty cache with the given TTL. ttl <= 0
// falls back to the 30-second default.
func NewSeverityCache(ttl time.Duration) *SeverityCache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &SeverityCache{
		m:   make(map[string]cacheRecord),
		ttl: ttl,
	}
}

// Get returns the cached entry and (true) if present and unexpired.
// Expired entries are NOT eagerly evicted on read — Set will overwrite
// them, and Invalidate is the explicit eviction path.
func (c *SeverityCache) Get(repoID int64, kind, artifactID string) (CacheEntry, bool) {
	c.mu.RLock()
	rec, ok := c.m[cacheKey(repoID, kind, artifactID)]
	c.mu.RUnlock()
	if !ok {
		return CacheEntry{}, false
	}
	if time.Now().After(rec.expiresAt) {
		return CacheEntry{}, false
	}
	return rec.entry, true
}

// Set stores entry for (repoID, kind, artifactID) with the cache's TTL.
func (c *SeverityCache) Set(repoID int64, kind, artifactID string, entry CacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[cacheKey(repoID, kind, artifactID)] = cacheRecord{
		entry:     entry,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// Invalidate evicts the entry for (repoID, kind, artifactID). Called by
// the scan handler immediately after a scan's writer tx commits so a fresh
// decision is computed on the next gate query.
func (c *SeverityCache) Invalidate(repoID int64, kind, artifactID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, cacheKey(repoID, kind, artifactID))
}


// cacheKey builds the map key. Uses a delimiter that cannot appear in a
// numeric repo id to avoid collisions ("\x1f" is the ASCII unit separator).
func cacheKey(repoID int64, kind, artifactID string) string {
	var b strings.Builder
	b.Grow(32 + len(kind) + len(artifactID))
	b.WriteString(strconv.FormatInt(repoID, 10))
	b.WriteByte('\x1f')
	b.WriteString(kind)
	b.WriteByte('\x1f')
	b.WriteString(artifactID)
	return b.String()
}
