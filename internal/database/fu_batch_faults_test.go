package database

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fu_batch_faults_test.go covers E6 and E7 of the web-db-followups plan: what the batch writer
// does when a phase of a batch it has already committed fails.
//
// E7 (batchUpdateThreadCache, db_batch.go): the select-miss fallback used to fire on *any*
// error from its SELECT, so a thread_cache row that exists but could not be read was treated
// as missing - the upsert leaves child_articles alone and the following UPDATE then overwrites
// it with only this batch's children, losing the ones already listed until a rescan.
//
// E6 (processNewsgroupBatch, PHASE 2): a threading failure is swallowed and the batch carries
// on into PHASE 3, so the history entries and the newsgroup counters of an already committed
// batch still happen. The retry loop that used to sit there was unreachable and is gone; these
// tests pin the behaviour that replaced it, and wave 1's
// TestFuBatchRetry2ThreadingFailureIsNotRetried still pins that the call does not retry.

// fuBatchFaultsUnreadableRow seeds a thread_cache row for threadRoot that exists but cannot be
// read: message_count holds text, so the fallback's Scan fails with a conversion error instead
// of sql.ErrNoRows. What it shares with the real fault - a write lock outliving the retry
// budget - is only that both are non-sql.ErrNoRows and so take the same branch: a lock error
// IS retryable, which is exactly why this helper's caller asserts the injected one is not.
// Nothing here covers the retry-then-exhaust-the-cap half of the real fault.
func fuBatchFaultsUnreadableRow(t *testing.T, gdb *GroupDB, threadRoot int64, children string) {
	t.Helper()
	if _, err := RetryableExec(gdb.DB, `INSERT INTO thread_cache
		(thread_root, root_date, message_count, child_articles, last_child_number, last_activity)
		VALUES (?, ?, 'notanumber', ?, ?, ?)`,
		threadRoot, fuThreadcountBase, children, threadRoot, fuThreadcountBase); err != nil {
		t.Fatalf("seed unreadable thread_cache row %d: %v", threadRoot, err)
	}
	// Sanity: the row has to be unreadable in exactly the way the fallback sees it, or the
	// test would pass for the wrong reason.
	var count int
	var listed string
	err := RetryableQueryRowScan(gdb.DB,
		"SELECT message_count, child_articles FROM thread_cache WHERE thread_root = ?",
		[]interface{}{threadRoot}, &count, &listed)
	if err == nil {
		t.Fatalf("thread_cache row %d reads back fine (message_count %d): the seed no longer produces an unreadable row", threadRoot, count)
	}
	if isRetryableError(err) {
		t.Fatalf("the seeded read failure is a retryable SQLite error (%v): RetryableTransactionExec would retry it for the whole cap instead of returning", err)
	}
}

// fuBatchFaultsChildren reads child_articles of threadRoot on its own, without touching
// message_count (which fuBatchFaultsUnreadableRow deliberately leaves unscannable).
func fuBatchFaultsChildren(t *testing.T, gdb *GroupDB, threadRoot int64) string {
	t.Helper()
	var children string
	if err := RetryableQueryRowScan(gdb.DB,
		"SELECT child_articles FROM thread_cache WHERE thread_root = ?",
		[]interface{}{threadRoot}, &children); err != nil {
		t.Fatalf("read child_articles of row %d: %v", threadRoot, err)
	}
	return children
}

// TestFuBatchFaultsThreadCacheUnreadableRowKeepsChildren: when batchUpdateThreadCache cannot
// read an existing thread_cache row, it must return the error instead of taking the
// "row missing" fallback. Returning is right because the whole closure runs inside
// RetryableTransactionExec, which rolls back and retries a transient lock; taking the fallback
// overwrote child_articles with only this batch's children (E7).
func TestFuBatchFaultsThreadCacheUnreadableRowKeepsChildren(t *testing.T) {
	db, gdb := fuThreadcountArticles(t, 3) // root 1, children 2..4
	sq := db.Batch
	if sq == nil {
		t.Fatal("db.Batch is nil")
	}
	const listed = "2,3,4"
	fuBatchFaultsUnreadableRow(t, gdb, 1, listed)

	err := sq.batchUpdateThreadCache(gdb, map[int64][]threadCacheUpdateData{
		1: {{childArticleNum: 5, childDate: fuThreadcountBase.Add(5 * time.Minute)}},
	})
	if err == nil {
		t.Error("batchUpdateThreadCache returned nil for a thread_cache row it could not read: the select-miss fallback still treats an unreadable row as missing (E7)")
	}

	if got := fuBatchFaultsChildren(t, gdb, 1); got != listed {
		t.Errorf("child_articles = %q after the failed update, want %q: the already listed children were overwritten with this batch's child and are lost until a rescan (E7)",
			got, listed)
	}
}

// TestFuBatchFaultsThreadCacheMissingRowStillInitializes is the other half of the E7 gate: a
// genuinely absent row (sql.ErrNoRows) must still take the fallback and be created, so the
// gate cannot be tightened into refusing the first reply of every thread whose root was
// processed without initializing the cache. The counts that fallback writes are covered by
// TestFuThreadcountOneReplyPerBatchCountsFromOne; this only pins that it is still entered.
func TestFuBatchFaultsThreadCacheMissingRowStillInitializes(t *testing.T) {
	db, gdb := fuThreadcountArticles(t, 1) // root 1, child 2, and no thread_cache row
	sq := db.Batch
	if sq == nil {
		t.Fatal("db.Batch is nil")
	}
	if err := sq.batchUpdateThreadCache(gdb, map[int64][]threadCacheUpdateData{
		1: {{childArticleNum: 2, childDate: fuThreadcountBase.Add(2 * time.Minute)}},
	}); err != nil {
		t.Fatalf("batchUpdateThreadCache on a thread with no cache row: %v: the sql.ErrNoRows fallback is no longer entered", err)
	}
	if got := fuBatchFaultsChildren(t, gdb, 1); got != "2" {
		t.Errorf("child_articles = %q, want \"2\"", got)
	}
}

// TestFuBatchFaultsThreadingFailureFallsThroughToPhase3: with the threading write blocked and
// the database live, processNewsgroupBatch must swallow the failure and carry on into PHASE 3,
// where the history adds and the newsgroup stats update live. That fall-through is the whole
// reason the swallow is the right stance (E6): the articles are committed by then, so
// propagating would retry and eventually abandon a batch, discarding the history entries and
// counters that do succeed. A threading failure must cost threading and nothing else.
//
// fuBatchIsolatedDB has no processor and no main database, so PHASE 3 cannot actually write
// either; what it can show is that control reached the stats block and the function ran to its
// end rather than returning out of PHASE 2.
func TestFuBatchFaultsThreadingFailureFallsThroughToPhase3(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	db := fuBatchIsolatedDB(t)
	const articles = 3
	group := fuBatchGroupWithTrigger(t, db, "fubatchfaults.phase3", "threads")
	task := fuBatchTask(t, db.Batch, group, articles)

	// Deliberately no shutdown: this is the live path. The wait is only a wedge detector -
	// nothing here retries, so the call returns in milliseconds.
	fuBatchRun(t, db, task, db.Batch.retryShutdownGrace()+fuBatchSlack, logs)
	out := logs.String()

	if !strings.Contains(out, "Failed to batch process thread roots") {
		t.Fatalf("the threads trigger never fired, so this test proves nothing. Last log lines:\n%s", logs.tail(12))
	}
	// PHASE 3 was reached: this line comes from its stats block.
	if want := fmt.Sprintf("cannot update newsgroup stats for '%s'", group); !strings.Contains(out, want) {
		t.Errorf("no %q in the log: the batch left PHASE 2 without reaching PHASE 3, so a threading failure now also costs the history entries and the newsgroup counters (E6). Last log lines:\n%s",
			want, logs.tail(12))
	}
	// ... and the function ran to its last line.
	if want := fmt.Sprintf("[BATCH-END] newsgroup '%s' processed articles: %d", group, articles); !strings.Contains(out, want) {
		t.Errorf("no %q in the log: processNewsgroupBatch returned early. Last log lines:\n%s", want, logs.tail(12))
	}
	// The retry2 loop's log promised a state that cannot occur, which is why it was deleted
	// along with the loop: nothing may claim history and stats were skipped.
	if bad := "committed without threading/history/stats"; strings.Contains(out, bad) {
		t.Errorf("the log still claims %q, but PHASE 3 runs after a threading failure. Last log lines:\n%s", bad, logs.tail(12))
	}

	// The premise the swallow rests on: PHASE 1 committed the articles and only threading
	// was lost.
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	var committed, threads int
	if err := RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM articles", nil, &committed); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if committed != articles {
		t.Errorf("articles committed = %d, want %d", committed, articles)
	}
	if err := RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM threads", nil, &threads); err != nil {
		t.Fatalf("count threads: %v", err)
	}
	if threads != 0 {
		t.Errorf("threads rows = %d, want 0: the trigger did not block the threading write", threads)
	}
}
