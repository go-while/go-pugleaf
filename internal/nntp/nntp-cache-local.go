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
	// arrivals is the server's recentArrivals table; run cleans it with the same ticker
	// (nil: nothing to clean).
	arrivals *RecentArrivals
}

// SetLocal430TTL sets how long a message-id that answered 430 is answered from the
// local430 cache (cmd/nntp-server -local430ttl); ttl <= 0 means DefaultLocal430TTL. It may be
// called while serving: entries already cached use the new TTL from then on.
func (s *NNTPServer) SetLocal430TTL(ttl time.Duration) {
	if s == nil || s.local430 == nil {
		return
	}
	s.local430.setTTL(ttl)
}

// DefaultPostCommitWait is how long handlePost waits for the batch writer to commit a posted
// article before it answers 240 (cmd/nntp-server -post-commit-wait); 0 disables the wait.
const DefaultPostCommitWait = 5 * time.Second

// SetPostCommitWait sets the POST commit wait (plan post-430-window); d <= 0 means no wait.
// It may be called while serving.
func (s *NNTPServer) SetPostCommitWait(d time.Duration) {
	if s == nil {
		return
	}
	if d < 0 {
		d = 0
	}
	s.postWait.Store(int64(d))
}

// PostCommitWait returns the POST commit wait in effect (0 = none).
func (s *NNTPServer) PostCommitWait() time.Duration {
	if s == nil {
		return 0
	}
	return time.Duration(s.postWait.Load())
}

// Local430TTL returns the TTL of the local430 cache in effect.
func (s *NNTPServer) Local430TTL() time.Duration {
	if s == nil || s.local430 == nil {
		return DefaultLocal430TTL
	}
	lc := s.local430
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.ttlLocked()
}

// setTTL sets the TTL (<= 0: DefaultLocal430TTL).
func (lc *Local430) setTTL(ttl time.Duration) {
	lc.mu.Lock()
	lc.ttl = ttl
	lc.mu.Unlock()
}

// ttlLocked returns the TTL in effect; lc.mu must be held.
func (lc *Local430) ttlLocked() time.Duration {
	if lc.ttl <= 0 {
		return DefaultLocal430TTL
	}
	return lc.ttl
}

// Check reports whether messageID answered 430 less than ttl ago. An expired
// entry is removed.
func (lc *Local430) Check(messageID string) bool {
	lc.mu.RLock()
	added, ok := lc.cache[messageID]
	ttl := lc.ttlLocked()
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
	lc.mu.Lock()
	defer lc.mu.Unlock()
	cutoff := time.Now().Add(-lc.ttlLocked())
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
			lc.arrivals.Cleanup()
		}
	}
}

const (
	// ArrivalGrace is how long a same-process arrival (POST/IHAVE/TAKETHIS) is remembered in
	// recentArrivals: the by-message-id lookup tries the arrival's groups directly and never
	// caches a miss for it in local430 (plan post-430-window). It covers the batch writer
	// (database.BatchInterval, 3 s) plus the history flush (BatchTimeout, 1 s) with margin.
	ArrivalGrace = 10 * time.Second
	// recentArrivalsMaxEntries bounds recentArrivals; NoteArrival refuses new ids when it is
	// full of entries younger than ArrivalGrace.
	recentArrivalsMaxEntries = local430MaxEntries
)

// arrival is one recentArrivals entry.
type arrival struct {
	groups []string  // Newsgroups names as received (never modified after NoteArrival)
	at     time.Time // time of the 240/235/239
}

// RecentArrivals remembers the articles this process accepted in the last ArrivalGrace,
// with the groups they were sent to, so a by-message-id lookup does not depend on the
// history index (flushed after the group DB commit) and does not poison local430 with a
// miss while the article is still on its way to the databases. All methods are safe on a
// nil table (they do nothing / find nothing).
type RecentArrivals struct {
	mu    sync.Mutex
	cache map[string]arrival // message-id -> arrival
}

// newRecentArrivals returns an empty table.
func newRecentArrivals() *RecentArrivals {
	return &RecentArrivals{cache: make(map[string]arrival)}
}

// NoteArrival remembers that messageID was accepted now for groups (copied). It returns
// false (and stores nothing) for ids over 250 octets and for new ids when the table holds
// recentArrivalsMaxEntries entries younger than ArrivalGrace.
func (ra *RecentArrivals) NoteArrival(messageID string, groups []string) bool {
	if ra == nil || len(messageID) > 250 { // RFC 5536 §3.1.3: message-ids are at most 250 octets
		return false
	}
	entry := arrival{groups: append([]string(nil), groups...), at: time.Now()}
	ra.mu.Lock()
	defer ra.mu.Unlock()
	if ra.cache == nil {
		ra.cache = make(map[string]arrival)
	}
	if _, exists := ra.cache[messageID]; !exists && len(ra.cache) >= recentArrivalsMaxEntries {
		ra.cleanupLocked()
		if len(ra.cache) >= recentArrivalsMaxEntries {
			return false
		}
	}
	ra.cache[messageID] = entry
	return true
}

// RecentArrival returns the groups of messageID when it arrived less than ArrivalGrace ago.
// An expired entry is removed. The returned slice must not be modified.
func (ra *RecentArrivals) RecentArrival(messageID string) (groups []string, ok bool) {
	if ra == nil {
		return nil, false
	}
	ra.mu.Lock()
	defer ra.mu.Unlock()
	e, found := ra.cache[messageID]
	if !found {
		return nil, false
	}
	if time.Since(e.at) >= ArrivalGrace {
		delete(ra.cache, messageID)
		return nil, false
	}
	return e.groups, true
}

// Cleanup removes entries older than ArrivalGrace (called by Local430.run).
func (ra *RecentArrivals) Cleanup() {
	if ra == nil {
		return
	}
	ra.mu.Lock()
	ra.cleanupLocked()
	ra.mu.Unlock()
}

// cleanupLocked removes expired entries; ra.mu must be held.
func (ra *RecentArrivals) cleanupLocked() {
	cutoff := time.Now().Add(-ArrivalGrace)
	for messageID, e := range ra.cache {
		if !e.at.After(cutoff) {
			delete(ra.cache, messageID)
		}
	}
}
