package database

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// adf_batch_test.go covers slice adf-db-batch of .claude/plans/wip/audit-delta-fixes.md:
// monotonic article numbers (INT-5, migration 0010), the per-article queued counter and its
// brake (A-db-9), idle-task expiry that spares busy tasks (A-db-5) and the batch loops that
// stop on Shutdown (OPS-1d).

const adfBatchSeqMigration = "0010_single_article_num_seq.sql"

// adfBatchWait bounds every fuBatchRun of these tests (a successful batch takes milliseconds).
const adfBatchWait = 60 * time.Second

// adfBatchNums returns the article numbers stored in the group database of name, ascending.
func adfBatchNums(t *testing.T, db *Database, name string) []int64 {
	t.Helper()
	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", name, err)
	}
	defer groupDB.Return()
	rows, err := RetryableQuery(groupDB.DB, "SELECT article_num FROM articles ORDER BY article_num")
	if err != nil {
		t.Fatalf("select article_num: %v", err)
	}
	defer rows.Close()
	var nums []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan article_num: %v", err)
		}
		nums = append(nums, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return nums
}

// adfBatchLastNum reads article_num_seq.last_num of the group database of name.
func adfBatchLastNum(t *testing.T, db *Database, name string) int64 {
	t.Helper()
	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", name, err)
	}
	defer groupDB.Return()
	var n int64
	if err := RetryableQueryRowScan(groupDB.DB, "SELECT last_num FROM article_num_seq WHERE id = 1", nil, &n); err != nil {
		t.Fatalf("read article_num_seq: %v", err)
	}
	return n
}

// adfBatchExec runs one statement on the group database of name.
func adfBatchExec(t *testing.T, db *Database, name, query string, args ...interface{}) {
	t.Helper()
	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", name, err)
	}
	defer groupDB.Return()
	if _, err := RetryableExec(groupDB.DB, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func adfBatchEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// adfBatchArticle builds an article for task's group as the capture path leaves it.
func adfBatchArticle(task *BatchTasks, messageID string) *models.Article {
	now := time.Now().UTC()
	return &models.Article{
		MessageID:     messageID,
		Subject:       "adf-batch",
		FromHeader:    "adf <adf@test.invalid>",
		DateSent:      now,
		DateString:    now.Format(time.RFC1123Z),
		ArticleNums:   make(map[*string]int64),
		NewsgroupsPtr: []*string{task.Newsgroup},
		IsThrRoot:     true,
	}
}

// adfBatchQueue puts arts in task.BATCHchan and counts them in sq.queued, like fuBatchTask.
func adfBatchQueue(sq *SQ3batch, task *BatchTasks, arts ...*models.Article) {
	task.Mux.Lock()
	task.BATCHchan = make(chan *models.Article, len(arts))
	for _, a := range arts {
		task.BATCHchan <- a
	}
	task.Mux.Unlock()
	sq.GMux.Lock()
	sq.queued += len(arts)
	sq.GMux.Unlock()
}

func adfBatchMsgID() string {
	return fmt.Sprintf("<adf-batch-%d@test.invalid>", w0Seq.Add(1))
}

func adfBatchQueued(sq *SQ3batch) int {
	sq.GMux.RLock()
	defer sq.GMux.RUnlock()
	return sq.queued
}

// adfBatchStopDB is an isolated batch database with its own background stop channel, so a
// test can run BatchDivider/ExpireCache on it and stop them. The cleanup closes the channel,
// waits for the started loops and clears bgStop again before fuBatchIsolatedDB's Shutdown: a
// Database with bgStop gives up the process-wide INIT, which belongs to the shared test DB.
type adfBatchStopDB struct {
	db *Database
	wg sync.WaitGroup
}

func adfBatchNewStopDB(t *testing.T) *adfBatchStopDB {
	t.Helper()
	s := &adfBatchStopDB{db: fuBatchIsolatedDB(t)}
	s.db.bgStop = make(chan struct{})
	t.Cleanup(func() {
		s.db.stopBackground()
		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("batch loops did not return within 5s after the stop channel was closed")
		}
		s.db.bgStop = nil
	})
	return s
}

// start runs fn in a goroutine tracked by the cleanup.
func (s *adfBatchStopDB) start(fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

// TestAdfBatchNumbersNotReused: after the highest-numbered articles are deleted, the batch
// writer and InsertOverview hand out new numbers instead of reusing the deleted ones (INT-5).
func TestAdfBatchNumbersNotReused(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	logs := &fuBatchLogs{}
	name := w0Name("adfbatch.reuse")

	fuBatchRun(t, db, fuBatchTask(t, db.Batch, name, 2), adfBatchWait, logs)
	if got := adfBatchNums(t, db, name); !adfBatchEqual(got, []int64{1, 2}) {
		t.Fatalf("numbers after the first batch = %v, want [1 2]", got)
	}

	adfBatchExec(t, db, name, "DELETE FROM articles")
	fuBatchRun(t, db, fuBatchTask(t, db.Batch, name, 1), adfBatchWait, logs)
	if got := adfBatchNums(t, db, name); !adfBatchEqual(got, []int64{3}) {
		t.Fatalf("numbers after delete + batch = %v, want [3]: a deleted number was reused", got)
	}
	if got := adfBatchLastNum(t, db, name); got != 3 {
		t.Errorf("article_num_seq.last_num = %d, want 3", got)
	}

	// InsertOverview (query_InsertOverview) takes the next number from the same table
	adfBatchExec(t, db, name, "DELETE FROM articles")
	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	num, err := db.InsertOverview(groupDB, &models.Overview{
		Subject: "adf-batch", FromHeader: "adf <adf@test.invalid>", DateSent: time.Now(),
		DateString: "x", MessageID: adfBatchMsgID(), Downloaded: 1,
	})
	groupDB.Return()
	if err != nil {
		t.Fatalf("InsertOverview: %v", err)
	}
	if num != 4 {
		t.Errorf("InsertOverview after delete got number %d, want 4", num)
	}
	if got := adfBatchQueued(db.Batch); got != 0 {
		t.Errorf("queued = %d after the batches, want 0", got)
	}
}

// TestAdfBatchSeqSeededFromExisting: the migration seeds last_num from the highest stored
// number and installs a trigger that the runner executes as a whole (BEGIN ... END).
func TestAdfBatchSeqSeededFromExisting(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	name := w0Name("adfbatch.seed")
	for i := 1; i <= 5; i++ {
		adfBatchExec(t, db, name, "INSERT INTO articles (article_num, message_id) VALUES (?, ?)", i, adfBatchMsgID())
	}
	if got := adfBatchLastNum(t, db, name); got != 5 {
		t.Fatalf("trigger: last_num = %d after raw inserts 1..5, want 5", got)
	}
	// back to a group database from before the migration
	adfBatchExec(t, db, name, "DROP TRIGGER articles_num_seq_ai")
	adfBatchExec(t, db, name, "DROP TABLE article_num_seq")
	adfBatchExec(t, db, name, "DELETE FROM schema_migrations WHERE filename = ? AND db_type = 'group'", adfBatchSeqMigration)

	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	err = db.migrateGroupDB(groupDB, false)
	groupDB.Return()
	if err != nil {
		t.Fatalf("migrateGroupDB: %v", err)
	}
	if got := adfBatchLastNum(t, db, name); got != 5 {
		t.Fatalf("seeded last_num = %d, want 5", got)
	}

	// the trigger is back and follows an explicit number; the next batch number continues
	adfBatchExec(t, db, name, "INSERT INTO articles (article_num, message_id) VALUES (9, ?)", adfBatchMsgID())
	adfBatchExec(t, db, name, "DELETE FROM articles WHERE article_num = 9")
	fuBatchRun(t, db, fuBatchTask(t, db.Batch, name, 1), adfBatchWait, &fuBatchLogs{})
	if got := adfBatchNums(t, db, name); !adfBatchEqual(got, []int64{1, 2, 3, 4, 5, 10}) {
		t.Errorf("numbers = %v, want [1 2 3 4 5 10]", got)
	}
}

// TestAdfBatchDuplicateConsumesNoNumber: an ignored duplicate message-id fires no trigger, so
// the next inserted article gets the next number without a gap.
func TestAdfBatchDuplicateConsumesNoNumber(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	name := w0Name("adfbatch.dupe")
	task := db.Batch.GetOrCreateTasksMapKey(name)
	first := adfBatchMsgID()

	adfBatchQueue(db.Batch, task, adfBatchArticle(task, first))
	fuBatchRun(t, db, task, adfBatchWait, &fuBatchLogs{})
	adfBatchQueue(db.Batch, task, adfBatchArticle(task, first), adfBatchArticle(task, adfBatchMsgID()))
	fuBatchRun(t, db, task, adfBatchWait, &fuBatchLogs{})

	if got := adfBatchNums(t, db, name); !adfBatchEqual(got, []int64{1, 2}) {
		t.Errorf("numbers = %v, want [1 2]: the duplicate consumed a number", got)
	}
	if got := adfBatchLastNum(t, db, name); got != 2 {
		t.Errorf("last_num = %d, want 2", got)
	}
}

// TestAdfBatchQueuedNeverNegative: articles that go through the capture path are counted one
// by one when BatchDivider routes them, so the counter matches what is queued and returns to 0
// after the batch, never below (A-db-9).
func TestAdfBatchQueuedNeverNegative(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	s := adfBatchNewStopDB(t)
	sq := s.db.Batch
	s.start(sq.BatchDivider)

	name := w0Name("adfbatch.queued")
	ptr := sq.GetNewsgroupPointer(name)
	const n = 5
	for i := 0; i < n; i++ {
		a := adfBatchArticle(&BatchTasks{Newsgroup: ptr}, adfBatchMsgID())
		a.NewsgroupsPtr = nil
		a.ProcessQueue = make(chan *string, 16)
		sq.BatchCaptureOverviewForLater(ptr, a)
	}
	task := sq.GetOrCreateTasksMapKey(name)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if q := adfBatchQueued(sq); q < 0 {
			t.Fatalf("queued = %d while capturing", q)
		}
		task.Mux.RLock()
		queuedInChan := len(task.BATCHchan)
		task.Mux.RUnlock()
		if queuedInChan == n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("BatchDivider routed %d of %d articles within 10s", queuedInChan, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if q := adfBatchQueued(sq); q != n {
		t.Fatalf("queued = %d with %d articles in BATCHchan, want %d (counted per article)", q, n, n)
	}
	fuBatchRun(t, s.db, task, adfBatchWait, logs)
	if q := adfBatchQueued(sq); q != 0 {
		t.Errorf("queued = %d after the batch, want 0", q)
	}
	if got := len(adfBatchNums(t, s.db, name)); got != n {
		t.Errorf("%d articles stored, want %d", got, n)
	}
	if strings.Contains(logs.String(), "went negative") {
		t.Errorf("negative counter warning logged:\n%s", logs.tail(12))
	}
}

// TestAdfBatchDividerBrakeEngages: with MaxQueued 16 the divider stops routing at 80 % (12)
// and resumes once a batch brought the live count down to 20 % or less.
func TestAdfBatchDividerBrakeEngages(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	s := adfBatchNewStopDB(t)
	sq := s.db.Batch
	sq.maxQueued = 16 // set before the divider starts: it reads it once
	s.start(sq.BatchDivider)

	name := w0Name("adfbatch.brake")
	ptr := sq.GetNewsgroupPointer(name)
	const n = 20
	s.start(func() {
		for i := 0; i < n; i++ {
			a := adfBatchArticle(&BatchTasks{Newsgroup: ptr}, adfBatchMsgID())
			a.NewsgroupsPtr = nil
			a.ProcessQueue = make(chan *string, 16)
			sq.BatchCaptureOverviewForLater(ptr, a)
		}
	})
	task := sq.GetOrCreateTasksMapKey(name)
	inChan := func() int {
		task.Mux.RLock()
		defer task.Mux.RUnlock()
		return len(task.BATCHchan)
	}
	waitFor := func(want int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for inChan() != want {
			if time.Now().After(deadline) {
				t.Fatalf("BATCHchan holds %d articles, want %d. Last log lines:\n%s", inChan(), want, logs.tail(12))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor(12)
	time.Sleep(80 * time.Millisecond) // the divider polls every 16 ms: it must stay braked
	if got := inChan(); got != 12 {
		t.Fatalf("BATCHchan holds %d articles while braked, want 12", got)
	}
	if !strings.Contains(logs.String(), "MaxQueued reached") {
		t.Errorf("brake not logged. Last log lines:\n%s", logs.tail(12))
	}
	fuBatchRun(t, s.db, task, adfBatchWait, logs) // drains 12, queued 12 -> 0
	waitFor(n - 12)
	fuBatchRun(t, s.db, task, adfBatchWait, logs)
	if q := adfBatchQueued(sq); q != 0 {
		t.Errorf("queued = %d after both batches, want 0", q)
	}
	if got := len(adfBatchNums(t, s.db, name)); got != n {
		t.Errorf("%d articles stored, want %d", got, n)
	}
}

// TestAdfBatchExpireSkipsBusyTask: idle-task expiry removes only expired tasks without work;
// GetNewsgroupPointer renews a task's expiry on a hit (A-db-5).
func TestAdfBatchExpireSkipsBusyTask(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	sq := db.Batch
	past := time.Now().Add(-time.Minute)
	mk := func(prefix string, mod func(*BatchTasks)) string {
		name := w0Name(prefix)
		task := sq.GetOrCreateTasksMapKey(name)
		task.Mux.Lock()
		task.Expires = past
		if mod != nil {
			mod(task)
		}
		task.Mux.Unlock()
		return name
	}
	idle := mk("adfbatch.idle", nil)
	busy := mk("adfbatch.busy", func(task *BatchTasks) { task.BATCHprocessing = true })
	queued := mk("adfbatch.inchan", func(task *BatchTasks) {
		task.BATCHchan = make(chan *models.Article, 1)
		task.BATCHchan <- &models.Article{}
	})
	fresh := mk("adfbatch.fresh", func(task *BatchTasks) { task.Expires = time.Now().Add(time.Minute) })
	read := mk("adfbatch.read", nil)
	readPtr := sq.GetNewsgroupPointer(read) // a hit renews the expiry

	if got := sq.expireIdleTasks(time.Now()); got != 1 {
		t.Errorf("expireIdleTasks removed %d tasks, want 1", got)
	}
	sq.GMux.RLock()
	defer sq.GMux.RUnlock()
	if _, ok := sq.TasksMap[idle]; ok {
		t.Error("expired idle task was kept")
	}
	for _, name := range []string{busy, queued, fresh, read} {
		if _, ok := sq.TasksMap[name]; !ok {
			t.Errorf("task %s was expired although it is busy, queued, fresh or just read", name)
		}
	}
	if got := sq.TasksMap[read]; got == nil || got.Newsgroup != readPtr {
		t.Error("GetNewsgroupPointer's pointer is no longer the task's")
	}
}

// TestAdfBatchLoopsStopOnShutdown: BatchDivider and ExpireCache return once the background
// stop channel is closed, and a capture after that returns instead of parking (OPS-1d).
func TestAdfBatchLoopsStopOnShutdown(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	db := fuBatchIsolatedDB(t)
	db.bgStop = make(chan struct{})
	defer func() { db.bgStop = nil }() // before fuBatchIsolatedDB's Shutdown: INIT is the shared DB's
	sq := db.Batch

	divider, expire := make(chan struct{}), make(chan struct{})
	go func() { defer close(divider); sq.BatchDivider() }()
	go func() { defer close(expire); sq.ExpireCache() }()
	db.stopBackground()
	for what, done := range map[string]chan struct{}{"BatchDivider": divider, "ExpireCache": expire} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not return within 2s after the stop channel was closed", what)
		}
	}

	// dividerChan holds 128: at least the rest is dropped, and no call blocks
	ptr := sq.GetNewsgroupPointer(w0Name("adfbatch.stop"))
	captured := make(chan struct{})
	go func() {
		defer close(captured)
		for i := 0; i < 200; i++ {
			sq.BatchCaptureOverviewForLater(ptr, &models.Article{ProcessQueue: make(chan *string, 1)})
		}
	}()
	select {
	case <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("BatchCaptureOverviewForLater blocked after Shutdown")
	}
	if c := strings.Count(logs.String(), "[BATCH] capture after Shutdown dropped"); c != 1 {
		t.Errorf("drop logged %d times, want once", c)
	}
}

// TestAdfBatchResetRestartsNumbering: ResetNewsgroupData restarts the numbering and removes the
// group's main-DB spam rows, user spam flags and cached articles, which would otherwise be
// inherited by the new articles 1, 2, ... (INT-5).
func TestAdfBatchResetRestartsNumbering(t *testing.T) {
	db := w0DB(t)
	name := fuQueriesGroup(t, true)
	other := fuQueriesGroup(t, true)
	id, otherID := fuQueriesNewsgroupID(t, name), fuQueriesNewsgroupID(t, other)
	user := fuQueriesUser(t)

	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.InsertOverview(groupDB, &models.Overview{
			Subject: "adf-batch", FromHeader: "adf <adf@test.invalid>", DateSent: time.Now(),
			DateString: "x", MessageID: adfBatchMsgID(), Downloaded: 1,
		}); err != nil {
			groupDB.Return()
			t.Fatalf("InsertOverview: %v", err)
		}
	}
	groupDB.Return()
	for _, g := range []int64{id, otherID} {
		if _, err := RetryableExec(db.GetMainDB(), "INSERT INTO spam (newsgroup_id, article_num) VALUES (?, 2)", g); err != nil {
			t.Fatalf("insert spam: %v", err)
		}
		fuQueriesFlagSpam(t, user.ID, g, 2)
	}
	if db.ArticleCache == nil {
		t.Fatal("the shared test DB has no article cache")
	}
	db.ArticleCache.Put(name, 2, &models.Article{MessageID: "<adf-batch-cached@test.invalid>"})

	if err := db.ResetNewsgroupData(name); err != nil {
		t.Fatalf("ResetNewsgroupData: %v", err)
	}
	if got := adfBatchLastNum(t, db, name); got != 0 {
		t.Errorf("last_num = %d after reset, want 0", got)
	}
	if _, ok := db.ArticleCache.Get(name, 2); ok {
		t.Error("cached article 2 survived the reset")
	}
	spamRows := func(g int64) int {
		var n int
		if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT COUNT(*) FROM spam WHERE newsgroup_id = ?", []interface{}{g}, &n); err != nil {
			t.Fatalf("count spam: %v", err)
		}
		return n
	}
	if spamRows(id) != 0 || fuQueriesSpamFlagCount(t, id) != 0 {
		t.Errorf("spam rows %d, user flags %d of the reset group, want 0 and 0", spamRows(id), fuQueriesSpamFlagCount(t, id))
	}
	if spamRows(otherID) != 1 || fuQueriesSpamFlagCount(t, otherID) != 1 {
		t.Errorf("spam rows %d, user flags %d of another group, want 1 and 1", spamRows(otherID), fuQueriesSpamFlagCount(t, otherID))
	}

	groupDB, err = db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	num, err := db.InsertOverview(groupDB, &models.Overview{
		Subject: "adf-batch", FromHeader: "adf <adf@test.invalid>", DateSent: time.Now(),
		DateString: "x", MessageID: adfBatchMsgID(), Downloaded: 1,
	})
	groupDB.Return()
	if err != nil || num != 1 {
		t.Errorf("first number after reset = %d (%v), want 1", num, err)
	}
}

// TestAdfBatchDeleteSpamRowsForArticles: only the given numbers of the given group lose their
// spam rows and user flags, also across more than one 500-number chunk.
func TestAdfBatchDeleteSpamRowsForArticles(t *testing.T) {
	db := w0DB(t)
	name := fuQueriesGroup(t, true)
	other := fuQueriesGroup(t, true)
	id, otherID := fuQueriesNewsgroupID(t, name), fuQueriesNewsgroupID(t, other)
	user := fuQueriesUser(t)
	for _, g := range []int64{id, otherID} {
		for _, num := range []int64{2, 700, 900} {
			if _, err := RetryableExec(db.GetMainDB(), "INSERT INTO spam (newsgroup_id, article_num) VALUES (?, ?)", g, num); err != nil {
				t.Fatalf("insert spam: %v", err)
			}
			fuQueriesFlagSpam(t, user.ID, g, num)
		}
	}
	nums := make([]int64, 0, 800)
	for n := int64(1); n <= 800; n++ { // 2 and 700, in two chunks; 900 stays
		nums = append(nums, n)
	}
	if err := db.DeleteSpamRowsForArticles(name, nums); err != nil {
		t.Fatalf("DeleteSpamRowsForArticles: %v", err)
	}
	if err := db.DeleteSpamRowsForArticles("adfbatch.no.such.group", nums); err != nil {
		t.Fatalf("DeleteSpamRowsForArticles of an unknown group: %v", err)
	}
	var left int
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT COUNT(*) FROM spam WHERE newsgroup_id = ?", []interface{}{id}, &left); err != nil {
		t.Fatalf("count spam: %v", err)
	}
	if left != 1 || fuQueriesSpamFlagCount(t, id) != 1 {
		t.Errorf("group: %d spam rows, %d user flags left, want 1 and 1 (number 900)", left, fuQueriesSpamFlagCount(t, id))
	}
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT COUNT(*) FROM spam WHERE newsgroup_id = ?", []interface{}{otherID}, &left); err != nil {
		t.Fatalf("count spam: %v", err)
	}
	if left != 3 || fuQueriesSpamFlagCount(t, otherID) != 3 {
		t.Errorf("other group: %d spam rows, %d user flags left, want 3 and 3", left, fuQueriesSpamFlagCount(t, otherID))
	}
}
