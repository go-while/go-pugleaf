package database

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

// AuthCacheEntry represents a cached authentication result
type AuthCacheEntry struct {
	UserID       int
	Username     string
	PasswordHash string // hash of the provided password for verification
	ExpiresAt    time.Time
}

// NNTPAuthCache provides in-memory caching of successful NNTP authentications
type NNTPAuthCache struct {
	entries map[string]*AuthCacheEntry // key: username
	mutex   sync.RWMutex
	ttl     time.Duration
	// Stats tracking
	hits      int64
	misses    int64
	evictions int64
}

// NewNNTPAuthCache creates a new authentication cache with specified TTL
func NewNNTPAuthCache(ttl time.Duration) *NNTPAuthCache {
	cache := &NNTPAuthCache{
		entries: make(map[string]*AuthCacheEntry),
		ttl:     ttl,
	}

	// Start cleanup goroutine
	go cache.cleanupExpired()

	return cache
}

// generatePasswordHash creates a deterministic hash of the provided password
// This is NOT for storage - it's just for cache key verification
func (c *NNTPAuthCache) generatePasswordHash(password string) string {
	hash := sha256.Sum256([]byte(password))
	return fmt.Sprintf("%x", hash)
}

// Set caches a successful authentication
func (c *NNTPAuthCache) Set(userID int, username, password string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	passwordHash := c.generatePasswordHash(password)

	c.entries[username] = &AuthCacheEntry{
		UserID:       userID,
		Username:     username,
		PasswordHash: passwordHash,
		ExpiresAt:    time.Now().Add(c.ttl),
	}
}

// Get checks if authentication is cached and still valid
func (c *NNTPAuthCache) Get(username, password string) (int, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	entry, exists := c.entries[username]
	if !exists {
		c.misses++
		return 0, false
	}

	// Check if expired
	if time.Now().After(entry.ExpiresAt) {
		c.misses++
		return 0, false
	}

	// Check if password matches
	passwordHash := c.generatePasswordHash(password)
	if entry.PasswordHash != passwordHash {
		c.misses++
		return 0, false
	}

	c.hits++
	return entry.UserID, true
}

// Remove removes a user from the cache (useful for password changes)
func (c *NNTPAuthCache) Remove(username string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	delete(c.entries, username)
}

// Clear removes all entries from the cache
func (c *NNTPAuthCache) Clear() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.entries = make(map[string]*AuthCacheEntry)
	// Reset stats
	c.hits = 0
	c.misses = 0
	c.evictions = 0
}

// Stats returns cache statistics
func (c *NNTPAuthCache) Stats() map[string]interface{} {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	totalEntries := len(c.entries)
	expiredCount := 0

	now := time.Now()
	for _, entry := range c.entries {
		if now.After(entry.ExpiresAt) {
			expiredCount++
		}
	}

	activeEntries := totalEntries - expiredCount

	// Calculate hit rate
	totalRequests := c.hits + c.misses
	var hitRate float64
	if totalRequests > 0 {
		hitRate = (float64(c.hits) / float64(totalRequests)) * 100
	}

	// Estimate memory usage (very rough approximation)
	// Each cache entry is roughly: username (avg 20 bytes) + password hash (64 bytes) + overhead (40 bytes) = ~124 bytes
	memoryBytes := float64(totalEntries * 124)
	memoryMB := memoryBytes / (1024 * 1024)

	return map[string]interface{}{
		"entries":   activeEntries,
		"hit_rate":  hitRate,
		"hits":      c.hits,
		"misses":    c.misses,
		"evictions": c.evictions,
		"memory_mb": memoryMB,
		"max_age":   c.ttl.String(),
		"status":    "Active",
	}
}

// cleanupExpired removes expired entries periodically
func (c *NNTPAuthCache) cleanupExpired() {
	ticker := time.NewTicker(5 * time.Minute) // cleanup every 5 minutes
	defer ticker.Stop()

	for range ticker.C {
		c.mutex.Lock()
		now := time.Now()
		evictedCount := 0

		for username, entry := range c.entries {
			if now.After(entry.ExpiresAt) {
				delete(c.entries, username)
				evictedCount++
			}
		}

		c.evictions += int64(evictedCount)
		c.mutex.Unlock()
	}
}
