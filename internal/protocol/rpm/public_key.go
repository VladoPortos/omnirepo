// Package rpm — /public-key.asc handler with in-memory armored-key cache.
//
// The public-key endpoint resolves the repo, fetches SigningKeyMeta from the
// in-process cache or falls through to SigningKeysRepo.Lookup, and returns
// the armored public key with Content-Type: application/pgp-keys. RWMutex
// gives lock-free concurrent reads on a cache hit.
package rpm

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// PublicKeyCache caches armored public-key bytes keyed on repo id. There is
// no TTL or invalidation: signing keys do not rotate in v1, so a cached
// entry stays valid for the process lifetime.
type PublicKeyCache struct {
	mu          sync.RWMutex
	m           map[int64][]byte
	signingKeys *metadata.SigningKeysRepo
}

// NewPublicKeyCache constructs a cache bound to signingKeys. Nil
// signingKeys produces a cache that always misses and 404s.
func NewPublicKeyCache(signingKeys *metadata.SigningKeysRepo) *PublicKeyCache {
	return &PublicKeyCache{
		m:           make(map[int64][]byte),
		signingKeys: signingKeys,
	}
}

// Lookup returns the armored public-key bytes for repoID. Cache hit path
// uses RLock only; on miss, falls through to SigningKeysRepo.Lookup with a
// write-lock fill. Returns ErrNotFound when no signing_keys row exists.
func (c *PublicKeyCache) Lookup(ctx context.Context, repoID int64) ([]byte, error) {
	c.mu.RLock()
	if b, ok := c.m[repoID]; ok {
		c.mu.RUnlock()
		return b, nil
	}
	c.mu.RUnlock()

	if c.signingKeys == nil {
		return nil, metadata.ErrNotFound
	}
	meta, err := c.signingKeys.Lookup(ctx, repoID)
	if err != nil {
		return nil, err
	}
	if meta == nil || meta.PublicArmored == "" {
		return nil, metadata.ErrNotFound
	}
	bytes := []byte(meta.PublicArmored)

	c.mu.Lock()
	c.m[repoID] = bytes
	c.mu.Unlock()
	return bytes, nil
}

// ServePublicKey writes the armored public key for repoID to w with
// Content-Type: application/pgp-keys. 404 when no signing key exists.
// Lock-free on cache hit.
func (c *PublicKeyCache) ServePublicKey(w http.ResponseWriter, r *http.Request, repoID int64) {
	body, err := c.Lookup(r.Context(), repoID)
	if err != nil {
		if errors.Is(err, metadata.ErrNotFound) {
			http.Error(w, "no signing key", http.StatusNotFound)
			return
		}
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/pgp-keys")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
