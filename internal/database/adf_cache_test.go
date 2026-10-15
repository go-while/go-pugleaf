package database

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// adf_cache_test.go covers the adf-db-cache slice of the audit-delta-fixes plan:
// A-db-2 (article cache map entries outliving their eviction), FU:E16 (GetCachedThreadReplies
// swallowing scan errors and rows.Err) and OPS-1e (MemCachedThreads.CleanCron without a stop).

// TestAdfCacheMapBounded: entries whose ArticleNums are keyed by a newsgroup pointer that is
// not the current TasksMap pointer (as after ExpireCache replaced it) must still leave the map
// on eviction. removeElement used to recompute the key through GetNewsgroupPointer, got
// article number 0, deleted the wrong key and left the evicted body in the map.
func TestAdfCacheMapBounded(t *testing.T) {
	db := w0DB(t)
	group := w0Name("adfcache")
	ac := NewArticleCache(2, time.Hour, db)

	// A local copy of the name: its address is never the pointer held in db.Batch.TasksMap.
	stale := group

	var wantSize int64
	for i := int64(1); i <= 5; i++ {
		art := &models.Article{
			BodyText:    strings.Repeat("b", int(10*i)),
			HeadersJSON: strings.Repeat("h", int(i)),
			Bytes:       999999, // must not be what is subtracted on removal
			ArticleNums: map[*string]int64{&stale: i},
		}
		ac.Put(group, i, art)
		if i >= 4 {
			wantSize += int64(len(art.BodyText) + len(art.HeadersJSON))
		}
	}

	ac.mutex.RLock()
	mapLen, listLen, totalSize := len(ac.cache), ac.lruList.Len(), ac.totalSize
	ac.mutex.RUnlock()
	if mapLen > 2 {
		t.Errorf("len(ac.cache) = %d after 5 Puts into a cache of 2, want <= 2 (evicted entries stayed in the map)", mapLen)
	}
	if listLen != 2 {
		t.Errorf("lruList.Len() = %d, want 2", listLen)
	}
	if totalSize != wantSize {
		t.Errorf("totalSize = %d, want %d (the two remaining entries)", totalSize, wantSize)
	}

	for i := int64(1); i <= 3; i++ {
		if _, ok := ac.Get(group, i); ok {
			t.Errorf("Get(%s, %d) hit, want a miss for an evicted entry", group, i)
		}
	}
	for i := int64(4); i <= 5; i++ {
		if _, ok := ac.Get(group, i); !ok {
			t.Errorf("Get(%s, %d) missed, want a hit for a cached entry", group, i)
		}
	}

	// Re-putting an existing key replaces its size instead of adding to it.
	ac.Put(group, 5, &models.Article{BodyText: "x", ArticleNums: map[*string]int64{&stale: 5}})
	ac.Remove(group, 4)
	ac.ClearGroup(group)
	ac.mutex.RLock()
	mapLen, listLen, totalSize = len(ac.cache), ac.lruList.Len(), ac.totalSize
	ac.mutex.RUnlock()
	if mapLen != 0 || listLen != 0 || totalSize != 0 {
		t.Errorf("after Remove+ClearGroup: map %d, list %d, totalSize %d; want 0, 0, 0", mapLen, listLen, totalSize)
	}
}

// TestAdfCacheThreadRepliesRowsErr: an unreadable reply row is skipped and logged once per
// call; the readable replies are still returned and the count still comes from child_articles.
func TestAdfCacheThreadRepliesRowsErr(t *testing.T) {
	db, gdb := fuThreadcountArticles(t, 4) // root 1, children 2..5
	fuBatchFaultsHealthyRow(t, gdb, 1, "2,3,4,5")

	// date_sent as a BLOB cannot be scanned into time.Time: a conversion error, not a
	// driver-level failure, so rows.Next keeps going.
	for _, num := range []int64{3, 4} {
		if _, err := RetryableExec(gdb.DB, "UPDATE articles SET date_sent = X'00' WHERE article_num = ?", num); err != nil {
			t.Fatalf("corrupt date_sent of %d: %v", num, err)
		}
	}

	logs := fuBatchCaptureLog(t)
	replies, total, err := db.GetCachedThreadReplies(gdb, 1, 1, 50)
	if err != nil {
		t.Fatalf("GetCachedThreadReplies: %v", err)
	}
	if total != 4 {
		t.Errorf("totalReplies = %d, want 4 (from child_articles)", total)
	}
	got := make([]int64, 0, len(replies))
	for _, r := range replies {
		got = append(got, r.ArticleNum)
	}
	if fmt.Sprint(got) != fmt.Sprint([]int64{2, 5}) {
		t.Errorf("replies = %v, want [2 5] (the readable rows)", got)
	}

	want := fmt.Sprintf("[CACHE:THREADS] GetCachedThreadReplies(%s/1): scan: ", gdb.Newsgroup)
	if n := strings.Count(logs.String(), want); n != 1 {
		t.Errorf("scan error logged %d times, want once per call; log tail:\n%s", n, logs.tail(10))
	}
}

// TestAdfCacheCleanCronStops: closing the stop channel ends the CleanCron goroutine.
func TestAdfCacheCleanCronStops(t *testing.T) {
	before := runtime.NumGoroutine()
	ch := make(chan struct{})
	mem := NewMemCachedThreadsStop(ch)
	if mem == nil || mem.Groups == nil {
		t.Fatal("NewMemCachedThreadsStop returned an unusable cache")
	}
	close(ch)

	deadline := time.Now().Add(time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d one second after close(stop), want <= %d (CleanCron still running)", n, before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
