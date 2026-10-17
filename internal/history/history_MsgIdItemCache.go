package history

import (
	"log"
	"sync"
	"time"
)

var (
	MIICmutex  sync.RWMutex    // Global mutex for the cache
	MsgIdCache *MsgIdItemCache // Global instance of the message ID item cache

	UpperLimitMsgIdCacheSize = 1024 * 1024
	// This is the maximum size for the cache, used to prevent overflow

	DefaultMsgIdCacheSize = 4 * 1024 // N buckets
	MaxLoadFactor         = 0.75     // Resize when load factor exceeds this

	// TTL Configuration Constants
	TmpCacheTTL       = 15 * time.Second // TTL for temporary processing (CaseWrite items) // @AI!!! DO NOT CHANGE THIS!!!!
	CachedEntryTTL    = 15 * time.Second // TTL for cache entries (CaseDupes items after flushing) // @AI!!! DO NOT CHANGE THIS!!!!
	ErrorCaseTTL      = 15 * time.Second // TTL for error cases (shorter to retry sooner)  // @AI!!! DO NOT CHANGE THIS!!!!
	MaxCachedEntryTTL = 15 * time.Second // Maximum TTL for any cache entry // @AI!!! DO NOT CHANGE THIS!!!!

	// StuckEntryMaxAge is the extra time an in-progress item (CaseLock/CaseWrite)
	// may stay cached after its CachedEntryExpires before it is evicted as stuck.
	StuckEntryMaxAge = 10 * time.Minute
)

// msgIdCacheShards is the fixed number of shards (must be a power of two)
const msgIdCacheShards = 256

// MsgIdItemCache is a short-term in-memory table message-id -> *MessageIdItem.
// While an entry is cached there is exactly one *MessageIdItem per message-id.
type MsgIdItemCache struct {
	shards [msgIdCacheShards]msgIdCacheShard
}

type msgIdCacheShard struct {
	mu sync.Mutex
	m  map[string]*MessageIdItem
}

// NewMsgIdItemCache returns the process-wide singleton cache, creating it on first use
func NewMsgIdItemCache() *MsgIdItemCache {
	MIICmutex.Lock()
	defer MIICmutex.Unlock()
	if MsgIdCache != nil {
		return MsgIdCache // Return existing cache if already created
	}
	MsgIdCache = newMsgIdItemCache()
	return MsgIdCache // Return the newly created cache
}

// newMsgIdItemCache creates an independent cache (used by the singleton and by tests)
func newMsgIdItemCache() *MsgIdItemCache {
	c := &MsgIdItemCache{}
	for i := range c.shards {
		c.shards[i].m = make(map[string]*MessageIdItem)
	}
	return c
}

// shard returns the shard for a message-id (FNV-1a 32bit, no allocation)
func (c *MsgIdItemCache) shard(messageId string) *msgIdCacheShard {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(messageId); i++ {
		h ^= uint32(messageId[i])
		h *= prime32
	}
	return &c.shards[h&(msgIdCacheShards-1)]
}

// GetORCreate returns the cached item for messageId or inserts a new one
func (c *MsgIdItemCache) GetORCreate(messageId string) *MessageIdItem {
	s := c.shard(messageId)
	s.mu.Lock()
	item, exists := s.m[messageId]
	if !exists {
		item = &MessageIdItem{
			MessageId:          messageId,
			CachedEntryExpires: time.Now().Add(CachedEntryTTL), // Use configurable TTL for cache entries
		}
		s.m[messageId] = item
	}
	s.mu.Unlock()
	return item
}

// Stats returns cache statistics for monitoring and debugging.
// buckets = number of shards, items = total items, maxChainLength = item count of the largest shard
func (c *MsgIdItemCache) Stats() (buckets, items, maxChainLength int) {
	buckets, _, items, maxChainLength, _ = c.DetailedStats()
	return buckets, items, maxChainLength
}

// DetailedStats returns comprehensive cache statistics for monitoring and debugging.
// totalBuckets = number of shards, occupiedBuckets = non-empty shards,
// maxChainLength = item count of the largest shard, loadFactor = items / shards
func (c *MsgIdItemCache) DetailedStats() (totalBuckets, occupiedBuckets, items, maxChainLength int, loadFactor float64) {
	totalBuckets = msgIdCacheShards
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		n := len(s.m)
		s.mu.Unlock()
		if n > 0 {
			occupiedBuckets++
		}
		items += n
		if n > maxChainLength {
			maxChainLength = n
		}
	}
	loadFactor = float64(items) / float64(totalBuckets)
	return totalBuckets, occupiedBuckets, items, maxChainLength, loadFactor
}

// Delete removes messageId from the cache. The item itself is not modified.
func (c *MsgIdItemCache) Delete(messageId string) bool {
	s := c.shard(messageId)
	s.mu.Lock()
	_, exists := s.m[messageId]
	if exists {
		delete(s.m, messageId)
	}
	s.mu.Unlock()
	return exists
}

// Clear removes all items from the cache. The items themselves are not modified.
func (c *MsgIdItemCache) Clear() {
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		s.m = make(map[string]*MessageIdItem)
		s.mu.Unlock()
	}
}

type msgIdCacheEntry struct {
	key   string
	item  *MessageIdItem
	stuck bool
}

// CleanExpiredEntries evicts expired entries and returns the number of evicted entries.
// Terminal/unset states are evicted once CachedEntryExpires has passed;
// in-progress states (CaseLock, CaseWrite) only after CachedEntryExpires + StuckEntryMaxAge.
// Eviction only removes the map entry: evicted items are never modified,
// other goroutines may still hold them.
func (c *MsgIdItemCache) CleanExpiredEntries() int {
	start := time.Now()
	evicted := 0
	stuck := 0
	var entries, expired []msgIdCacheEntry

	for i := range c.shards {
		s := &c.shards[i]

		// snapshot (key, ptr) pairs under the shard lock
		entries = entries[:0]
		s.mu.Lock()
		for k, item := range s.m {
			entries = append(entries, msgIdCacheEntry{key: k, item: item})
		}
		s.mu.Unlock()
		if len(entries) == 0 {
			continue
		}

		// check item states without holding the shard lock
		now := time.Now()
		expired = expired[:0]
		for _, e := range entries {
			e.item.Mux.RLock()
			expires := e.item.CachedEntryExpires
			response := e.item.Response
			e.item.Mux.RUnlock()

			if expires.IsZero() {
				e.item.Mux.Lock()
				if e.item.CachedEntryExpires.IsZero() {
					e.item.CachedEntryExpires = now.Add(CachedEntryTTL)
				}
				e.item.Mux.Unlock()
				continue
			}

			switch response {
			case CaseLock, CaseWrite:
				// in progress: evict only when stuck for too long
				if now.After(expires.Add(StuckEntryMaxAge)) {
					e.stuck = true
					expired = append(expired, e)
				}
			default:
				// CaseDupes, CaseError, CasePass, CaseRetry, 0 (unset) and unknown states
				if now.After(expires) {
					expired = append(expired, e)
				}
			}
		}
		if len(expired) == 0 {
			continue
		}

		// delete only entries that still map to the checked item
		s.mu.Lock()
		for _, e := range expired {
			if s.m[e.key] == e.item {
				delete(s.m, e.key)
				evicted++
				if e.stuck {
					stuck++
				}
			}
		}
		s.mu.Unlock()
	}

	if evicted > 0 {
		_, _, items, _, _ := c.DetailedStats()
		log.Printf("[CACHE-CLEANUP] evicted %d entries (stuck lock/write: %d) items=%d took %v", evicted, stuck, items, time.Since(start))
	}
	return evicted
}

// StartCleanupRoutine starts a background goroutine to clean expired entries
func (c *MsgIdItemCache) StartCleanupRoutine() {
	go func() {
		for {
			time.Sleep(TmpCacheTTL)
			c.CleanExpiredEntries()
		}
	}()
}
