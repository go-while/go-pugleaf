package database

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// ArticleCacheEntry represents a cached article with metadata
type ArticleCacheEntry struct {
	Article     *models.Article `json:"article"`
	CachedAt    time.Time       `json:"cached_at"`
	AccessCount int64           `json:"access_count"`
	LastAccess  time.Time       `json:"last_access"`
	GroupName   string          `json:"group_name"`
}

// ArticleCache provides LRU caching for articles
type ArticleCache struct {
	maxSize   int
	ttl       time.Duration
	cache     map[string]*list.Element // key -> list element
	lruList   *list.List               // LRU ordering
	mutex     sync.RWMutex
	hits      int64
	misses    int64
	evictions int64
	totalSize int64 // Approximate memory usage
}

// NewArticleCache creates a new article cache
func NewArticleCache(maxSize int, ttl time.Duration) *ArticleCache {
	return &ArticleCache{
		maxSize: maxSize,
		ttl:     ttl,
		cache:   make(map[string]*list.Element),
		lruList: list.New(),
	}
}

// makeKey creates a cache key for group + article number
func (ac *ArticleCache) makeKey(groupName string, articleNum int64) string {
	return fmt.Sprintf("%s:%d", groupName, articleNum)
}

// Get retrieves an article from cache
func (ac *ArticleCache) Get(groupName string, articleNum int64) (*models.Article, bool) {
	ac.mutex.Lock()
	defer ac.mutex.Unlock()

	key := ac.makeKey(groupName, articleNum)

	if elem, exists := ac.cache[key]; exists {
		entry := elem.Value.(*ArticleCacheEntry)

		// Check TTL (DISABLED! SINCE ARTICLES DONT CHANGE: IF IT IS STILL CACHED WE TAKE IT!)
		/*
			if ac.ttl > 0 && time.Since(entry.CachedAt) > ac.ttl {
				ac.removeElement(elem)
				ac.misses++
				return nil, false
			}
		*/

		// Move to front (most recently used)
		ac.lruList.MoveToFront(elem)

		// Update access statistics
		entry.AccessCount++
		entry.LastAccess = time.Now()

		ac.hits++
		return entry.Article, true
	}

	ac.misses++
	return nil, false
}

// Put adds an article to cache
func (ac *ArticleCache) Put(groupName string, articleNum int64, article *models.Article) {
	ac.mutex.Lock()
	defer ac.mutex.Unlock()

	key := ac.makeKey(groupName, articleNum)

	// If already exists, update it
	if elem, exists := ac.cache[key]; exists {
		entry := elem.Value.(*ArticleCacheEntry)
		entry.Article = article
		entry.CachedAt = time.Now()
		entry.LastAccess = time.Now()
		ac.lruList.MoveToFront(elem)
		return
	}

	// Create new entry
	entry := &ArticleCacheEntry{
		Article:     article,
		CachedAt:    time.Now(),
		AccessCount: 0,
		LastAccess:  time.Now(),
		GroupName:   groupName,
	}

	// Add to front of LRU list
	elem := ac.lruList.PushFront(entry)
	ac.cache[key] = elem

	// Estimate memory usage (rough approximation)
	ac.totalSize += int64(len(article.BodyText) + len(article.Subject) + len(article.FromHeader) + 200)

	// Evict if necessary
	ac.evictIfNeeded()
}

// evictIfNeeded removes old entries if cache is too large
func (ac *ArticleCache) evictIfNeeded() {
	for ac.lruList.Len() > ac.maxSize {
		elem := ac.lruList.Back()
		if elem != nil {
			ac.removeElement(elem)
			ac.evictions++
		}
	}
}

// removeElement removes an element from cache
func (ac *ArticleCache) removeElement(elem *list.Element) {
	entry := elem.Value.(*ArticleCacheEntry)
	key := ac.makeKey(entry.GroupName, entry.Article.ArticleNum)

	// Estimate memory freed
	ac.totalSize -= int64(entry.Article.Bytes)
	if ac.totalSize < 0 {
		ac.totalSize = 0
	}

	delete(ac.cache, key)
	ac.lruList.Remove(elem)
}

// Remove explicitly removes an article from cache
func (ac *ArticleCache) Remove(groupName string, articleNum int64) {
	ac.mutex.Lock()
	defer ac.mutex.Unlock()

	key := ac.makeKey(groupName, articleNum)
	if elem, exists := ac.cache[key]; exists {
		ac.removeElement(elem)
	}
}

// Clear removes all cached articles
func (ac *ArticleCache) Clear() {
	ac.mutex.Lock()
	defer ac.mutex.Unlock()

	ac.cache = make(map[string]*list.Element)
	ac.lruList = list.New()
	ac.totalSize = 0
}

// ClearGroup removes all cached articles for a specific group
func (ac *ArticleCache) ClearGroup(groupName string) {
	ac.mutex.Lock()
	defer ac.mutex.Unlock()

	// Find all keys for this group
	keysToRemove := make([]string, 0)
	for key := range ac.cache {
		if len(key) > len(groupName) && key[:len(groupName)+1] == groupName+":" {
			keysToRemove = append(keysToRemove, key)
		}
	}

	// Remove them
	for _, key := range keysToRemove {
		if elem, exists := ac.cache[key]; exists {
			ac.removeElement(elem)
		}
	}
}

// Stats returns cache statistics
func (ac *ArticleCache) Stats() map[string]interface{} {
	ac.mutex.RLock()
	defer ac.mutex.RUnlock()

	totalRequests := ac.hits + ac.misses
	hitRate := 0.0
	if totalRequests > 0 {
		hitRate = float64(ac.hits) / float64(totalRequests) * 100
	}

	// Calculate utilization percentage
	utilizationPercent := 0.0
	if ac.maxSize > 0 {
		utilizationPercent = float64(ac.lruList.Len()) / float64(ac.maxSize) * 100
	}

	return map[string]interface{}{
		"size":                ac.lruList.Len(),
		"max_size":            ac.maxSize,
		"hits":                ac.hits,
		"misses":              ac.misses,
		"evictions":           ac.evictions,
		"hit_rate":            hitRate,
		"total_size":          ac.totalSize,
		"memory_mb":           float64(ac.totalSize) / 1024 / 1024,
		"max_age":             ac.ttl.String(),
		"utilization_percent": utilizationPercent,
	}
}

// Cleanup removes expired entries (call periodically)
func (ac *ArticleCache) Cleanup() {
	ac.mutex.Lock()
	defer ac.mutex.Unlock()

	if ac.ttl <= 0 {
		return
	}

	now := time.Now()
	var toRemove []*list.Element

	// Find expired entries
	for elem := ac.lruList.Back(); elem != nil; elem = elem.Prev() {
		entry := elem.Value.(*ArticleCacheEntry)
		if now.Sub(entry.CachedAt) > ac.ttl {
			toRemove = append(toRemove, elem)
		}
	}

	// Remove expired entries
	for _, elem := range toRemove {
		ac.removeElement(elem)
	}
}
