package cache

import (
	"fmt"
	"html/template"
	"log"
	"sync"
	"time"
)

// SanitizedArticle holds all sanitized fields for an article
type SanitizedArticle struct {
	mux         sync.RWMutex // Mutex for thread-safe access
	Subject     template.HTML
	Author      template.HTML
	Body        template.HTML
	Date        template.HTML
	References  template.HTML
	MessageID   template.HTML
	Path        template.HTML
	HeadersJSON template.HTML
	DateSince   template.HTML
	// Add other fields as needed
	CreatedAt time.Time
	LastUsed  time.Time
	Size      int64
}

// SanitizedCache provides caching for complete sanitized articles
type SanitizedCache struct {
	cache       map[string]*SanitizedArticle
	mutex       sync.RWMutex
	maxEntries  int           // Maximum number of articles (not fields!)
	maxAge      time.Duration // Maximum age of entries
	cleanupTick time.Duration // How often to run cleanup
	stopCleanup chan bool
	cachedSize  int64        // Size of the cache in bytes (optional, can be used for memory limits)
	countermux  sync.RWMutex // Mutex for cachedSize updates
	hits        int64        // Cache hit counter
	misses      int64        // Cache miss counter
}

func (sc *SanitizedCache) GetCachedSize() int64 {
	// This function returns the current size of the sanitized cache
	sc.countermux.RLock()
	defer sc.countermux.RUnlock()
	return sc.cachedSize
}

func (sc *SanitizedCache) GetCachedSizeHuman() string {
	size := sc.GetCachedSize()
	if size < 1024 {
		return fmt.Sprintf("%d bytes", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%.2f KB", float64(size)/1024.0)
	}
	return fmt.Sprintf("%.2f MB", float64(size)/(1024.0*1024.0))
}

// NewSanitizedCache creates a new cache with specified limits
func NewSanitizedCache(maxEntries int, maxAge time.Duration) *SanitizedCache {
	sc := &SanitizedCache{
		cache:       make(map[string]*SanitizedArticle),
		maxEntries:  maxEntries,
		maxAge:      maxAge,
		cleanupTick: 1 * time.Minute, // Cleanup every minute
		stopCleanup: make(chan bool),
	}

	// Start background cleanup goroutine
	go sc.cleanupLoop()

	return sc
}

// GetField retrieves a specific sanitized field from a cached article
func (sc *SanitizedCache) GetField(messageID string, field string) (template.HTML, bool) {
	key := sc.hashMessageID(messageID)

	sc.mutex.RLock()
	article, exists := sc.cache[key]
	sc.mutex.RUnlock()

	if !exists {
		// Increment miss counter
		sc.countermux.Lock()
		sc.misses++
		sc.countermux.Unlock()
		//log.Printf("SanitizedCache: GetField MISS for messageID='%s', field='%s', key='%s'", messageID, field, key)
		return "", false
	}

	// Increment hit counter
	sc.countermux.Lock()
	sc.hits++
	sc.countermux.Unlock()

	// Update last used time
	article.mux.Lock()
	article.LastUsed = time.Now()
	article.mux.Unlock()

	// Return the requested field
	switch field {
	case "subject":
		if len(article.Subject) == 0 {
			//log.Printf("SanitizedCache: GetField EMPTY-HIT->MISS for messageID='%s', field='%s', treating as miss", messageID, field)
			return "", false
		}
		return article.Subject, true
	case "author", "fromheader":
		if len(article.Author) == 0 {
			//log.Printf("SanitizedCache: GetField EMPTY-HIT->MISS for messageID='%s', field='%s', treating as miss", messageID, field)
			return "", false
		}
		return article.Author, true
	case "body", "bodytext":
		if len(article.Body) == 0 {
			//log.Printf("SanitizedCache: GetField EMPTY-HIT->MISS for messageID='%s', field='%s', treating as miss", messageID, field)
			return "", false
		}
		return article.Body, true
	case "date", "date_string":
		if len(article.Date) == 0 {
			//log.Printf("SanitizedCache: GetField EMPTY-HIT->MISS for messageID='%s', field='%s', treating as miss", messageID, field)
			return "", false
		}
		return article.Date, true
	case "references":
		if len(article.References) == 0 {
			//log.Printf("SanitizedCache: GetField EMPTY-HIT->MISS for messageID='%s', field='%s', key='%s', treating as miss", messageID, field, key)
			return "", false
		}
		//log.Printf("SanitizedCache: GetField HIT for messageID='%s', field='%s', value_len=%d", messageID, field, len(article.References))
		return article.References, true
	case "messageid":
		if len(article.MessageID) == 0 {
			return "", false
		}
		return article.MessageID, true
	case "path":
		if len(article.Path) == 0 {
			return "", false
		}
		return article.Path, true
	case "headers_json":
		if len(article.HeadersJSON) == 0 {
			return "", false
		}
		return article.HeadersJSON, true
	case "date_since":
		if len(article.DateSince) == 0 {
			return "", false
		}
		return article.DateSince, true
	default:
		return "", false
	}
}

// GetArticle retrieves a complete cached sanitized article
func (sc *SanitizedCache) GetArticle(messageID string) (*SanitizedArticle, bool) {
	key := sc.hashMessageID(messageID)
	sc.mutex.RLock()
	article, exists := sc.cache[key]
	sc.mutex.RUnlock()

	if !exists {
		return nil, false
	}

	// Update last used time
	article.mux.Lock()
	article.LastUsed = time.Now()
	article.mux.Unlock()

	log.Printf("SanitizedCache: GetArticle: found cached article for messageID '%s'", messageID)
	return article, true
}

// SetField stores a single sanitized field, creating or updating the article entry
func (sc *SanitizedCache) SetField(messageID string, field string, value template.HTML) {
	key := sc.hashMessageID(messageID)
	now := time.Now()

	sc.mutex.Lock()
	// Get existing article or create new one
	article, exists := sc.cache[key]
	if !exists {
		log.Printf("SanitizedCache: SetField '%s': creating new entry for messageID '%s'", field, messageID)

		article = &SanitizedArticle{
			CreatedAt: now,
			LastUsed:  now,
		}
		sc.cache[key] = article
	}
	sc.mutex.Unlock()

	article.mux.Lock()
	// Calculate old size for this field to avoid double-counting
	var oldSize int64
	switch field {
	case "subject":
		oldSize = int64(len(article.Subject))
		article.Subject = value
	case "author", "fromheader":
		oldSize = int64(len(article.Author))
		article.Author = value
	case "body", "bodytext":
		oldSize = int64(len(article.Body))
		article.Body = value
	case "date", "date_string":
		oldSize = int64(len(article.Date))
		article.Date = value
	case "references":
		oldSize = int64(len(article.References))
		log.Printf("SanitizedCache: SetField 'references' for messageID='%s', key='%s', old_len=%d, new_len=%d", messageID, key, len(article.References), len(value))
		article.References = value
	case "messageid":
		oldSize = int64(len(article.MessageID))
		article.MessageID = value
	case "path":
		oldSize = int64(len(article.Path))
		article.Path = value
	case "headers_json":
		oldSize = int64(len(article.HeadersJSON))
		article.HeadersJSON = value
	case "date_since":
		oldSize = int64(len(article.DateSince))
		article.DateSince = value
	}
	article.LastUsed = now
	newSize := int64(len(value))
	sizeDiff := newSize - oldSize
	article.Size += sizeDiff
	article.mux.Unlock()

	// Update global cache size
	go func(sc *SanitizedCache, sizeDiff int64) {
		sc.countermux.Lock()
		sc.cachedSize += sizeDiff
		sc.countermux.Unlock()
	}(sc, sizeDiff)
}

// SetArticle stores a complete sanitized article in one operation
func (sc *SanitizedCache) SetArticle(messageID string, sanitizedFields map[string]template.HTML) {
	key := sc.hashMessageID(messageID)
	now := time.Now()

	sc.mutex.Lock()
	// Get existing article or create new one
	article, exists := sc.cache[key]
	if !exists {
		article = &SanitizedArticle{
			CreatedAt: now,
			LastUsed:  now,
		}
		sc.cache[key] = article
	}
	sc.mutex.Unlock()

	article.mux.Lock()
	oldSize := article.Size
	var newSize int64

	// Set all fields from the map
	for field, value := range sanitizedFields {
		switch field {
		case "subject":
			article.Subject = value
		case "author", "fromheader":
			article.Author = value
		case "body", "bodytext":
			article.Body = value
		case "date", "date_string":
			article.Date = value
		case "references":
			article.References = value
		case "messageid":
			article.MessageID = value
		case "path":
			article.Path = value
		case "headers_json":
			article.HeadersJSON = value
		case "date_since":
			article.DateSince = value
		}
		newSize += int64(len(value))
	}

	article.LastUsed = now
	sizeDiff := newSize - oldSize
	article.Size = newSize
	article.mux.Unlock()

	// Update global cache size
	sc.countermux.Lock()
	sc.cachedSize += sizeDiff
	sc.countermux.Unlock()
}

// BatchSetArticles efficiently stores multiple complete sanitized articles
func (sc *SanitizedCache) BatchSetArticles(articles map[string]map[string]template.HTML) {
	if len(articles) == 0 {
		return
	}

	now := time.Now()
	var totalSizeDiff int64

	// Process all articles in one lock cycle
	sc.mutex.Lock()
	for messageID, fields := range articles {
		key := sc.hashMessageID(messageID)

		// Get existing article or create new one
		article, exists := sc.cache[key]
		if !exists {
			article = &SanitizedArticle{
				CreatedAt: now,
				LastUsed:  now,
			}
			sc.cache[key] = article
		}

		// Set fields for this article
		article.mux.Lock()
		oldSize := article.Size
		var newSize int64

		for field, value := range fields {
			switch field {
			case "subject":
				article.Subject = value
			case "author", "fromheader", "from", "from_header":
				article.Author = value
			case "body", "bodytext", "body_text":
				article.Body = value
			case "date", "datestring", "date_string":
				article.Date = value
			case "references":
				article.References = value
			case "messageid":
				article.MessageID = value
			case "path":
				article.Path = value
			case "headers_json":
				article.HeadersJSON = value
			case "date_since":
				article.DateSince = value
			}
			newSize += int64(len(value))
		}

		article.LastUsed = now
		sizeDiff := newSize - oldSize
		article.Size = newSize
		totalSizeDiff += sizeDiff
		article.mux.Unlock()
	}
	sc.mutex.Unlock()

	// Update global cache size
	sc.countermux.Lock()
	sc.cachedSize += totalSizeDiff
	sc.countermux.Unlock()
}

// Clear removes all entries from the cache
func (sc *SanitizedCache) Clear() {
	sc.mutex.Lock()
	sc.cache = make(map[string]*SanitizedArticle)
	sc.mutex.Unlock()
}

// Stats returns cache statistics
func (sc *SanitizedCache) Stats() map[string]interface{} {
	sc.mutex.RLock()
	entries := len(sc.cache)
	sc.mutex.RUnlock()

	sc.countermux.RLock()
	hits := sc.hits
	misses := sc.misses
	sc.countermux.RUnlock()

	totalRequests := hits + misses
	hitRate := 0.0
	if totalRequests > 0 {
		hitRate = float64(hits) / float64(totalRequests) * 100
	}

	return map[string]interface{}{
		"entries":     entries,
		"max_entries": sc.maxEntries,
		"max_age":     sc.maxAge.String(),
		"hits":        hits,
		"misses":      misses,
		"hit_rate":    hitRate,
	}
}

// Stop shuts down the cache and cleanup goroutine
func (sc *SanitizedCache) Stop() {
	close(sc.stopCleanup)
}

// hashMessageID creates a consistent hash from a message ID
func (sc *SanitizedCache) hashMessageID(messageID string) string {
	// For now, just use the message ID directly
	// Could hash it if needed for consistent key length
	/*
		if len(messageID) > 32 {
			md5hash := md5.Sum([]byte(messageID))
			return hex.EncodeToString(md5hash[:])
		}
	*/
	return messageID
}

// evictOldest removes the oldest entry
func (sc *SanitizedCache) evictOldest() {
	go func() {
		var oldestKey string
		var oldestTime time.Time

		sc.mutex.RLock()
		for key, article := range sc.cache {
			article.mux.RLock()
			lastused := article.LastUsed
			article.mux.RUnlock()
			if oldestKey == "" || lastused.Before(oldestTime) {
				oldestKey = key
				oldestTime = lastused
			}
		}
		sc.mutex.RUnlock()

		sc.mutex.Lock()
		if oldestKey != "" {
			delete(sc.cache, oldestKey)
		}
		sc.mutex.Unlock()
	}()
}

// cleanupLoop runs periodic cleanup of expired entries
func (sc *SanitizedCache) cleanupLoop() {
	ticker := time.NewTicker(sc.cleanupTick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sc.cleanup()
		case <-sc.stopCleanup:
			return
		}
	}
}

// cleanup removes expired entries
func (sc *SanitizedCache) cleanup() {

	sc.mutex.RLock()
	if sc.maxAge <= 0 || len(sc.cache) < sc.maxEntries {
		sc.mutex.RUnlock()

		return // no cleanup needed
	}
	sc.mutex.RUnlock()

	now := time.Now()
	keysToDelete := make([]string, 0)

	var delsize int64

	sc.mutex.RLock()
	for key, article := range sc.cache {
		article.mux.RLock()
		if now.Sub(article.CreatedAt) > sc.maxAge {
			keysToDelete = append(keysToDelete, key)
			delsize += article.Size // Track size for cleanup
		}
		article.mux.RUnlock()
	}
	sc.mutex.RUnlock()

	if len(keysToDelete) > 0 {
		sc.mutex.Lock()
		for _, key := range keysToDelete {
			delete(sc.cache, key)
		}
		sc.mutex.Unlock()
		// Could add logging here if needed
		// log.Printf("Cache cleanup: removed %d expired entries", len(keysToDelete))
	}
	go func(sc *SanitizedCache, delsize int64) {
		sc.countermux.Lock()
		sc.cachedSize -= delsize
		sc.countermux.Unlock()
		log.Printf("SanitizedCache: cleanup: removed %d bytes from cache", delsize)
	}(sc, delsize)
}
