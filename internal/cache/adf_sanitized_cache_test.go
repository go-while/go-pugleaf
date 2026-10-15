package cache

import (
	"fmt"
	"html/template"
	"sync"
	"testing"
	"time"
)

func adfSanCacheLen(sc *SanitizedCache) int {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return len(sc.cache)
}

// TestAdfSanCacheBounded: maxEntries is enforced on insert (A-web-2).
func TestAdfSanCacheBounded(t *testing.T) {
	sc := NewSanitizedCache(100, time.Hour)
	t.Cleanup(sc.Stop)

	const total = 1000
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("<adf-web-cache-%d@test.invalid>", i)
		sc.SetArticle(id, map[string]template.HTML{
			"subject":  template.HTML(fmt.Sprintf("subject %d", i)),
			"bodytext": "body",
		})
		if n := adfSanCacheLen(sc); n > 100 {
			t.Fatalf("after %d inserts: len=%d > maxEntries 100", i+1, n)
		}
	}
	n := adfSanCacheLen(sc)
	if n > 100 || n < 90 {
		t.Fatalf("final len=%d, want 90..100", n)
	}
	// The most recently set ids must still be present.
	for i := total - 50; i < total; i++ {
		id := fmt.Sprintf("<adf-web-cache-%d@test.invalid>", i)
		v, ok := sc.GetField(id, "subject")
		if !ok || string(v) != fmt.Sprintf("subject %d", i) {
			t.Fatalf("recent id %s missing or wrong: ok=%v v=%q", id, ok, v)
		}
	}
	// The oldest ids must be gone.
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("<adf-web-cache-%d@test.invalid>", i)
		if _, ok := sc.GetArticle(id); ok {
			t.Fatalf("old id %s still cached", id)
		}
	}

	// SetField and BatchSetArticles are bounded as well.
	for i := 0; i < 300; i++ {
		sc.SetField(fmt.Sprintf("<adf-web-cache-f%d@test.invalid>", i), "subject", "s")
	}
	if n := adfSanCacheLen(sc); n > 100 {
		t.Fatalf("after SetField: len=%d > 100", n)
	}
	batch := make(map[string]map[string]template.HTML)
	for i := 0; i < 250; i++ {
		batch[fmt.Sprintf("<adf-web-cache-b%d@test.invalid>", i)] = map[string]template.HTML{"subject": "s"}
	}
	sc.BatchSetArticles(batch)
	if n := adfSanCacheLen(sc); n > 100 {
		t.Fatalf("after BatchSetArticles: len=%d > 100", n)
	}

	// Size accounting follows the evictions (SetField updates it asynchronously).
	deadline := time.Now().Add(2 * time.Second)
	for {
		var sum int64
		sc.mutex.RLock()
		for _, a := range sc.cache {
			a.mux.RLock()
			sum += a.Size
			a.mux.RUnlock()
		}
		sc.mutex.RUnlock()
		got := sc.GetCachedSize()
		if got == sum {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cachedSize=%d, sum of entries=%d", got, sum)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAdfSanCacheUnbounded: maxEntries <= 0 keeps everything.
func TestAdfSanCacheUnbounded(t *testing.T) {
	sc := NewSanitizedCache(0, time.Hour)
	t.Cleanup(sc.Stop)
	for i := 0; i < 200; i++ {
		sc.SetArticle(fmt.Sprintf("<adf-web-cache-u%d@test.invalid>", i), map[string]template.HTML{"subject": "s"})
	}
	if n := adfSanCacheLen(sc); n != 200 {
		t.Fatalf("len=%d, want 200", n)
	}
}

// TestAdfSanCacheGetFieldRace: GetField reads under article.mux (A-web-3); run with -race.
func TestAdfSanCacheGetFieldRace(t *testing.T) {
	sc := NewSanitizedCache(100, time.Hour)
	t.Cleanup(sc.Stop)

	const id = "<adf-web-cache-race@test.invalid>"
	sc.SetField(id, "subject", "start")

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				if i%2 == 0 {
					sc.SetField(id, "subject", template.HTML(fmt.Sprintf("g%d-%d", g, i)))
					sc.SetField(id, "bodytext", template.HTML(fmt.Sprintf("body g%d-%d", g, i)))
					continue
				}
				if v, ok := sc.GetField(id, "subject"); !ok || v == "" {
					t.Errorf("GetField subject: ok=%v v=%q", ok, v)
					return
				}
				_, _ = sc.GetField(id, "bodytext")
			}
		}(g)
	}
	wg.Wait()
}

// TestAdfSanCacheCleanupExpired: cleanup removes expired entries and keeps the size in step.
func TestAdfSanCacheCleanupExpired(t *testing.T) {
	sc := NewSanitizedCache(100, time.Hour)
	t.Cleanup(sc.Stop)
	const oldID = "<adf-web-cache-old@test.invalid>"
	const newID = "<adf-web-cache-new@test.invalid>"
	sc.SetArticle(oldID, map[string]template.HTML{"subject": "12345"})
	sc.SetArticle(newID, map[string]template.HTML{"subject": "abc"})
	sc.mutex.RLock()
	old := sc.cache[oldID]
	sc.mutex.RUnlock()
	old.mux.Lock()
	old.CreatedAt = time.Now().Add(-2 * time.Hour)
	old.mux.Unlock()

	sc.cleanup()
	if _, ok := sc.GetArticle(oldID); ok {
		t.Fatal("expired entry still cached")
	}
	if _, ok := sc.GetArticle(newID); !ok {
		t.Fatal("fresh entry removed")
	}
	if got := sc.GetCachedSize(); got != 3 {
		t.Fatalf("cachedSize=%d, want 3", got)
	}
	// Nothing expired: no change.
	sc.cleanup()
	if got := sc.GetCachedSize(); got != 3 {
		t.Fatalf("cachedSize after no-op cleanup=%d, want 3", got)
	}
}
