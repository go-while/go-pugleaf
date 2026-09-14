package history

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func cacheTestID(i int) string {
	return fmt.Sprintf("<cache-test-%d@example.invalid>", i)
}

// cacheTestSetState sets Response and CachedEntryExpires of a cached item
func cacheTestSetState(c *MsgIdItemCache, id string, response int, expires time.Time) *MessageIdItem {
	item := c.GetORCreate(id)
	item.Mux.Lock()
	item.Response = response
	item.CachedEntryExpires = expires
	item.Mux.Unlock()
	return item
}

func cacheTestHas(c *MsgIdItemCache, id string) bool {
	s := c.shard(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	return ok
}

func cacheTestItems(c *MsgIdItemCache) int {
	_, items, _ := c.Stats()
	return items
}

func TestCacheGetORCreateConcurrentUnique(t *testing.T) {
	const workers = 8
	numIDs := 200000
	if testing.Short() {
		numIDs = 20000
	}
	ids := make([]string, numIDs)
	for i := range ids {
		ids[i] = cacheTestID(i)
	}

	c := newMsgIdItemCache()
	results := make([][]*MessageIdItem, workers)
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		results[w] = make([]*MessageIdItem, numIDs)
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// alternate direction so goroutines collide on inserts
			for j := 0; j < numIDs; j++ {
				i := j
				if w%2 == 1 {
					i = numIDs - 1 - j
				}
				results[w][i] = c.GetORCreate(ids[i])
			}
		}(w)
	}
	wg.Wait()
	t.Logf("%d goroutines x %d IDs took %v", workers, numIDs, time.Since(start))

	dupes := 0
	for i := 0; i < numIDs; i++ {
		first := results[0][i]
		if first == nil {
			t.Fatalf("GetORCreate returned nil for %s", ids[i])
		}
		if first.MessageId != ids[i] {
			t.Fatalf("item MessageId=%q want %q", first.MessageId, ids[i])
		}
		for w := 1; w < workers; w++ {
			if results[w][i] != first {
				dupes++
				break
			}
		}
	}
	if dupes > 0 {
		t.Fatalf("%d IDs got more than one distinct *MessageIdItem", dupes)
	}
	if got := cacheTestItems(c); got != numIDs {
		t.Fatalf("item count=%d want %d", got, numIDs)
	}
}

func TestCacheCleanExpiredEntries(t *testing.T) {
	c := newMsgIdItemCache()
	past := time.Now().Add(-time.Second)
	future := time.Now().Add(time.Hour)

	evictIDs := map[string]int{
		"<dupes-expired@t>": CaseDupes,
		"<error-expired@t>": CaseError,
		"<unset-expired@t>": 0,
		"<pass-expired@t>":  CasePass,
		"<retry-expired@t>": CaseRetry,
	}
	keepIDs := map[string]int{
		"<dupes-fresh@t>": CaseDupes,
		"<error-fresh@t>": CaseError,
		"<unset-fresh@t>": 0,
		"<lock-fresh@t>":  CaseLock,
		"<write-fresh@t>": CaseWrite,
	}
	for id, resp := range evictIDs {
		cacheTestSetState(c, id, resp, past)
	}
	for id, resp := range keepIDs {
		cacheTestSetState(c, id, resp, future)
	}
	// expired, but still within StuckEntryMaxAge
	cacheTestSetState(c, "<lock-expired@t>", CaseLock, past)
	cacheTestSetState(c, "<write-expired@t>", CaseWrite, past)

	if n := c.CleanExpiredEntries(); n != len(evictIDs) {
		t.Fatalf("CleanExpiredEntries evicted %d want %d", n, len(evictIDs))
	}
	for id := range evictIDs {
		if cacheTestHas(c, id) {
			t.Errorf("%s should have been evicted", id)
		}
	}
	for id := range keepIDs {
		if !cacheTestHas(c, id) {
			t.Errorf("%s should have been kept", id)
		}
	}
	for _, id := range []string{"<lock-expired@t>", "<write-expired@t>"} {
		if !cacheTestHas(c, id) {
			t.Errorf("%s should be kept while within StuckEntryMaxAge", id)
		}
	}

	// age the in-progress items beyond StuckEntryMaxAge
	old := StuckEntryMaxAge
	StuckEntryMaxAge = time.Minute
	t.Cleanup(func() { StuckEntryMaxAge = old })
	longAgo := time.Now().Add(-2 * time.Minute)
	cacheTestSetState(c, "<lock-expired@t>", CaseLock, longAgo)
	cacheTestSetState(c, "<write-expired@t>", CaseWrite, longAgo)

	if n := c.CleanExpiredEntries(); n != 2 {
		t.Fatalf("CleanExpiredEntries evicted %d stuck entries want 2", n)
	}
	for _, id := range []string{"<lock-expired@t>", "<write-expired@t>"} {
		if cacheTestHas(c, id) {
			t.Errorf("stuck %s should have been evicted", id)
		}
	}
	if got := cacheTestItems(c); got != len(keepIDs) {
		t.Fatalf("item count=%d want %d", got, len(keepIDs))
	}
}

func TestCacheZeroExpiresGetsTTL(t *testing.T) {
	c := newMsgIdItemCache()
	item := cacheTestSetState(c, "<zero@t>", CaseDupes, time.Time{})
	if n := c.CleanExpiredEntries(); n != 0 {
		t.Fatalf("evicted %d want 0", n)
	}
	item.Mux.RLock()
	expires := item.CachedEntryExpires
	item.Mux.RUnlock()
	if expires.IsZero() || !expires.After(time.Now()) {
		t.Fatalf("CachedEntryExpires=%v, want a time in the future", expires)
	}
	if !cacheTestHas(c, "<zero@t>") {
		t.Fatal("item with zero expiry should be kept")
	}
}

func TestCacheEvictedItemUntouched(t *testing.T) {
	c := newMsgIdItemCache()
	id := "<untouched@t>"
	past := time.Now().Add(-time.Second)
	item := cacheTestSetState(c, id, CaseDupes, past)

	if n := c.CleanExpiredEntries(); n != 1 {
		t.Fatalf("evicted %d want 1", n)
	}
	item.Mux.RLock()
	defer item.Mux.RUnlock()
	if item.MessageId != id || item.Response != CaseDupes || !item.CachedEntryExpires.Equal(past) {
		t.Fatalf("evicted item was modified: MessageId=%q Response=%x Expires=%v",
			item.MessageId, item.Response, item.CachedEntryExpires)
	}

	// Delete must not modify the item either
	item2 := c.GetORCreate("<delete@t>")
	item2.Response = CaseLock
	if !c.Delete("<delete@t>") {
		t.Fatal("Delete returned false for existing item")
	}
	if c.Delete("<delete@t>") {
		t.Fatal("Delete returned true for missing item")
	}
	if item2.MessageId != "<delete@t>" || item2.Response != CaseLock {
		t.Fatal("Delete modified the item")
	}
}

func TestCacheGetORCreateAfterEviction(t *testing.T) {
	c := newMsgIdItemCache()
	id := "<recreate@t>"
	old := cacheTestSetState(c, id, CaseDupes, time.Now().Add(-time.Second))
	if n := c.CleanExpiredEntries(); n != 1 {
		t.Fatalf("evicted %d want 1", n)
	}
	fresh := c.GetORCreate(id)
	if fresh == old {
		t.Fatal("GetORCreate after eviction returned the evicted item")
	}
	if fresh.MessageId != id || fresh.Response != 0 {
		t.Fatalf("fresh item MessageId=%q Response=%x", fresh.MessageId, fresh.Response)
	}
	if !fresh.CachedEntryExpires.After(time.Now()) {
		t.Fatalf("fresh item CachedEntryExpires=%v not in the future", fresh.CachedEntryExpires)
	}
	if c.GetORCreate(id) != fresh {
		t.Fatal("second GetORCreate returned a different item")
	}
}

func TestCacheStats(t *testing.T) {
	c := newMsgIdItemCache()
	total, occupied, items, maxChain, load := c.DetailedStats()
	if total != 256 || occupied != 0 || items != 0 || maxChain != 0 || load != 0 {
		t.Fatalf("empty cache stats: %d %d %d %d %v", total, occupied, items, maxChain, load)
	}

	const n = 1000
	for i := 0; i < n; i++ {
		c.GetORCreate(cacheTestID(i))
	}
	c.GetORCreate(cacheTestID(0)) // existing, must not count twice

	wantOccupied, wantMax := 0, 0
	for i := range c.shards {
		l := len(c.shards[i].m)
		if l > 0 {
			wantOccupied++
		}
		if l > wantMax {
			wantMax = l
		}
	}
	total, occupied, items, maxChain, load = c.DetailedStats()
	if total != 256 || occupied != wantOccupied || items != n || maxChain != wantMax {
		t.Fatalf("DetailedStats=(%d,%d,%d,%d) want (256,%d,%d,%d)", total, occupied, items, maxChain, wantOccupied, n, wantMax)
	}
	if load != float64(n)/256 {
		t.Fatalf("loadFactor=%v want %v", load, float64(n)/256)
	}
	b, it, mc := c.Stats()
	if b != total || it != items || mc != maxChain {
		t.Fatalf("Stats=(%d,%d,%d) mismatch DetailedStats", b, it, mc)
	}

	c.Clear()
	if _, occupied, items, _, _ = c.DetailedStats(); occupied != 0 || items != 0 {
		t.Fatalf("after Clear occupied=%d items=%d", occupied, items)
	}
}
