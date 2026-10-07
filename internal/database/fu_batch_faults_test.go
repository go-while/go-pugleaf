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

// fuBatchFaultsHealthyRow seeds a readable, self-consistent thread_cache row
// (message_count == 1 + len(children)) for threadRoot.
func fuBatchFaultsHealthyRow(t *testing.T, gdb *GroupDB, threadRoot int64, children string) {
	t.Helper()
	if _, err := RetryableExec(gdb.DB, `INSERT INTO thread_cache
		(thread_root, root_date, message_count, child_articles, last_child_number, last_activity)
		VALUES (?, ?, ?, ?, ?, ?)`,
		threadRoot, fuThreadcountBase, 1+threadCacheReplyCount(children), children,
		threadRoot, fuThreadcountBase); err != nil {
		t.Fatalf("seed thread_cache row %d: %v", threadRoot, err)
	}
}

// TestFuBatchFaultsThreadCacheUnreadableRowKeepsChildren: when batchUpdateThreadCache cannot
// read an existing thread_cache row, it must not take the "row missing" fallback - that
// fallback's upsert leaves child_articles alone and the following UPDATE then overwrites it
// with only this batch's children, losing the ones already listed until a rescan (E7).
//
// For a read failure no retry can fix - a corrupt value, here message_count holding text - the
// right answer is to skip that one root and carry on. Returning the error instead aborts the
// transaction of *every* root in the batch, and because the error is not retryable nothing
// retries it and every later batch repeats the abort, so a single corrupt row stops the whole
// group's thread cache from advancing. The batch therefore carries a second, healthy root whose
// update has to survive, which is the blast radius the first version of this test did not cover.
func TestFuBatchFaultsThreadCacheUnreadableRowKeepsChildren(t *testing.T) {
	db, gdb := fuThreadcountArticles(t, 12) // articles 1..13
	sq := db.Batch
	if sq == nil {
		t.Fatal("db.Batch is nil")
	}
	logs := fuBatchCaptureLog(t)

	const corruptListed = "11,12" // root 10, unreadable
	const healthyListed = "2,3"   // root 1, fine
	fuBatchFaultsUnreadableRow(t, gdb, 10, corruptListed)
	fuBatchFaultsHealthyRow(t, gdb, 1, healthyListed)

	if err := sq.batchUpdateThreadCache(gdb, map[int64][]threadCacheUpdateData{
		10: {{childArticleNum: 13, childDate: fuThreadcountBase.Add(13 * time.Minute)}},
		1:  {{childArticleNum: 4, childDate: fuThreadcountBase.Add(4 * time.Minute)}},
	}); err != nil {
		t.Errorf("batchUpdateThreadCache returned %v: one unreadable row that no retry can fix must cost only its own thread, not the whole batch's transaction", err)
	}

	// The corrupt row keeps its children: the fallback did not run on it (E7).
	if got := fuBatchFaultsChildren(t, gdb, 10); got != corruptListed {
		t.Errorf("child_articles of the unreadable root 10 = %q, want %q: the already listed children were overwritten with this batch's child and are lost until a rescan (E7)",
			got, corruptListed)
	}
	// ... and the healthy root in the same batch still got its update: no whole-batch abort.
	if got, want := fuBatchFaultsChildren(t, gdb, 1), healthyListed+",4"; got != want {
		t.Errorf("child_articles of the healthy root 1 = %q, want %q: the unreadable root rolled back the whole batch's transaction, so every other thread in it lost its update and - the error not being retryable - nothing retries and every later batch does the same",
			got, want)
	}
	if want := "thread_cache row for root 10 cannot be read"; !strings.Contains(logs.String(), want) {
		t.Errorf("the skipped root was not logged as %q. Last log lines:\n%s", want, logs.tail(12))
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

// fuBatchFaultsMemChildren reads one root's cached child_articles out of a memory thread cache,
// under its own lock, and reports whether the entry exists at all.
func fuBatchFaultsMemChildren(mem *MemCachedThreads, group string, threadRoot int64) (string, bool) {
	mem.mux.RLock()
	defer mem.mux.RUnlock()
	groupCache := mem.Groups[group]
	if groupCache == nil {
		return "", false
	}
	meta := groupCache.ThreadMeta[threadRoot]
	if meta == nil {
		return "", false
	}
	return meta.ChildArticles, true
}

// fuBatchFaultsAbortSecondUpdate installs a trigger that lets the first UPDATE on thread_cache
// through and aborts the second one, whichever roots those turn out to be. A counter table is
// what makes it independent of Go's random map iteration order: a plain per-root trigger would
// only reach the "one root already succeeded, then the transaction aborts" state in the runs
// where the map happened to yield the healthy root first.
//
// The abort is not a lock error, so RetryableTransactionExec does not retry it: the closure
// returns mid-loop exactly as it does when a lock error survives the retry cap, which is the
// one path that can leave committed-looking state behind in memory.
func fuBatchFaultsAbortSecondUpdate(t *testing.T, gdb *GroupDB) {
	t.Helper()
	for _, stmt := range []string{
		"CREATE TABLE fu_batch_faults_abort (n INTEGER)",
		"INSERT INTO fu_batch_faults_abort (n) VALUES (1)",
		`CREATE TRIGGER fu_batch_faults_abort_2nd AFTER UPDATE ON thread_cache BEGIN
			UPDATE fu_batch_faults_abort SET n = n - 1;
			SELECT RAISE(ABORT, 'fu-batch-faults: second thread_cache update blocked')
				WHERE (SELECT n FROM fu_batch_faults_abort) < 0;
		END`,
	} {
		if _, err := RetryableExec(gdb.DB, stmt); err != nil {
			t.Fatalf("install the abort-on-second-update trigger (%q): %v", stmt, err)
		}
	}
}

// TestFuBatchFaultsAbortedBatchDoesNotPoisonMemoryCache: batchUpdateThreadCache must not tell
// MemThreadCache about children its transaction has not committed.
//
// The memory copy is authoritative for the group listing - wave 2's GetCachedThreadsFromMemory
// renders MessageCount as threadCacheReplyCount(meta.ChildArticles) - while the thread page
// paginates the disk row. So a root pushed into memory inside the transaction and then rolled
// back by a later root's failure makes the listing promise replies the page cannot serve: the
// disagreement E2 was raised to kill, re-created from the writer's side, and invisible to
// wave 1's mismatch warning because the disk row it leaves behind is self-consistent. It lasts
// until the memory window expires or a refresh runs.
//
// The second half of the test is the positive control: once the batch does commit, the memory
// cache must actually learn the new children, or holding the push back would simply have
// dropped it.
func TestFuBatchFaultsAbortedBatchDoesNotPoisonMemoryCache(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	// Isolated memory cache on an isolated Database: nothing else reads or writes it, and no
	// CleanCron goroutine is started (NewMemCachedThreads would start one).
	mem := &MemCachedThreads{Groups: make(map[string]*MemGroupThreadCache, 4)}
	db.MemThreadCache = mem

	group := w0Name("fubatchfaults.mem")
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	t.Cleanup(func() { gdb.Return() })

	const rootAChildren = "3,4"
	const rootBChildren = "5,6"
	fuBatchFaultsHealthyRow(t, gdb, 1, rootAChildren)
	fuBatchFaultsHealthyRow(t, gdb, 2, rootBChildren)
	fuBatchFaultsAbortSecondUpdate(t, gdb)

	disk := map[int64]string{1: rootAChildren, 2: rootBChildren}
	batch := map[int64][]threadCacheUpdateData{
		1: {{childArticleNum: 7, childDate: fuThreadcountBase.Add(7 * time.Minute)}},
		2: {{childArticleNum: 8, childDate: fuThreadcountBase.Add(8 * time.Minute)}},
	}

	err = db.Batch.batchUpdateThreadCache(gdb, batch)
	if err == nil {
		t.Fatal("batchUpdateThreadCache returned nil although the second thread_cache UPDATE was aborted: the trigger did not fire, so this test proves nothing")
	}
	if !strings.Contains(err.Error(), "second thread_cache update blocked") {
		t.Fatalf("batchUpdateThreadCache failed with %v, not the injected abort: the batch never reached a second UPDATE, so it never got into the state this test is about", err)
	}

	// Nothing committed, so no root may have advanced on disk ...
	for root, want := range disk {
		if got := fuBatchFaultsChildren(t, gdb, root); got != want {
			t.Errorf("child_articles of root %d = %q after the aborted batch, want %q", root, got, want)
		}
	}
	// ... and the memory cache may not claim otherwise for any of them.
	for root, onDisk := range disk {
		if got, ok := fuBatchFaultsMemChildren(mem, group, root); ok && got != onDisk {
			t.Errorf("MemThreadCache lists child_articles %q for root %d while the disk row reads %q: the rolled-back children were published to memory, so the group listing now serves more replies than the thread page can paginate",
				got, root, onDisk)
		}
	}

	// Positive control: with the trigger gone the same batch commits, and now the memory cache
	// has to learn the new children.
	if _, err := RetryableExec(gdb.DB, "DROP TRIGGER fu_batch_faults_abort_2nd"); err != nil {
		t.Fatalf("drop the abort trigger: %v", err)
	}
	if err := db.Batch.batchUpdateThreadCache(gdb, batch); err != nil {
		t.Fatalf("batchUpdateThreadCache after dropping the trigger: %v", err)
	}
	for root, before := range disk {
		want := before + "," + map[int64]string{1: "7", 2: "8"}[root]
		if got := fuBatchFaultsChildren(t, gdb, root); got != want {
			t.Errorf("child_articles of root %d = %q after the committed batch, want %q", root, got, want)
		}
		got, ok := fuBatchFaultsMemChildren(mem, group, root)
		if !ok {
			t.Errorf("MemThreadCache has no entry for root %d after a committed batch: holding the push back until the commit dropped it instead of deferring it", root)
			continue
		}
		if got != want {
			t.Errorf("MemThreadCache lists child_articles %q for root %d after a committed batch, want %q (the disk row)", got, root, want)
		}
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
