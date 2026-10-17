package database

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fl_nntpdb_commit_test.go covers the commit barrier of plan followup-leftovers (D-B,
// A-tools-5b): NoteCaptured/settle/CommitMark/WaitCommitted in db_batch_commit.go and the
// settle calls of the batch writer, plus the S2 retry path of processOverviewBatch (the
// insert committed, the follow-up SELECT failed, retry1 runs again).

// flNntpdbCounts returns the barrier counters of group.
func flNntpdbCounts(sq *SQ3batch, group string) (captured, settled, dropped uint64) {
	sq.commitMu.Lock()
	defer sq.commitMu.Unlock()
	c, ok := sq.commits[group]
	if !ok {
		return 0, 0, 0
	}
	return c.captured, c.settled, c.dropped
}

// flNntpdbNote counts n captures of the task's group, as ProcessArticle does per article.
func flNntpdbNote(sq *SQ3batch, task *BatchTasks, n int) {
	for i := 0; i < n; i++ {
		sq.NoteCaptured([]*string{task.Newsgroup})
	}
}

// flNntpdbWaitAsync runs WaitCommitted in a goroutine and returns its result channel.
func flNntpdbWaitAsync(sq *SQ3batch, m CommitMark, stop <-chan struct{}, timeout time.Duration) <-chan error {
	done := make(chan error, 1)
	go func() { done <- sq.WaitCommitted(m, stop, timeout) }()
	return done
}

// flNntpdbResult waits for the result of flNntpdbWaitAsync (hang guard testWait).
func flNntpdbResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(testWait):
		t.Fatalf("WaitCommitted did not return within %v", testWait)
		return nil
	}
}

// flNntpdbNotReturned checks that a waiter has not returned yet (a deliberate short window:
// it only shows the waiter blocks while articles are unsettled, never that it returns late).
func flNntpdbNotReturned(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("WaitCommitted returned %v %s", err, what)
	case <-time.After(20 * time.Millisecond):
	}
}

// TestFlNntpdbCommitBarrierCommitted: a waiter that starts before the batch runs blocks until
// the batch committed and then returns nil; the counters settle to captured == settled.
func TestFlNntpdbCommitBarrierCommitted(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	sq := db.Batch
	name := w0Name("flnntpdb.commit")
	const n = 4
	mark := sq.CommitMark(name)
	task := fuBatchTask(t, sq, name, n)
	flNntpdbNote(sq, task, n)

	done := flNntpdbWaitAsync(sq, mark, nil, testWait)
	// the waiter requests a flush (flushReq) while it waits; polled, hang guard testWait
	deadline := time.Now().Add(testWait)
	for !sq.flushReq.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("a waiting WaitCommitted did not request a flush (flushReq) within %v", testWait)
		}
		time.Sleep(time.Millisecond)
	}
	flNntpdbNotReturned(t, done, "before the batch ran")

	fuBatchRun(t, db, task, adfBatchWait, &fuBatchLogs{})
	if err := flNntpdbResult(t, done); err != nil {
		t.Fatalf("WaitCommitted after the commit: %v, want nil", err)
	}
	if c, s, d := flNntpdbCounts(sq, name); c != n || s != n || d != 0 {
		t.Errorf("counters captured=%d settled=%d dropped=%d, want %d/%d/0", c, s, d, n, n)
	}
	if got := len(adfBatchNums(t, db, name)); got != n {
		t.Errorf("%d articles stored, want %d", got, n)
	}
	// Nothing captured since: a new wait returns at once.
	if err := sq.WaitCommitted(sq.CommitMark(name), nil, testWait); err != nil {
		t.Errorf("WaitCommitted with nothing pending: %v", err)
	}
}

// TestFlNntpdbCommitBarrierClosedGroupDB: the group databases are shut down, getBatchGroupDB
// gives up and drops the batch: the waiter gets ErrBatchDropped.
func TestFlNntpdbCommitBarrierClosedGroupDB(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	db := fuBatchIsolatedDB(t)
	sq := db.Batch
	name := w0Name("flnntpdb.closed")
	const n = 3
	mark := sq.CommitMark(name)
	task := fuBatchTask(t, sq, name, n)
	flNntpdbNote(sq, task, n)
	if err := db.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	fuBatchRun(t, db, task, adfBatchWait, logs)
	err := sq.WaitCommitted(mark, nil, testWait)
	if !errors.Is(err, ErrBatchDropped) {
		t.Fatalf("WaitCommitted after a dropped batch: %v, want ErrBatchDropped. Last log lines:\n%s", err, logs.tail(12))
	}
	if c, s, d := flNntpdbCounts(sq, name); c != n || s != n || d != n {
		t.Errorf("counters captured=%d settled=%d dropped=%d, want %d/%d/%d", c, s, d, n, n, n)
	}
	// A mark taken after the drop does not report it again.
	if err := sq.WaitCommitted(sq.CommitMark(name), nil, testWait); err != nil {
		t.Errorf("WaitCommitted with a mark after the drop: %v, want nil", err)
	}
}

// TestFlNntpdbCommitBarrierInsertGraceDrop: an insert that keeps failing after shutdown began
// is dropped once the (shortened) retry grace has passed: the waiter gets ErrBatchDropped.
func TestFlNntpdbCommitBarrierInsertGraceDrop(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	db := fuBatchIsolatedDB(t)
	sq := db.Batch
	sq.retryGrace = 100 * time.Millisecond
	group := fuBatchGroupWithTrigger(t, db, "flnntpdb.grace", "articles")
	const n = 2
	mark := sq.CommitMark(group)
	task := fuBatchTask(t, sq, group, n)
	flNntpdbNote(sq, task, n)
	close(db.StopChan)

	fuBatchRun(t, db, task, sq.retryShutdownGrace()+fuBatchSlack, logs)
	if !strings.Contains(logs.String(), "insert still failing") {
		t.Fatalf("the batch was not dropped on the insert grace. Last log lines:\n%s", logs.tail(12))
	}
	if err := sq.WaitCommitted(mark, nil, testWait); !errors.Is(err, ErrBatchDropped) {
		t.Fatalf("WaitCommitted: %v, want ErrBatchDropped", err)
	}
}

// TestFlNntpdbCommitBarrierStop: the stop channel and the database's background stop channel
// end a wait for unsettled articles with ErrBatchWaitStopped.
func TestFlNntpdbCommitBarrierStop(t *testing.T) {
	t.Run("stop channel", func(t *testing.T) {
		db := fuBatchIsolatedDB(t)
		sq := db.Batch
		ptr := sq.GetNewsgroupPointer(w0Name("flnntpdb.stop"))
		mark := sq.CommitMark(*ptr)
		sq.NoteCaptured([]*string{ptr})
		stop := make(chan struct{})
		done := flNntpdbWaitAsync(sq, mark, stop, testWait)
		flNntpdbNotReturned(t, done, "with an unsettled article")
		close(stop)
		if err := flNntpdbResult(t, done); !errors.Is(err, ErrBatchWaitStopped) {
			t.Fatalf("WaitCommitted after stop: %v, want ErrBatchWaitStopped", err)
		}
	})
	t.Run("background stop", func(t *testing.T) {
		s := adfBatchNewStopDB(t)
		sq := s.db.Batch
		ptr := sq.GetNewsgroupPointer(w0Name("flnntpdb.bgstop"))
		mark := sq.CommitMark(*ptr)
		sq.NoteCaptured([]*string{ptr})
		done := flNntpdbWaitAsync(sq, mark, nil, testWait)
		flNntpdbNotReturned(t, done, "with an unsettled article")
		s.db.stopBackground()
		if err := flNntpdbResult(t, done); !errors.Is(err, ErrBatchWaitStopped) {
			t.Fatalf("WaitCommitted after the background stop: %v, want ErrBatchWaitStopped", err)
		}
	})
}

// TestFlNntpdbCommitBarrierTimeout: unsettled articles and a timeout give ErrBatchWaitTimeout.
func TestFlNntpdbCommitBarrierTimeout(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	sq := db.Batch
	ptr := sq.GetNewsgroupPointer(w0Name("flnntpdb.timeout"))
	mark := sq.CommitMark(*ptr)
	sq.NoteCaptured([]*string{ptr, ptr})
	sq.settle(*ptr, 1, false)
	// a deliberately short timeout: the assertion is the error, not the time
	if err := sq.WaitCommitted(mark, nil, 10*time.Millisecond); !errors.Is(err, ErrBatchWaitTimeout) {
		t.Fatalf("WaitCommitted with 1 of 2 articles settled: %v, want ErrBatchWaitTimeout", err)
	}
	sq.settle(*ptr, 1, false)
	if err := sq.WaitCommitted(mark, nil, 10*time.Millisecond); err != nil {
		t.Fatalf("WaitCommitted with both settled: %v", err)
	}
}

// TestFlNntpdbCommitBarrierSurvivesExpiry: ExpireCache removes idle tasks from TasksMap; the
// counters are not kept there, so an expiry between capture and commit loses no count.
func TestFlNntpdbCommitBarrierSurvivesExpiry(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	sq := db.Batch
	name := w0Name("flnntpdb.expiry")
	ptr := sq.GetNewsgroupPointer(name)
	mark := sq.CommitMark(name)
	sq.NoteCaptured([]*string{ptr})

	if expired := sq.expireIdleTasks(time.Now().Add(time.Hour)); expired < 1 {
		t.Fatalf("expireIdleTasks removed %d tasks, want the idle task of %s", expired, name)
	}
	sq.GMux.RLock()
	_, inMap := sq.TasksMap[name]
	sq.GMux.RUnlock()
	if inMap {
		t.Fatalf("task %s still in TasksMap after the expiry", name)
	}
	if c, s, _ := flNntpdbCounts(sq, name); c != 1 || s != 0 {
		t.Fatalf("counters after the expiry: captured=%d settled=%d, want 1/0", c, s)
	}
	if err := sq.WaitCommitted(mark, nil, 10*time.Millisecond); !errors.Is(err, ErrBatchWaitTimeout) {
		t.Fatalf("WaitCommitted with the capture unsettled: %v, want ErrBatchWaitTimeout (the expiry reset the count)", err)
	}

	task := fuBatchTask(t, sq, name, 1) // the recreated task (same interned pointer)
	if task.Newsgroup != ptr {
		t.Fatalf("recreated task has a new name pointer")
	}
	fuBatchRun(t, db, task, adfBatchWait, &fuBatchLogs{})
	if err := sq.WaitCommitted(mark, nil, testWait); err != nil {
		t.Fatalf("WaitCommitted after the commit: %v", err)
	}
}

// TestFlNntpdbCommitBarrierConcurrent: concurrent NoteCaptured, settle and WaitCommitted on a
// few groups (go test -race): every waiter returns nil once everything is settled and the
// counters add up.
func TestFlNntpdbCommitBarrierConcurrent(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	sq := db.Batch
	groups := []string{w0Name("flnntpdb.race"), w0Name("flnntpdb.race"), w0Name("flnntpdb.race")}
	ptrs := make([]*string, len(groups))
	marks := make([]CommitMark, len(groups))
	for i, g := range groups {
		ptrs[i] = sq.GetNewsgroupPointer(g)
		marks[i] = sq.CommitMark(g)
	}
	const workers, perWorker = 8, 200
	var noted, settled sync.WaitGroup
	var pending atomic.Int64
	for w := 0; w < workers; w++ {
		noted.Add(1)
		settled.Add(1)
		go func(w int) {
			defer settled.Done()
			ptr := ptrs[w%len(ptrs)]
			for i := 0; i < perWorker; i++ {
				sq.NoteCaptured([]*string{ptr})
				pending.Add(1)
			}
			noted.Done()
			for i := 0; i < perWorker; i += 10 {
				sq.settle(*ptr, 10, false)
			}
		}(w)
	}
	noted.Wait()
	results := make([]<-chan error, 0, len(groups)*2)
	for round := 0; round < 2; round++ {
		for i := range groups {
			results = append(results, flNntpdbWaitAsync(sq, marks[i], nil, testWait))
		}
	}
	settled.Wait()
	for _, done := range results {
		if err := flNntpdbResult(t, done); err != nil {
			t.Errorf("WaitCommitted: %v", err)
		}
	}
	var total uint64
	for _, g := range groups {
		c, s, d := flNntpdbCounts(sq, g)
		if c != s || d != 0 {
			t.Errorf("%s: captured=%d settled=%d dropped=%d, want captured == settled, 0 dropped", g, c, s, d)
		}
		total += c
	}
	if total != uint64(pending.Load()) {
		t.Errorf("captured %d in total, want %d", total, pending.Load())
	}
}

// TestFlNntpdbCommitBarrierSharedDB: on the shared database the running orchestrator flushes a
// batch below the size threshold and the waiter returns nil (flushReq or BatchInterval,
// whichever comes first: only the result is asserted).
func TestFlNntpdbCommitBarrierSharedDB(t *testing.T) {
	adfOptHierSkipLowEUID(t)
	db := w0DB(t)
	sq := db.Batch
	name := w0Name("flnntpdb.shared")
	const n = 3
	mark := sq.CommitMark(name)
	ptr := sq.GetNewsgroupPointer(name)
	// the capture path of ProcessArticle: count first, then hand over (BatchDivider runs)
	for i := 0; i < n; i++ {
		a := adfBatchArticle(&BatchTasks{Newsgroup: ptr}, adfBatchMsgID())
		a.NewsgroupsPtr = []*string{ptr}
		a.ProcessQueue = make(chan *string, 16)
		sq.NoteCaptured([]*string{ptr})
		sq.BatchCaptureOverviewForLater(ptr, a)
	}
	if err := sq.WaitCommitted(mark, nil, testWait); err != nil {
		t.Fatalf("WaitCommitted on the shared database: %v", err)
	}
	if got := len(adfBatchNums(t, db, name)); got != n {
		t.Errorf("%d articles stored, want %d", got, n)
	}
}

// TestFlNntpdbBatchRetrySelectAfterCommit (S2): the insert transaction committed, the SELECT of
// the article numbers failed: retry1 runs the batch again, the INSERT OR IGNORE is a no-op the
// second time, and the articles still get their numbers and count as inserted (not as
// duplicates), because the inserted set lives outside retry1.
func TestFlNntpdbBatchRetrySelectAfterCommit(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	db := fuBatchIsolatedDB(t)
	sq := db.Batch
	var calls atomic.Int32
	sq.testAfterInsertCommit = func() error {
		if calls.Add(1) == 1 {
			return fmt.Errorf("fl-nntpdb: injected select failure")
		}
		return nil
	}
	name := w0Name("flnntpdb.s2")
	const n = 3
	mark := sq.CommitMark(name)
	task := fuBatchTask(t, sq, name, n)
	flNntpdbNote(sq, task, n)

	fuBatchRun(t, db, task, adfBatchWait, logs)
	out := logs.String()
	if got := calls.Load(); got != 2 {
		t.Fatalf("hook ran %d times, want 2 (one failure, one retry). Last log lines:\n%s", got, logs.tail(12))
	}
	if !strings.Contains(out, "injected select failure") || !strings.Contains(out, "Failed1") {
		t.Errorf("the failed SELECT did not take the retry1 path. Last log lines:\n%s", logs.tail(12))
	}
	if strings.Contains(out, "duplicate message-ids ignored") {
		t.Errorf("the retry counted the committed rows as duplicates: the inserted set was lost. Last log lines:\n%s", logs.tail(12))
	}
	if got := adfBatchNums(t, db, name); !adfBatchEqual(got, []int64{1, 2, 3}) {
		t.Errorf("stored numbers %v, want [1 2 3]", got)
	}
	if err := sq.WaitCommitted(mark, nil, testWait); err != nil {
		t.Errorf("WaitCommitted: %v", err)
	}
	if c, s, d := flNntpdbCounts(sq, name); c != n || s != n || d != 0 {
		t.Errorf("counters captured=%d settled=%d dropped=%d, want %d/%d/0 (settled once, after the retry)", c, s, d, n, n)
	}
	if q := adfBatchQueued(sq); q != 0 {
		t.Errorf("queued = %d after the batch, want 0", q)
	}
}
