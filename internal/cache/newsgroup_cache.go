package cache

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// Newsgroup represents a newsgroup for caching (avoiding import cycle)
type Newsgroup struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Active       bool      `json:"active"`
	Description  string    `json:"description"`
	LastArticle  int64     `json:"last_article"`
	MessageCount int64     `json:"message_count"`
	ExpiryDays   int       `json:"expiry_days"`
	MaxArticles  int       `json:"max_articles"`
	MaxArtSize   int       `json:"max_art_size"`
	HighWater    int       `json:"high_water"`
	LowWater     int       `json:"low_water"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CachedGroupsResult holds cached newsgroup data with pagination info
type CachedGroupsResult struct {
	Groups     []*Newsgroup
	TotalCount int
	CreatedAt  time.Time
	LastUsed   time.Time
	Size       int64 // Estimated memory size
}

// NewsgroupCache provides caching for newsgroup listings with pagination
type NewsgroupCache struct {
	cache       map[string]*CachedGroupsResult
	mutex       sync.RWMutex
	maxEntries  int           // Maximum number of cached page results
	maxAge      time.Duration // Maximum age of entries
	cleanupTick time.Duration // How often to run cleanup
	stopCleanup chan bool
	cachedSize  int64        // Size of the cache in bytes
	countermux  sync.RWMutex // Mutex for cachedSize updates
	hits        int64        // Cache hit counter
	misses      int64        // Cache miss counter
}

// NewNewsgroupCache creates a new newsgroup cache with specified limits
func NewNewsgroupCache(maxEntries int, maxAge time.Duration) *NewsgroupCache {
	nc := &NewsgroupCache{
		cache:       make(map[string]*CachedGroupsResult),
		maxEntries:  maxEntries,
		maxAge:      maxAge,
		cleanupTick: time.Minute * 5, // Clean up every 5 minutes
		stopCleanup: make(chan bool),
	}

	// Start cleanup goroutine
	go nc.cleanup()

	return nc
}

// generateKey creates a cache key based on page and pageSize
func (nc *NewsgroupCache) generateKey(page, pageSize int) string {
	return fmt.Sprintf("groups_p%d_s%d", page, pageSize)
}

// Get retrieves cached newsgroup data for a specific page
func (nc *NewsgroupCache) Get(page, pageSize int) ([]*Newsgroup, int, bool) {
	key := nc.generateKey(page, pageSize)

	nc.mutex.RLock()
	entry, exists := nc.cache[key]
	nc.mutex.RUnlock()

	if !exists {
		// Increment miss counter
		nc.countermux.Lock()
		nc.misses++
		nc.countermux.Unlock()
		return nil, 0, false
	}

	// Check if entry is expired
	if time.Since(entry.CreatedAt) > nc.maxAge {
		nc.Remove(page, pageSize)
		// Increment miss counter for expired entry
		nc.countermux.Lock()
		nc.misses++
		nc.countermux.Unlock()
		return nil, 0, false
	}

	// Increment hit counter
	nc.countermux.Lock()
	nc.hits++
	nc.countermux.Unlock()

	// Update last used time
	nc.mutex.Lock()
	entry.LastUsed = time.Now()
	nc.mutex.Unlock()

	log.Printf("NewsgroupCache: Cache hit for page %d, size %d (%d groups)", page, pageSize, len(entry.Groups))
	return entry.Groups, entry.TotalCount, true
}

// Set stores newsgroup data in cache
func (nc *NewsgroupCache) Set(page, pageSize int, groups []*Newsgroup, totalCount int) {
	key := nc.generateKey(page, pageSize)

	// Calculate estimated size
	size := nc.estimateSize(groups)

	entry := &CachedGroupsResult{
		Groups:     groups,
		TotalCount: totalCount,
		CreatedAt:  time.Now(),
		LastUsed:   time.Now(),
		Size:       size,
	}

	nc.mutex.Lock()
	defer nc.mutex.Unlock()

	// Remove old entry if it exists
	if oldEntry, exists := nc.cache[key]; exists {
		nc.updateCachedSize(-oldEntry.Size)
	}

	nc.cache[key] = entry
	nc.updateCachedSize(size)

	// Check if we need to evict entries
	nc.evictIfNeeded()

	log.Printf("NewsgroupCache: Cached page %d, size %d (%d groups, %d total)", page, pageSize, len(groups), totalCount)
}

// Remove removes a specific cache entry
func (nc *NewsgroupCache) Remove(page, pageSize int) {
	key := nc.generateKey(page, pageSize)

	nc.mutex.Lock()
	defer nc.mutex.Unlock()

	if entry, exists := nc.cache[key]; exists {
		nc.updateCachedSize(-entry.Size)
		delete(nc.cache, key)
		log.Printf("NewsgroupCache: Removed cache entry for page %d, size %d", page, pageSize)
	}
}

// Clear removes all cache entries
func (nc *NewsgroupCache) Clear() {
	nc.mutex.Lock()
	defer nc.mutex.Unlock()

	count := len(nc.cache)
	nc.cache = make(map[string]*CachedGroupsResult)
	nc.updateCachedSize(-nc.getCachedSize())

	log.Printf("NewsgroupCache: Cleared all cache entries (%d entries)", count)
}

// GetStats returns cache statistics
func (nc *NewsgroupCache) GetStats() map[string]interface{} {
	nc.mutex.RLock()
	entryCount := len(nc.cache)
	nc.mutex.RUnlock()

	nc.countermux.RLock()
	hits := nc.hits
	misses := nc.misses
	nc.countermux.RUnlock()

	totalRequests := hits + misses
	hitRate := 0.0
	if totalRequests > 0 {
		hitRate = float64(hits) / float64(totalRequests) * 100
	}

	// Calculate utilization percentage
	utilizationPercent := 0.0
	if nc.maxEntries > 0 {
		utilizationPercent = float64(entryCount) / float64(nc.maxEntries) * 100
	}

	return map[string]interface{}{
		"entries":             entryCount,
		"max_entries":         nc.maxEntries,
		"size_bytes":          nc.GetCachedSize(),
		"size_human":          nc.GetCachedSizeHuman(),
		"max_age":             nc.maxAge.String(),
		"hits":                hits,
		"misses":              misses,
		"hit_rate":            hitRate,
		"utilization_percent": utilizationPercent,
	}
}

// GetCachedSize returns the current cache size in bytes
func (nc *NewsgroupCache) GetCachedSize() int64 {
	nc.countermux.RLock()
	defer nc.countermux.RUnlock()
	return nc.cachedSize
}

// GetCachedSizeHuman returns human-readable cache size
func (nc *NewsgroupCache) GetCachedSizeHuman() string {
	size := nc.GetCachedSize()
	if size < 1024 {
		return fmt.Sprintf("%d bytes", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%.2f KB", float64(size)/1024.0)
	}
	return fmt.Sprintf("%.2f MB", float64(size)/(1024.0*1024.0))
}

// getCachedSize returns cached size without lock (must be called with lock held)
func (nc *NewsgroupCache) getCachedSize() int64 {
	nc.countermux.RLock()
	defer nc.countermux.RUnlock()
	return nc.cachedSize
}

// updateCachedSize updates the cached size counter (thread-safe)
func (nc *NewsgroupCache) updateCachedSize(delta int64) {
	nc.countermux.Lock()
	nc.cachedSize += delta
	if nc.cachedSize < 0 {
		nc.cachedSize = 0
	}
	nc.countermux.Unlock()
}

// estimateSize calculates rough memory usage of newsgroup data
func (nc *NewsgroupCache) estimateSize(groups []*Newsgroup) int64 {
	if len(groups) == 0 {
		return 100 // Base overhead
	}

	// Rough estimation: struct overhead + string lengths
	baseSize := int64(len(groups) * 200) // Base struct size per group

	for _, group := range groups {
		if group != nil {
			baseSize += int64(len(group.Name))
			baseSize += int64(len(group.Description))
			baseSize += int64(len(group.Status))
		}
	}

	return baseSize
}

// evictIfNeeded removes oldest entries if cache is full (must be called with lock held)
func (nc *NewsgroupCache) evictIfNeeded() {
	if len(nc.cache) <= nc.maxEntries {
		return
	}

	// Find oldest entry by LastUsed time
	var oldestKey string
	var oldestTime time.Time

	for key, entry := range nc.cache {
		if oldestKey == "" || entry.LastUsed.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.LastUsed
		}
	}

	if oldestKey != "" {
		if entry := nc.cache[oldestKey]; entry != nil {
			nc.updateCachedSize(-entry.Size)
		}
		delete(nc.cache, oldestKey)
		log.Printf("NewsgroupCache: Evicted oldest entry: %s", oldestKey)
	}
}

// cleanup runs periodically to remove expired entries
func (nc *NewsgroupCache) cleanup() {
	ticker := time.NewTicker(nc.cleanupTick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			nc.cleanupExpired()
		case <-nc.stopCleanup:
			log.Println("NewsgroupCache: Stopping cleanup goroutine")
			return
		}
	}
}

// cleanupExpired removes expired cache entries
func (nc *NewsgroupCache) cleanupExpired() {
	nc.mutex.Lock()
	defer nc.mutex.Unlock()

	now := time.Now()
	expiredKeys := make([]string, 0)

	for key, entry := range nc.cache {
		if now.Sub(entry.CreatedAt) > nc.maxAge {
			expiredKeys = append(expiredKeys, key)
		}
	}

	for _, key := range expiredKeys {
		if entry := nc.cache[key]; entry != nil {
			nc.updateCachedSize(-entry.Size)
		}
		delete(nc.cache, key)
	}

	if len(expiredKeys) > 0 {
		log.Printf("NewsgroupCache: Cleaned up %d expired entries", len(expiredKeys))
	}
}

// Stop gracefully shuts down the cache
func (nc *NewsgroupCache) Stop() {
	close(nc.stopCleanup)
}
