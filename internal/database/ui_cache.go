package database

import (
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// uiCacheTTL is how long the base page data (site news, header sections, active AI models)
// is served from memory. Writers in this package invalidate the caches explicitly; the TTL
// only covers writers outside the package (for example direct INSERTs in internal/processor).
const uiCacheTTL = 30 * time.Second

// ttlCache holds one value that is reloaded after ttl or after invalidate.
// A failed load is never cached. A load that started before an invalidate
// does not store its result (generation check), so a writer's change is
// visible to every get that starts after the writer returned.
type ttlCache[T any] struct {
	mu  sync.RWMutex
	val T
	at  time.Time
	ok  bool
	gen uint64
}

// get returns the cached value if it is younger than ttl, otherwise it calls load
// outside the lock and stores the result unless the cache was invalidated meanwhile.
func (c *ttlCache[T]) get(ttl time.Duration, load func() (T, error)) (T, error) {
	c.mu.RLock()
	if c.ok && time.Since(c.at) < ttl {
		v := c.val
		c.mu.RUnlock()
		return v, nil
	}
	gen := c.gen
	c.mu.RUnlock()

	v, err := load()
	if err != nil {
		var zero T
		return zero, err
	}

	c.mu.Lock()
	if c.gen == gen {
		c.val = v
		c.at = time.Now()
		c.ok = true
	}
	c.mu.Unlock()
	return v, nil
}

// invalidate drops the cached value and makes in-flight loads discard their result.
func (c *ttlCache[T]) invalidate() {
	c.mu.Lock()
	var zero T
	c.val = zero
	c.ok = false
	c.gen++
	c.mu.Unlock()
}

// Package-level caches: OpenDatabase runs once per process, so there is one main DB.
var (
	uiCacheSiteNews       ttlCache[[]*models.SiteNews]
	uiCacheHeaderSections ttlCache[[]*models.Section]
	uiCacheActiveAIModels ttlCache[[]*models.AIModel]
)

// uiCacheCopy returns a new slice holding the same pointers (nil stays nil), so callers
// may append to or reorder the result without touching the cached slice.
func uiCacheCopy[T any](in []*T) []*T {
	if in == nil {
		return nil
	}
	out := make([]*T, len(in))
	copy(out, in)
	return out
}
