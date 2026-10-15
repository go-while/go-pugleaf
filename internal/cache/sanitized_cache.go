package cache

import (
	"fmt"
	"html/template"
	"log"
	"sort"
	"strings"
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
	stopOnce    sync.Once    // Stop closes stopCleanup once per cache instance
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

// sanitizedFieldNames maps every accepted field name (lower case) to its
// canonical name. It is the only alias table: GetField, SetField, SetArticle
// and BatchSetArticles all resolve names through it (A-web-10), so an alias
// such as "from" or "fromheader" always hits the same slot.
var sanitizedFieldNames = map[string]string{
	"subject":      "subject",
	"author":       "from",
	"from":         "from",
	"fromheader":   "from",
	"from_header":  "from",
	"body":         "body",
	"bodytext":     "body",
	"body_text":    "body",
	"date":         "date",
	"datestring":   "date",
	"date_string":  "date",
	"date_since":   "date_since",
	"references":   "references",
	"messageid":    "messageid",
	"message_id":   "messageid",
	"path":         "path",
	"headers_json": "headers_json",
}

// CanonicalField returns the canonical name of a sanitized field and whether
// the name is known. Field names are case-insensitive.
func CanonicalField(field string) (string, bool) {
	canon, ok := sanitizedFieldNames[strings.ToLower(field)]
	return canon, ok
}

// slot returns the struct field that stores the given field name, or nil for
// an unknown name. Caller must hold article.mux.
func (article *SanitizedArticle) slot(field string) *template.HTML {
	canon, ok := CanonicalField(field)
	if !ok {
		return nil
	}
	switch canon {
	case "subject":
		return &article.Subject
	case "from":
		return &article.Author
	case "body":
		return &article.Body
	case "date":
		return &article.Date
	case "references":
		return &article.References
	case "messageid":
		return &article.MessageID
	case "path":
		return &article.Path
	case "headers_json":
		return &article.HeadersJSON
	case "date_since":
		return &article.DateSince
	}
	return nil
}

// setFieldsLocked stores the known fields and returns the size change.
// Unknown field names are ignored and not counted. Caller must hold article.mux.
func (article *SanitizedArticle) setFieldsLocked(fields map[string]template.HTML) int64 {
	var sizeDiff int64
	for field, value := range fields {
		p := article.slot(field)
		if p == nil {
			continue
		}
		sizeDiff += int64(len(value)) - int64(len(*p))
		*p = value
	}
	article.Size += sizeDiff
	return sizeDiff
}

// hasKnownField reports whether at least one field name is known.
func hasKnownField(fields map[string]template.HTML) bool {
	for field := range fields {
		if _, ok := CanonicalField(field); ok {
			return true
		}
	}
	return false
}

// GetField retrieves a specific sanitized field from a cached article
func (sc *SanitizedCache) GetField(messageID string, field string) (template.HTML, bool) {

	sc.mutex.RLock()
	article, exists := sc.cache[messageID]
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

	// Update last used time and read the requested field under the same lock:
	// SetField/SetArticle/BatchSetArticles write these fields under article.mux.
	var value template.HTML
	article.mux.Lock()
	article.LastUsed = time.Now()
	if p := article.slot(field); p != nil {
		value = *p
	}
	article.mux.Unlock()

	// An empty field (or an unknown field name) is treated as a miss
	if len(value) == 0 {
		return "", false
	}
	return value, true
}

// GetArticle retrieves a complete cached sanitized article
func (sc *SanitizedCache) GetArticle(messageID string) (*SanitizedArticle, bool) {
	sc.mutex.RLock()
	article, exists := sc.cache[messageID]
	sc.mutex.RUnlock()

	if !exists {
		return nil, false
	}

	// Update last used time
	article.mux.Lock()
	article.LastUsed = time.Now()
	article.mux.Unlock()

	//log.Printf("SanitizedCache: GetArticle: found cached article for messageID '%s'", messageID)
	return article, true
}

// SetField stores a single sanitized field, creating or updating the article entry.
// An unknown field name is not stored and creates no entry.
func (sc *SanitizedCache) SetField(messageID string, field string, value template.HTML) {
	if _, ok := CanonicalField(field); !ok {
		return
	}
	sc.SetArticle(messageID, map[string]template.HTML{field: value})
}

// SetArticle stores sanitized fields of one article in one operation. Fields not
// in the map keep their cached value; unknown field names are ignored.
func (sc *SanitizedCache) SetArticle(messageID string, sanitizedFields map[string]template.HTML) {
	if !hasKnownField(sanitizedFields) {
		return
	}
	now := time.Now()

	sc.mutex.Lock()
	// Get existing article or create new one
	article, exists := sc.cache[messageID]
	if !exists {
		article = &SanitizedArticle{
			CreatedAt: now,
			LastUsed:  now,
		}
		sc.cache[messageID] = article
	}

	// Set the fields while still holding sc.mutex (as BatchSetArticles does), so an
	// eviction cannot remove the entry between insert and field set and leave
	// cachedSize counting an entry that is gone.
	article.mux.Lock()
	sizeDiff := article.setFieldsLocked(sanitizedFields)
	article.LastUsed = now
	article.mux.Unlock()

	sc.countermux.Lock()
	sc.cachedSize += sizeDiff
	sc.countermux.Unlock()

	if !exists {
		sc.enforceLimitLocked()
	}
	sc.mutex.Unlock()
}

// BatchSetArticles efficiently stores multiple complete sanitized articles.
// cachedSize is updated inside the same sc.mutex section as the writes and the
// eviction (as in SetArticle), so the counter always matches the entries a
// concurrent eviction or cleanup sees: a write that lands on an entry evicted a
// moment before re-creates it and is counted once, never subtracted twice.
func (sc *SanitizedCache) BatchSetArticles(articles map[string]map[string]template.HTML) {
	if len(articles) == 0 {
		return
	}

	now := time.Now()
	var totalSizeDiff int64

	// Process all articles in one lock cycle
	added := false
	sc.mutex.Lock()
	for messageID, fields := range articles {
		if !hasKnownField(fields) {
			continue
		}

		// Get existing article or create new one
		article, exists := sc.cache[messageID]
		if !exists {
			article = &SanitizedArticle{
				CreatedAt: now,
				LastUsed:  now,
			}
			sc.cache[messageID] = article
			added = true
		}

		// Set fields for this article
		article.mux.Lock()
		totalSizeDiff += article.setFieldsLocked(fields)
		article.LastUsed = now
		article.mux.Unlock()
	}
	// Count the writes before the eviction subtracts the sizes of the entries it removes.
	sc.countermux.Lock()
	sc.cachedSize += totalSizeDiff
	sc.countermux.Unlock()
	if added {
		sc.enforceLimitLocked()
	}
	sc.mutex.Unlock()
}

// Clear removes all entries from the cache and resets cachedSize with them.
func (sc *SanitizedCache) Clear() {
	sc.mutex.Lock()
	sc.cache = make(map[string]*SanitizedArticle)
	sc.countermux.Lock()
	sc.cachedSize = 0
	sc.countermux.Unlock()
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

// Stop shuts down the cleanup goroutine of this cache instance. It may be called
// more than once; only the first call closes stopCleanup.
func (sc *SanitizedCache) Stop() {
	sc.stopOnce.Do(func() { close(sc.stopCleanup) })
}

// hashMessageID creates a consistent hash from a message ID
func (sc *SanitizedCache) xxxhashMessageID(messageID string) string {
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

// enforceLimitLocked bounds the cache to maxEntries articles (A-web-2).
// Caller must hold sc.mutex (write lock). When the cache is over its limit,
// the least recently used entries are evicted down to 90% of maxEntries so the
// sort is not repeated on every insert. maxEntries <= 0 means unbounded.
func (sc *SanitizedCache) enforceLimitLocked() {
	if sc.maxEntries <= 0 || len(sc.cache) <= sc.maxEntries {
		return
	}
	target := sc.maxEntries * 9 / 10
	if target < 1 {
		target = 1
	}
	sc.evictOldest(len(sc.cache) - target)
}

// evictOldest removes the n least recently used entries in one pass over a
// slice sorted by LastUsed. Caller must hold sc.mutex (write lock).
func (sc *SanitizedCache) evictOldest(n int) {
	if n <= 0 || len(sc.cache) == 0 {
		return
	}
	type lruEntry struct {
		key      string
		lastUsed time.Time
		size     int64
	}
	entries := make([]lruEntry, 0, len(sc.cache))
	for key, article := range sc.cache {
		article.mux.RLock()
		entries = append(entries, lruEntry{key: key, lastUsed: article.LastUsed, size: article.Size})
		article.mux.RUnlock()
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].lastUsed.Before(entries[j].lastUsed)
	})
	if n > len(entries) {
		n = len(entries)
	}
	var delsize int64
	for _, e := range entries[:n] {
		delete(sc.cache, e.key)
		delsize += e.size
	}
	sc.countermux.Lock()
	sc.cachedSize -= delsize
	sc.countermux.Unlock()
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
	if sc.maxAge <= 0 {
		sc.mutex.RUnlock()
		return // no cleanup needed
	}
	sc.mutex.RUnlock()

	now := time.Now()
	keysToDelete := make([]string, 0)

	sc.mutex.RLock()
	for key, article := range sc.cache {
		article.mux.RLock()
		if now.Sub(article.CreatedAt) > sc.maxAge {
			keysToDelete = append(keysToDelete, key)
		}
		article.mux.RUnlock()
	}
	sc.mutex.RUnlock()

	if len(keysToDelete) == 0 {
		return // nothing expired: no size update, no log line
	}

	// Re-check under the write lock: an entry may have been evicted (and its
	// size already subtracted) or replaced since the read pass.
	var delsize int64
	removed := 0
	sc.mutex.Lock()
	for _, key := range keysToDelete {
		article, ok := sc.cache[key]
		if !ok {
			continue
		}
		article.mux.RLock()
		expired := now.Sub(article.CreatedAt) > sc.maxAge
		size := article.Size
		article.mux.RUnlock()
		if !expired {
			continue
		}
		delete(sc.cache, key)
		delsize += size
		removed++
	}
	// Subtract inside the sc.mutex section (as evictOldest and BatchSetArticles do): a Clear
	// between the unlock and the subtraction would otherwise leave cachedSize negative.
	if removed > 0 {
		sc.countermux.Lock()
		sc.cachedSize -= delsize
		sc.countermux.Unlock()
	}
	sc.mutex.Unlock()

	if removed == 0 {
		return
	}
	log.Printf("SanitizedCache: cleanup: removed %d entries (%d bytes) from cache", removed, delsize)
}
