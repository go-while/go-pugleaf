package nntp

import (
	"sync"
	"time"
)

const (
	// DefaultLocal430TTL is how long a message-id that answered 430 is answered
	// from local430 without asking the processor again.
	DefaultLocal430TTL = 60 * time.Second
	// local430MaxEntries bounds the cache; Add refuses new ids when it is full.
	local430MaxEntries = 65536
	// local430CleanupEvery is the interval of the background cleanup (run).
	local430CleanupEvery = 15 * time.Second
)

// Local430 is a negative cache of message-ids that answered 430, so repeated
// lookups of a missing article do not hit the processor each time. Entries
// expire after ttl (<= 0: DefaultLocal430TTL); intake calls Remove when the
// article arrives.
type Local430 struct {
	mu    sync.RWMutex
	cache map[string]time.Time // message-id -> time of the 430
	ttl   time.Duration
}

// Check reports whether messageID answered 430 less than ttl ago. An expired
// entry is removed.
func (lc *Local430) Check(messageID string) bool {
	ttl := lc.ttl
	if ttl <= 0 {
		ttl = DefaultLocal430TTL
	}
	lc.mu.RLock()
	added, ok := lc.cache[messageID]
	lc.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Since(added) < ttl {
		return true
	}
	lc.mu.Lock()
	if added, ok := lc.cache[messageID]; ok && time.Since(added) >= ttl {
		delete(lc.cache, messageID)
	}
	lc.mu.Unlock()
	return false
}

// Add remembers a 430 for messageID. It returns false (and caches nothing) for
// ids over 250 octets and for new ids when the cache holds local430MaxEntries.
func (lc *Local430) Add(messageID string) bool {
	if len(messageID) > 250 { // RFC 5536 §3.1.3: message-ids are at most 250 octets
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.cache == nil {
		lc.cache = make(map[string]time.Time)
	}
	if _, exists := lc.cache[messageID]; !exists && len(lc.cache) >= local430MaxEntries {
		return false
	}
	lc.cache[messageID] = time.Now()
	return true
}

// Remove forgets messageID (no-op when absent); called when the article arrives.
func (lc *Local430) Remove(messageID string) {
	lc.mu.Lock()
	delete(lc.cache, messageID)
	lc.mu.Unlock()
}

// Cleanup removes entries older than ttl.
func (lc *Local430) Cleanup() {
	ttl := lc.ttl
	if ttl <= 0 {
		ttl = DefaultLocal430TTL
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	cutoff := time.Now().Add(-ttl)
	for messageID, added := range lc.cache {
		if !added.After(cutoff) {
			delete(lc.cache, messageID)
		}
	}
}

// run cleans the cache every local430CleanupEvery until stop is closed.
func (lc *Local430) run(stop <-chan struct{}) {
	ticker := time.NewTicker(local430CleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			lc.Cleanup()
		}
	}
}
