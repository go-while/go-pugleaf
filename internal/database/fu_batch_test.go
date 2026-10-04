package database

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fu_batch_test.go covers C1 of the web-db-followups plan: the shutdown bounds of the two
// retry loops of processNewsgroupBatch (db_batch.go, labels retry1 and retry2).
//
// processNewsgroupBatch runs inside db.WG (StartOrch -> processAllPendingBatches) and every
// tool closes db.StopChan, waits for db.WG and only then closes the databases. A retry loop
// that keeps going after shutdown has begun therefore blocks db.WG.Wait() for good and the
// process has to be SIGKILLed. batchShutdownClock has its own unit test
// (TestLo2WriterBatchShutdownClock); these tests cover the two call sites.

// fuBatchSlack is how much longer than batchShutdownGrace a bounded loop may take before the
// test calls it unbounded. One extra round is a group database reopen plus one insert attempt
// plus the loop's own one second sleep, so this is generous on purpose.
const fuBatchSlack = time.Minute

// fuBatchLogs is a concurrency-safe log sink: the batch writer logs from the goroutine under
// test while the shared test database logs from its own background goroutines.
type fuBatchLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *fuBatchLogs) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *fuBatchLogs) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// tail returns the last n captured lines (the retry loops log once per attempt, so a full dump
// would bury the interesting part).
func (b *fuBatchLogs) tail(n int) string {
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// fuBatchCaptureLog sends log output to a buffer for the duration of one test. It mutates the
// global logger, so no test using it may run with t.Parallel.
func fuBatchCaptureLog(t *testing.T) *fuBatchLogs {
	t.Helper()
	b := &fuBatchLogs{}
	old := log.Writer()
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(old) })
	return b
}

// fuBatchIsolatedDB returns a Database with its own data root, its own group database map and
// its own StopChan, so a test can report shutdown (IsDBshutdown) without shutting down the
// shared test database. Only dbconfig is copied from it.
//
// Note GetGroupDB gates on groupDBsShutdown, not on StopChan, so group databases keep opening
// after StopChan is closed - which is exactly the state the retry loops are bounded for.
func fuBatchIsolatedDB(t *testing.T) *Database {
	t.Helper()
	cfg := *w0DB(t).dbconfig
	cfg.DataDir = t.TempDir()
	db := &Database{
		dbconfig: &cfg,
		groupDB:  make(map[string]*GroupDB),
		StopChan: make(chan struct{}),
	}
	db.Batch = NewSQ3batch(db)
	t.Cleanup(func() {
		if err := db.Shutdown(); err != nil {
			t.Logf("fuBatch: Shutdown: %v", err)
		}
	})
	return db
}

// fuBatchGroupWithTrigger creates the group database of a fresh newsgroup and installs a
// BEFORE INSERT trigger on table, so every insert into it aborts deterministically.
func fuBatchGroupWithTrigger(t *testing.T, db *Database, prefix, table string) string {
	t.Helper()
	name := w0Name(prefix)
	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", name, err)
	}
	_, err = RetryableExec(groupDB.DB, fmt.Sprintf(
		"CREATE TRIGGER fu_batch_block_%s BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'fu-batch: %s insert blocked'); END",
		table, table, table))
	groupDB.Return()
	if err != nil {
		t.Fatalf("create trigger on %s: %v", table, err)
	}
	return name
}

// fuBatchTask queues n articles for newsgroup the way the batch writer's capture path leaves
// them for processNewsgroupBatch: in task.BATCHchan, counted in sq.queued.
func fuBatchTask(t *testing.T, sq *SQ3batch, newsgroup string, n int) *BatchTasks {
	t.Helper()
	task := sq.GetOrCreateTasksMapKey(newsgroup)
	task.Mux.Lock()
	task.BATCHchan = make(chan *models.Article, n)
	for i := 0; i < n; i++ {
		task.BATCHchan <- &models.Article{
			MessageID:     fmt.Sprintf("<fubatch-%d@test.invalid>", w0Seq.Add(1)),
			Subject:       "fu-batch",
			FromHeader:    "fu <fu@test.invalid>",
			DateSent:      time.Now().UTC(),
			DateString:    time.Now().UTC().Format(time.RFC1123Z),
			ArticleNums:   make(map[*string]int64),
			NewsgroupsPtr: []*string{task.Newsgroup},
			IsThrRoot:     true,
		}
	}
	task.Mux.Unlock()
	sq.GMux.Lock()
	sq.queued += n
	sq.GMux.Unlock()
	return task
}

// fuBatchRun calls processNewsgroupBatch like processAllPendingBatches does (holding a
// LimitChan token, which its defer returns) and reports how long it took to return. A loop
// that lost its bound never returns, so the wait is bounded: db.Shutdown makes the group
// database reacquire fail fast, which unwedges the leaked goroutine before the test fails.
func fuBatchRun(t *testing.T, db *Database, task *BatchTasks, wait time.Duration, logs *fuBatchLogs) time.Duration {
	t.Helper()
	db.Batch.LimitChan <- struct{}{}
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		db.Batch.processNewsgroupBatch(task)
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(wait):
	}
	if err := db.Shutdown(); err != nil {
		t.Logf("fuBatch: Shutdown to unwedge the batch goroutine: %v", err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Log("fuBatch: the batch goroutine is still running and is leaked")
	}
	t.Fatalf("processNewsgroupBatch did not return within %v after shutdown began: the retry loop is unbounded and db.WG.Wait() would block for good. Last log lines:\n%s",
		wait, logs.tail(12))
	return 0
}

// TestFuBatchRetry1DropsBatchAfterShutdownGrace: with a failing insert and shutdown under way,
// the retry1 loop drops the batch after batchShutdownGrace instead of retrying for good (C1).
//
// The test needs batchShutdownGrace (2 minutes) of wall-clock time because the bound is
// wall-clock and neither call site takes the grace as a parameter the way
// updateNewsgroupStatsWithRetry does. It is almost all sleeping: about 120 rounds of
// reopen-insert-fail-sleep(1s).
func TestFuBatchRetry1DropsBatchAfterShutdownGrace(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	db := fuBatchIsolatedDB(t)
	const articles = 3
	group := fuBatchGroupWithTrigger(t, db, "fubatch.retry1", "articles")
	task := fuBatchTask(t, db.Batch, group, articles)

	close(db.StopChan)
	if !db.IsDBshutdown() {
		t.Fatal("IsDBshutdown() is false after closing StopChan: the test would hang, not bound anything")
	}

	took := fuBatchRun(t, db, task, batchShutdownGrace+fuBatchSlack, logs)

	if took < batchShutdownGrace {
		t.Errorf("processNewsgroupBatch returned after %v, before the %v grace had passed: a failing insert must keep retrying until then, the drained articles are only in memory",
			took, batchShutdownGrace)
	}
	want := fmt.Sprintf("dropping %d articles for '%s': insert still failing", articles, group)
	if !strings.Contains(logs.String(), want) {
		t.Errorf("the drop was not logged as %q. Last log lines:\n%s", want, logs.tail(12))
	}
}

// TestFuBatchRetry2ThreadingFailureIsNotRetried pins the premise the retry2 bound rests on.
//
// The plan (C1) asks for the retry1 shape with a trigger on the threading write, but
// batchProcessThreading logs the failures of batchProcessThreadRoots/batchProcessReplies and
// returns nil regardless ("Continue processing - don't fail the whole batch"), so the error
// branch of retry2 - and with it threadClock - is unreachable today: a blocked threading write
// commits the articles and returns at once instead of retrying.
//
// This test keeps that premise honest: it fails the day batchProcessThreading starts
// propagating errors, which is the day the retry2 bound needs the same test as retry1.
func TestFuBatchRetry2ThreadingFailureIsNotRetried(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	db := fuBatchIsolatedDB(t)
	const articles = 3
	group := fuBatchGroupWithTrigger(t, db, "fubatch.retry2", "threads")
	task := fuBatchTask(t, db.Batch, group, articles)

	close(db.StopChan)
	// Wait as long as the retry1 test does: if batchProcessThreading ever propagates again,
	// this call retries for batchShutdownGrace instead of returning at once, and the timing
	// below names what changed. Today it returns in milliseconds.
	took := fuBatchRun(t, db, task, batchShutdownGrace+fuBatchSlack, logs)
	if took > 30*time.Second {
		t.Fatalf("processNewsgroupBatch retried the blocked threading write for %v: batchProcessThreading no longer swallows the failure, so the error branch of retry2 is live and its bound (threadClock) now needs the same bounded-drop test as retry1 - it held this time, the call returned inside the %v grace",
			took, batchShutdownGrace)
	}

	if !strings.Contains(logs.String(), "Failed to batch process thread roots") {
		t.Fatalf("the threads trigger never fired, so this test proves nothing. Last log lines:\n%s", logs.tail(12))
	}

	// Phase 1 committed the articles: that is why giving up in retry2 only costs threading.
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	var committed int
	if err := RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM articles", nil, &committed); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if committed != articles {
		t.Errorf("articles committed = %d, want %d", committed, articles)
	}
	var threads int
	if err := RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM threads", nil, &threads); err != nil {
		t.Fatalf("count threads: %v", err)
	}
	if threads != 0 {
		t.Errorf("threads rows = %d, want 0: the trigger did not block the threading write", threads)
	}

	// The premise, called directly so the failure message lands on the right function.
	roots := []*models.Article{{
		MessageID:   fmt.Sprintf("<fubatch-direct-%d@test.invalid>", w0Seq.Add(1)),
		IsThrRoot:   true,
		ArticleNums: map[*string]int64{task.Newsgroup: 1},
	}}
	if err := db.Batch.batchProcessThreading(task.Newsgroup, roots, groupDB); err != nil {
		t.Fatalf("batchProcessThreading returns %v instead of swallowing the failure of the threading write: the error branch of retry2 in processNewsgroupBatch is reachable again and its bound (threadClock) now needs the same bounded-drop test as retry1", err)
	}
}
