package database

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
)

// nntp_intake_test.go covers slice intake-db (S2) of .claude/plans/wip/nntp-intake-injection.md:
// the NULL-safe intake group lookup, the batch stats UPDATE that never creates a newsgroups row
// (A-db-1, SEC-2 DB side), history adds and msgid-cache release for NULL-hierarchy and missing
// rows (INT-6), duplicate message-ids that are neither counted nor re-added (A-db-3), an
// updated_at that never becomes 0001-01-01 or moves backwards (A-db-4), and (plan
// small-fixes-postrun D2) a high_water that the stats UPDATE raises but never lowers.

// idbAdd is one AddArticleToHistory call seen by idbProc.
type idbAdd struct {
	messageID string
	groupID   int64
}

// idbProc is a fake ProcessorInterface that records the history adds of the batch writer.
type idbProc struct {
	db   *Database
	mu   sync.Mutex
	adds []idbAdd
}

func (p *idbProc) CheckNoMoreWorkInHistory() bool { return true }

func (p *idbProc) AddArticleToHistory(messageID string, groupID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.adds = append(p.adds, idbAdd{messageID: messageID, groupID: groupID})
}

func (p *idbProc) ForceCloseGroupDB(groupDB *GroupDB) error { return p.db.ForceCloseGroupDB(groupDB) }

func (p *idbProc) seen() []idbAdd {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]idbAdd(nil), p.adds...)
}

// idbBatchDB returns an isolated batch database (own data root and group DB map) plus its
// recording processor. Its mainDB is a separate *sql.DB pool on the package test DB's main
// database file (same driver, so the same per-connection pragmas): the rows it writes are
// visible through w0DB, but every Shutdown of the isolated database - fuBatchIsolatedDB's
// cleanup, or fuBatchRun's on a wedged batch - closes only this pool, never the shared one.
func idbBatchDB(t *testing.T) (*Database, *idbProc) {
	t.Helper()
	db := fuBatchIsolatedDB(t)
	mainDB, err := sql.Open(driverNameMain, filepath.Join(w0DB(t).dbconfig.DataDir, "cfg", "pugleaf.sq3"))
	if err != nil {
		t.Fatalf("open a second main DB pool: %v", err)
	}
	if err := mainDB.Ping(); err != nil {
		if cerr := mainDB.Close(); cerr != nil {
			t.Logf("close the second main DB pool: %v", cerr)
		}
		t.Fatalf("ping the second main DB pool: %v", err)
	}
	db.mainDB = mainDB
	proc := &idbProc{db: db}
	db.Batch.SetProcessor(proc)
	return db, proc
}

// idbInsertGroup inserts a newsgroups row the way an old batch upsert or a raw import leaves
// it: hierarchy NULL. It returns the row ID.
func idbInsertGroup(t *testing.T, name string) int64 {
	t.Helper()
	res, err := RetryableExec(w0DB(t).mainDB,
		`INSERT INTO newsgroups(name, description, last_article, message_count, active, status, low_water, high_water) VALUES(?, '', 0, 0, 1, 'y', 1, 0)`,
		name)
	if err != nil {
		t.Fatalf("insert newsgroups row %s: %v", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// idbGroupRowCount counts the newsgroups rows with this exact name.
func idbGroupRowCount(t *testing.T, name string) int {
	t.Helper()
	var n int
	if err := RetryableQueryRowScan(w0DB(t).mainDB, `SELECT COUNT(*) FROM newsgroups WHERE name = ?`, []interface{}{name}, &n); err != nil {
		t.Fatalf("count newsgroups rows %s: %v", name, err)
	}
	return n
}

// idbStats reads the counters of a newsgroups row; updated_at is read as raw text.
func idbStats(t *testing.T, name string) (messageCount, lastArticle, highWater int64, updatedAt string) {
	t.Helper()
	var mc, la, hw sql.NullInt64
	var ua sql.NullString
	if err := RetryableQueryRowScan(w0DB(t).mainDB,
		`SELECT message_count, last_article, high_water, CAST(updated_at AS TEXT) FROM newsgroups WHERE name = ?`,
		[]interface{}{name}, &mc, &la, &hw, &ua); err != nil {
		t.Fatalf("read newsgroups row %s: %v", name, err)
	}
	return mc.Int64, la.Int64, hw.Int64, ua.String
}

// idbArticle builds an article for task's group as the capture path leaves it. extra groups
// are added to NewsgroupsPtr (crossposts that are still pending elsewhere).
func idbArticle(task *BatchTasks, messageID string, date time.Time, item *history.MessageIdItem, extra ...*string) *models.Article {
	ptrs := append([]*string{task.Newsgroup}, extra...)
	return &models.Article{
		MessageID:     messageID,
		Subject:       "intake-db",
		FromHeader:    "idb <idb@test.invalid>",
		DateSent:      date,
		DateString:    date.Format(time.RFC1123Z),
		ArticleNums:   make(map[*string]int64),
		NewsgroupsPtr: ptrs,
		IsThrRoot:     true,
		MsgIdItem:     item,
	}
}

// idbQueue puts the articles in task.BATCHchan and counts them in sq.queued, like fuBatchTask.
func idbQueue(sq *SQ3batch, task *BatchTasks, arts ...*models.Article) {
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

// idbRun runs one processNewsgroupBatch for task (wedge-bounded like the fuBatch tests).
func idbRun(t *testing.T, db *Database, task *BatchTasks, logs *fuBatchLogs) {
	t.Helper()
	if logs == nil {
		logs = &fuBatchLogs{}
	}
	fuBatchRun(t, db, task, db.Batch.retryShutdownGrace()+fuBatchSlack, logs)
}

// idbGroupCount counts rows of table in the group database of name.
func idbGroupCount(t *testing.T, db *Database, name, table string) int {
	t.Helper()
	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", name, err)
	}
	defer groupDB.Return()
	var n int
	if err := RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM "+table, nil, &n); err != nil {
		t.Fatalf("count %s in %s: %v", table, name, err)
	}
	return n
}

func idbItem(messageID string) *history.MessageIdItem {
	return &history.MessageIdItem{MessageId: messageID, Response: history.CaseLock}
}

func idbItemResponse(item *history.MessageIdItem) int {
	item.Mux.RLock()
	defer item.Mux.RUnlock()
	return item.Response
}

func idbMsgID(test string) string {
	return fmt.Sprintf("<idb-%s-%d@test.invalid>", test, w0Seq.Add(1))
}

// TestIntakeDBGetIntakeGroup: a row with NULL hierarchy, status and counters resolves with the
// defaults; an unknown name returns sql.ErrNoRows; a nil main DB is an error, not a panic.
func TestIntakeDBGetIntakeGroup(t *testing.T) {
	db := w0DB(t)
	name := w0Name("idb.getintake")
	if _, err := RetryableExec(db.mainDB,
		`INSERT INTO newsgroups(name, last_article, message_count, status, high_water) VALUES(?, NULL, NULL, NULL, NULL)`, name); err != nil {
		t.Fatalf("insert: %v", err)
	}
	g, err := db.GetIntakeGroup(name)
	if err != nil {
		t.Fatalf("GetIntakeGroup on a NULL-hierarchy row: %v (A-db-1: intake must resolve such rows)", err)
	}
	if g.ID <= 0 || g.Name != name || !g.Active || g.Status != "y" || g.MaxArtSize != 0 || g.LastArticle != 0 || g.HighWater != 0 {
		t.Errorf("GetIntakeGroup = %+v, want ID>0, Name %q, Active, Status y and zeros", *g, name)
	}

	if _, err := db.GetIntakeGroup(w0Name("idb.getintake.nosuch")); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown name: err = %v, want sql.ErrNoRows", err)
	}

	var nilMain Database
	if _, err := nilMain.GetIntakeGroup(name); err == nil {
		t.Error("GetIntakeGroup with a nil main DB returned no error")
	}
}

// TestIntakeDBBatchNeverCreatesRow (A-db-1, SEC-2 DB side): a batch for a group without a
// newsgroups row stores the articles but creates no row, and says so in the log.
func TestIntakeDBBatchNeverCreatesRow(t *testing.T) {
	logs := fuBatchCaptureLog(t)
	db, proc := idbBatchDB(t)
	name := w0Name("idb.norow")
	task := fuBatchTask(t, db.Batch, name, 2)
	idbRun(t, db, task, logs)

	if n := idbGroupRowCount(t, name); n != 0 {
		t.Errorf("newsgroups rows for %s = %d, want 0: the batch writer created a row (NULL hierarchy, resurrected group)", name, n)
	}
	want := fmt.Sprintf("[BATCH] processNewsgroupBatch newsgroup '%s' has no newsgroups row: 2 stored articles not counted (rows are never created by the batch writer)", name)
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log lacks %q. Last log lines:\n%s", want, logs.tail(12))
	}
	if n := idbGroupCount(t, db, name, "articles"); n != 2 {
		t.Errorf("articles stored = %d, want 2", n)
	}
	if adds := proc.seen(); len(adds) != 0 {
		t.Errorf("history adds = %d, want 0 without a newsgroups row", len(adds))
	}
}

// TestIntakeDBHistoryForNullHierarchyRow (INT-6): two batches for a row with a NULL hierarchy
// add every article to history with the row's ID and count them; high_water follows the
// highest article number (D2).
func TestIntakeDBHistoryForNullHierarchyRow(t *testing.T) {
	db, proc := idbBatchDB(t)
	name := w0Name("idb.nullhier")
	id := idbInsertGroup(t, name)
	for i := 0; i < 2; i++ {
		task := fuBatchTask(t, db.Batch, name, 2)
		idbRun(t, db, task, nil)
	}
	adds := proc.seen()
	if len(adds) != 4 {
		t.Fatalf("history adds = %d, want 4: %+v", len(adds), adds)
	}
	for _, a := range adds {
		if a.groupID != id {
			t.Errorf("history add %s with group ID %d, want %d", a.messageID, a.groupID, id)
		}
	}
	mc, la, hw, _ := idbStats(t, name)
	if mc != 4 || la != 4 || hw != 4 {
		t.Errorf("message_count=%d last_article=%d high_water=%d, want 4, 4 and 4", mc, la, hw)
	}
}

// TestIntakeDBHighWaterNeverLowered (D2, Q2): a high_water stored by hand above the batch's
// highest article number stays as it is, while a NULL high_water is raised to that number.
func TestIntakeDBHighWaterNeverLowered(t *testing.T) {
	db, _ := idbBatchDB(t)
	high := w0Name("idb.hwhigh")
	idbInsertGroup(t, high)
	if _, err := RetryableExec(w0DB(t).mainDB, `UPDATE newsgroups SET high_water = 100 WHERE name = ?`, high); err != nil {
		t.Fatalf("seed high_water: %v", err)
	}
	null := w0Name("idb.hwnull")
	idbInsertGroup(t, null)
	if _, err := RetryableExec(w0DB(t).mainDB, `UPDATE newsgroups SET high_water = NULL WHERE name = ?`, null); err != nil {
		t.Fatalf("seed NULL high_water: %v", err)
	}
	for _, name := range []string{high, null} {
		task := fuBatchTask(t, db.Batch, name, 2)
		idbRun(t, db, task, nil)
	}

	if mc, la, hw, _ := idbStats(t, high); mc != 2 || la != 2 || hw != 100 {
		t.Errorf("%s: message_count=%d last_article=%d high_water=%d, want 2, 2 and 100: a smaller batch lowered high_water", high, mc, la, hw)
	}
	if mc, la, hw, _ := idbStats(t, null); mc != 2 || la != 2 || hw != 2 {
		t.Errorf("%s: message_count=%d last_article=%d high_water=%d, want 2, 2 and 2: a NULL high_water was not raised", null, mc, la, hw)
	}
}

// TestIntakeDBCaseDupesWithoutRow (INT-6): without a newsgroups row no history add runs, but
// every stored article's msgid item still leaves CaseLock.
func TestIntakeDBCaseDupesWithoutRow(t *testing.T) {
	db, proc := idbBatchDB(t)
	name := w0Name("idb.dupesnorow")
	task := db.Batch.GetOrCreateTasksMapKey(name)
	var items []*history.MessageIdItem
	var arts []*models.Article
	for i := 0; i < 3; i++ {
		id := idbMsgID("dupesnorow")
		item := idbItem(id)
		items = append(items, item)
		arts = append(arts, idbArticle(task, id, time.Now().UTC().Add(-time.Hour), item))
	}
	idbQueue(db.Batch, task, arts...)
	idbRun(t, db, task, nil)

	for i, item := range items {
		if got := idbItemResponse(item); got != history.CaseDupes {
			t.Errorf("item %d Response = %#x, want CaseDupes %#x: it stays CaseLock until eviction (INT-6)", i, got, history.CaseDupes)
		}
	}
	if adds := proc.seen(); len(adds) != 0 {
		t.Errorf("history adds = %d, want 0", len(adds))
	}
}

// TestIntakeDBDuplicatesNotCounted (A-db-3): a message-id twice in one batch, or already in the
// group, is stored once, counted once, added to history once and threaded once.
func TestIntakeDBDuplicatesNotCounted(t *testing.T) {
	db, proc := idbBatchDB(t)
	name := w0Name("idb.dupes")
	idbInsertGroup(t, name)
	task := db.Batch.GetOrCreateTasksMapKey(name)
	idA, idB := idbMsgID("dupes-a"), idbMsgID("dupes-b")
	date := time.Now().UTC().Add(-time.Hour)
	itemA1, itemA2, itemB := idbItem(idA), idbItem(idA), idbItem(idB)
	idbQueue(db.Batch, task,
		idbArticle(task, idA, date, itemA1),
		idbArticle(task, idA, date, itemA2),
		idbArticle(task, idB, date, itemB))
	idbRun(t, db, task, nil)

	check := func(stage string) {
		t.Helper()
		if n := idbGroupCount(t, db, name, "articles"); n != 2 {
			t.Errorf("%s: articles = %d, want 2", stage, n)
		}
		if n := idbGroupCount(t, db, name, "threads"); n != 2 {
			t.Errorf("%s: threads rows = %d, want 2: an ignored duplicate got a number and a second threads row", stage, n)
		}
		if mc, _, _, _ := idbStats(t, name); mc != 2 {
			t.Errorf("%s: message_count = %d, want 2: ignored duplicates were counted", stage, mc)
		}
		if adds := proc.seen(); len(adds) != 2 {
			t.Errorf("%s: history adds = %d, want 2: %+v", stage, len(adds), adds)
		}
	}
	check("first batch")
	for i, item := range []*history.MessageIdItem{itemA1, itemA2, itemB} {
		if got := idbItemResponse(item); got != history.CaseDupes {
			t.Errorf("first batch: item %d Response = %#x, want CaseDupes", i, got)
		}
	}

	itemA3 := idbItem(idA)
	idbQueue(db.Batch, task, idbArticle(task, idA, date, itemA3))
	idbRun(t, db, task, nil)
	check("second batch")
	if got := idbItemResponse(itemA3); got != history.CaseDupes {
		t.Errorf("second batch: A's item Response = %#x, want CaseDupes", got)
	}
}

// TestIntakeDBUpdatedAtCrosspost (A-db-4): a batch whose only article is still pending in
// another group moves updated_at to the article's date (never 0001-01-01), and an older
// article never moves it backwards. Dates are relative to now: the stats block clamps future
// dates to now.
func TestIntakeDBUpdatedAtCrosspost(t *testing.T) {
	db, _ := idbBatchDB(t)
	name := w0Name("idb.updatedat")
	idbInsertGroup(t, name)
	const layout = "2006-01-02 15:04:05"
	now := time.Now().UTC().Truncate(time.Second)
	before := now.Add(-48 * time.Hour).Format(layout)
	if _, err := RetryableExec(w0DB(t).mainDB, `UPDATE newsgroups SET updated_at = ? WHERE name = ?`, before, name); err != nil {
		t.Fatalf("seed updated_at: %v", err)
	}
	other := w0Name("idb.updatedat.other")
	task := db.Batch.GetOrCreateTasksMapKey(name)
	articleDate := now.Add(-24 * time.Hour)
	idbQueue(db.Batch, task, idbArticle(task, idbMsgID("updatedat"), articleDate, nil, &other))
	idbRun(t, db, task, nil)

	want := articleDate.Format(layout)
	if _, _, _, got := idbStats(t, name); got != want {
		t.Errorf("updated_at after the crosspost batch = %q, want %q (A-db-4: a crosspost batch must not leave it unset or write 0001-01-01)", got, want)
	}

	idbQueue(db.Batch, task, idbArticle(task, idbMsgID("updatedat-old"), time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), nil))
	idbRun(t, db, task, nil)
	if _, _, _, got := idbStats(t, name); got != want {
		t.Errorf("updated_at after a backfill batch = %q, want %q unchanged: it moved backwards", got, want)
	}
	if mc, _, _, _ := idbStats(t, name); mc != 2 {
		t.Errorf("message_count = %d, want 2", mc)
	}
}

// TestIntakeDBLookupMsgId (A-db-3): stored → (true, nil), unknown → (false, nil), a closed
// group database → an error instead of "not stored".
func TestIntakeDBLookupMsgId(t *testing.T) {
	db := fuBatchIsolatedDB(t)
	name := w0Name("idb.lookup")
	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", name, err)
	}
	stored := idbMsgID("lookup")
	if _, err := RetryableExec(groupDB.DB, `INSERT INTO articles (message_id) VALUES (?)`, stored); err != nil {
		groupDB.Return()
		t.Fatalf("insert article: %v", err)
	}
	if ok, err := groupDB.LookupMsgIdInArticlesDB(stored); !ok || err != nil {
		t.Errorf("stored id: (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := groupDB.LookupMsgIdInArticlesDB(idbMsgID("lookup-nosuch")); ok || err != nil {
		t.Errorf("unknown id: (%v, %v), want (false, nil)", ok, err)
	}
	if err := db.ForceCloseGroupDB(groupDB); err != nil {
		t.Fatalf("ForceCloseGroupDB: %v", err)
	}
	if ok, err := groupDB.LookupMsgIdInArticlesDB(stored); ok || err == nil {
		t.Errorf("closed group DB: (%v, %v), want (false, error)", ok, err)
	}
}
