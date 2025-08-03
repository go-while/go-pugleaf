package nntp

import (
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
)

// Local430 is a simple in-memory cache for 430 responses
type Local430 struct {
	mu    sync.RWMutex
	cache map[*history.MessageIdItem]time.Time // Map of message IDs to their last 430 response time
}

// NewLocal430 creates a new Local430 cache
func (lc *Local430) CronLocal430() {
	for {
		time.Sleep(15 * time.Second) // HARDCODED Local430
		lc.Cleanup()
	}
}

// Check checks if a message ID is in the cache
func (lc *Local430) Check(msgIdItem *history.MessageIdItem) bool {
	lc.mu.RLock()
	defer lc.mu.RUnlock()

	_, exists := lc.cache[msgIdItem]
	return exists
}

// Add adds a message ID to the cache with the current time
func (lc *Local430) Add(msgIdItem *history.MessageIdItem) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	// Add to cache with current time
	lc.cache[msgIdItem] = time.Now()
	return true
}

// Cleanup removes expired entries from the cache
func (lc *Local430) Cleanup() {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	// Remove entries older than N
	cutoff := time.Now().Add(-1 * time.Minute) // TODO HARDCODED Local430
	for msgIdItem, lastTime := range lc.cache {
		if lastTime.Before(cutoff) {
			delete(lc.cache, msgIdItem)
		}
	}
}

/*
type CacheMessageIDNumtoGroup struct {
	mux   sync.RWMutex
	cache map[string]map[string]*ItemCMIDNG // messageID -> group -> article number
}

type ItemCMIDNG struct {
	Num     int64
	expires time.Time // Optional expiration time for cache entries
}

// NewCacheMessageIDNumtoGroup creates a new CacheMessageIDNumtoGroup
func NewCacheMessageIDNumtoGroup() *CacheMessageIDNumtoGroup {
	return &CacheMessageIDNumtoGroup{
		cache: make(map[string]map[string]*ItemCMIDNG),
	}
}

// Get retrieves the article number for a given message ID and group
func (c *CacheMessageIDNumtoGroup) Get(messageID, group string) (int64, bool) {
	c.mux.RLock()
	defer c.mux.RUnlock()

	if groupCache, ok := c.cache[messageID]; ok {
		if item, ok := groupCache[group]; ok {
			return item.Num, true
		}
	}
	return 0, false
}

// Set sets the article number for a given message ID and group
func (c *CacheMessageIDNumtoGroup) Set(messageID, group string, articleNum int64) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if _, ok := c.cache[messageID]; !ok {
		c.cache[messageID] = make(map[string]*ItemCMIDNG)
	}
	c.cache[messageID][group] = &ItemCMIDNG{
		Num:     articleNum,
		expires: time.Now().Add(5 * time.Minute), // TODO HARDCODED
	}
}

// Del removes the entry for a given message ID and group
func (c *CacheMessageIDNumtoGroup) Del(messageID, group string) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if groupCache, ok := c.cache[messageID]; ok {
		delete(groupCache, group)
		if len(groupCache) == 0 {
			delete(c.cache, messageID)
		}
	}
}

// Clear removes all entries for a given message ID
func (c *CacheMessageIDNumtoGroup) Clear(messageID string) {
	c.mux.Lock()
	defer c.mux.Unlock()
	delete(c.cache, messageID)
}

// Periodic cleanup to remove expired entries
func (c *CacheMessageIDNumtoGroup) CleanupCron() {
	go func() {
		for {
			time.Sleep(15 * time.Second) // Run cleanup every 15 seconds
			c.Cleanup()
		}
	}()
}

// Cleanup removes expired entries from the cache
func (c *CacheMessageIDNumtoGroup) Cleanup() {
	c.mux.Lock()
	defer c.mux.Unlock()

	// Remove entries older than 1 minute
	for messageID, groupCache := range c.cache {
		for group, item := range groupCache {
			if item.expires.Before(time.Now()) {
				delete(groupCache, group)
			}
		}
		if len(groupCache) == 0 {
			delete(c.cache, messageID)
		}
	}
}
*/
