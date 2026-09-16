package database

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/mattn/go-sqlite3"
)

// TestLo2WriterBatchGroupDBRetry pins the retry policy the batch writer uses for a
// batch it has already drained out of BATCHchan (F16).
func TestLo2WriterBatchGroupDBRetry(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("failed to get group database x: %w", err) }
	for _, tc := range []struct {
		name    string
		err     error
		attempt int
		retry   bool
		delay   time.Duration
	}{
		{"nil", nil, 0, false, 0},
		{"closed", wrap(errGroupDBClosed), 0, false, 0},
		{"closed late", wrap(errGroupDBClosed), 50, false, 0},
		{"init timeout", wrap(errGroupDBInitTimeout), 0, true, time.Second},
		{"init timeout late", wrap(errGroupDBInitTimeout), 50, true, time.Second},
		{"failed 0", wrap(errGroupDBInitFailed), 0, true, 1 * time.Second},
		{"failed 1", wrap(errGroupDBInitFailed), 1, true, 2 * time.Second},
		{"failed 2", wrap(errGroupDBInitFailed), 2, true, 4 * time.Second},
		{"failed 3", wrap(errGroupDBInitFailed), 3, true, 8 * time.Second},
		{"failed 4", wrap(errGroupDBInitFailed), 4, true, 16 * time.Second},
		{"failed 5 capped", wrap(errGroupDBInitFailed), 5, true, 30 * time.Second},
		{"failed 6 capped", wrap(errGroupDBInitFailed), 6, true, 30 * time.Second},
		{"failed 7 gives up", wrap(errGroupDBInitFailed), 7, false, 0},
		{"failed 8 gives up", wrap(errGroupDBInitFailed), 8, false, 0},
		{"other error", errors.New("disk full"), 0, true, time.Second},
		{"other error gives up", errors.New("disk full"), batchGroupDBAttempts - 1, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retry, delay := batchGroupDBRetry(tc.err, tc.attempt)
			if retry != tc.retry || delay != tc.delay {
				t.Fatalf("batchGroupDBRetry(%v, %d) = %v, %v; want %v, %v",
					tc.err, tc.attempt, retry, delay, tc.retry, tc.delay)
			}
		})
	}
	if batchGroupDBAttempts != 8 {
		t.Fatalf("batchGroupDBAttempts = %d, want 8", batchGroupDBAttempts)
	}
}

// TestLo2WriterRetryCapCountsBusyTime shows that the retry cap measures the time spent
// waiting for a busy database, not the time the work itself took (F17): an operation
// that runs longer than the cap and only then hits BUSY is still retried.
//
// It lowers the process-global cap for a few hundred milliseconds. As in
// TestLo1DBRetryWaitRace that is safe only because no test in this package calls
// t.Parallel and the cap is read from retryBackoff, i.e. only after a BUSY/LOCKED error
// on a database this test owns.
func TestLo2WriterRetryCapCountsBusyTime(t *testing.T) {
	t.Cleanup(func() { SetSQLiteMaxRetryWait(0) })
	SetSQLiteMaxRetryWait(100 * time.Millisecond)

	tdb, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "lo2writer-cap.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()
	if _, err := tdb.Exec("CREATE TABLE lo2writer_cap (n INTEGER)"); err != nil {
		t.Fatal(err)
	}

	calls := 0
	err = RetryableTransactionExec(tdb, func(tx *sql.Tx) error {
		calls++
		// Work that takes far longer than the cap and only then hits BUSY.
		time.Sleep(300 * time.Millisecond)
		if calls == 1 {
			return sqlite3.Error{Code: sqlite3.ErrBusy}
		}
		_, execErr := tx.Exec("INSERT INTO lo2writer_cap (n) VALUES (?)", calls)
		return execErr
	})
	if err != nil {
		t.Fatalf("RetryableTransactionExec: %v (cap %v must count busy time only)", err, GetSQLiteMaxRetryWait())
	}
	if calls != 2 {
		t.Fatalf("txFunc calls = %d, want 2", calls)
	}
	var n int
	if err := RetryableQueryRowScan(tdb, "SELECT COUNT(*) FROM lo2writer_cap", nil, &n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

// TestLo2WriterStatsRetry: the batch stats update rides out busy errors (the articles
// are already committed) and returns any other error at once (F17).
func TestLo2WriterStatsRetry(t *testing.T) {
	running := func() bool { return false }

	calls := 0
	err := updateNewsgroupStatsWithRetry(func() error {
		calls++
		if calls <= 2 {
			return sqlite3.Error{Code: sqlite3.ErrBusy}
		}
		return nil
	}, time.Millisecond, "'lo2writer.stats' (+3 articles, max_article=7)", running, batchShutdownGrace)
	if err != nil || calls != 3 {
		t.Fatalf("busy twice: err=%v calls=%d; want nil, 3", err, calls)
	}

	fatal := errors.New("no such table: newsgroups")
	calls = 0
	err = updateNewsgroupStatsWithRetry(func() error {
		calls++
		return fatal
	}, time.Millisecond, "'lo2writer.stats'", running, batchShutdownGrace)
	if !errors.Is(err, fatal) || calls != 1 {
		t.Fatalf("non-retryable: err=%v calls=%d; want %v, 1", err, calls, fatal)
	}

	calls = 0
	if err := updateNewsgroupStatsWithRetry(func() error { calls++; return nil }, time.Millisecond, "ok", running, batchShutdownGrace); err != nil || calls != 1 {
		t.Fatalf("success: err=%v calls=%d; want nil, 1", err, calls)
	}

	// Shutdown has begun: the loop must stop within the grace, because every tool waits
	// for db.WG before it closes the databases. A permanently busy main DB would
	// otherwise hang the exit. The bound is wall-clock, so it holds however long one
	// round takes inside RetryableTransactionExec.
	busy := sqlite3.Error{Code: sqlite3.ErrBusy}
	calls = 0
	const grace = 50 * time.Millisecond
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		done <- updateNewsgroupStatsWithRetry(func() error {
			calls++
			// A round that itself takes longer than the grace (RetryableTransactionExec
			// waits for GetSQLiteMaxRetryWait before it reports the busy error).
			time.Sleep(30 * time.Millisecond)
			return busy
		}, time.Millisecond, "'lo2writer.stats'", func() bool { return true }, grace)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, busy) {
			t.Fatalf("shutdown bound: err=%v, want the busy error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("updateNewsgroupStatsWithRetry did not stop after shutdown began")
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("shutdown bound took %v for a %v grace", took, grace)
	}
	if calls < 2 {
		t.Fatalf("shutdown bound: calls=%d, want at least one retry before the grace passed", calls)
	}
}

// TestLo2WriterBatchShutdownClock: the retry loops of the batch writer are unbounded
// while the database is live and get a wall-clock grace once shutdown has begun, so the
// bound does not depend on how long one attempt takes.
func TestLo2WriterBatchShutdownClock(t *testing.T) {
	live := batchShutdownClock{grace: time.Nanosecond}
	for i := 0; i < 5; i++ {
		if live.expired(func() bool { return false }) {
			t.Fatal("clock expired while the database is live")
		}
	}
	var none batchShutdownClock
	if none.expired(nil) {
		t.Fatal("clock with no isShutdown must never expire")
	}

	shutdown := func() bool { return true }
	c := batchShutdownClock{grace: 30 * time.Millisecond}
	if c.expired(shutdown) {
		t.Fatal("the first shutdown check must start the clock, not expire it")
	}
	if c.expired(shutdown) {
		t.Fatal("clock expired before its grace had passed")
	}
	started := c.deadline
	time.Sleep(40 * time.Millisecond)
	if !c.expired(shutdown) {
		t.Fatal("clock did not expire after its grace")
	}
	if c.deadline != started {
		t.Fatal("the deadline moved after the clock was started")
	}
}

// TestLo2WriterSpamFlagZeroRows: when the counter UPDATE matches no row the flag is
// rolled back and the call fails instead of reporting success (F18).
func TestLo2WriterSpamFlagZeroRows(t *testing.T) {
	db := w0DB(t)
	user := w1APIUser(t)
	group := w1APIGroup(t)

	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	_, err = db.InsertOverview(groupDB, &models.Overview{
		ArticleNum: 1, Subject: "spam", FromHeader: "a <a@b>", DateSent: time.Now(),
		DateString: "x", MessageID: fmt.Sprintf("<lo2writer-spam-%d@test.invalid>", w0Seq.Add(1)), Downloaded: 1,
	})
	if err != nil {
		groupDB.Return()
		t.Fatalf("InsertOverview: %v", err)
	}
	// Make the counter UPDATE a no-op without removing the article, which is what a
	// concurrent delete between the existence check and the UPDATE looks like.
	if _, err := RetryableExec(groupDB.DB,
		"CREATE TRIGGER lo2_ignore BEFORE UPDATE OF spam ON articles BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		groupDB.Return()
		t.Fatalf("create trigger: %v", err)
	}
	groupDB.Return()

	ok, err := db.FlagArticleSpamByUser(user.ID, group, 1)
	if ok || !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("FlagArticleSpamByUser = %v, %v; want false, ErrArticleNotFound", ok, err)
	}

	var flags, spamRows int
	if err := RetryableQueryRowScan(db.GetMainDB(),
		"SELECT (SELECT COUNT(*) FROM user_spam_flags WHERE user_id = ? AND article_num = 1 AND newsgroup_id = (SELECT id FROM newsgroups WHERE name = ?)), "+
			"(SELECT COUNT(*) FROM spam WHERE article_num = 1 AND newsgroup_id = (SELECT id FROM newsgroups WHERE name = ?))",
		[]interface{}{user.ID, group, group}, &flags, &spamRows); err != nil {
		t.Fatalf("count flags: %v", err)
	}
	if flags != 0 || spamRows != 0 {
		t.Fatalf("user_spam_flags rows = %d, spam rows = %d; want 0 and 0", flags, spamRows)
	}
}

// TestLo2WriterPragmaStrip: a PRAGMA line is removed, a PRAGMA without its semicolon is
// left in place so the migration fails instead of losing the next statement (F19).
func TestLo2WriterPragmaStrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"whole line", "PRAGMA foreign_keys = ON;\nCREATE TABLE a(x);\n", "\nCREATE TABLE a(x);\n"},
		{"trailing comment", "PRAGMA journal_mode = WAL; -- set by the driver\nCREATE TABLE b(x);\n", "\nCREATE TABLE b(x);\n"},
		// The CR belongs to the stripped line, the line feed stays.
		{"crlf", "PRAGMA foreign_keys = ON;\r\nCREATE TABLE c(x);\r\n", "\nCREATE TABLE c(x);\r\n"},
		{"indented lowercase", "\tpragma synchronous = NORMAL;\nCREATE TABLE d(x);\n", "\nCREATE TABLE d(x);\n"},
		{"no semicolon keeps everything", "PRAGMA foreign_keys = ON\nCREATE TABLE t(x);\n", "PRAGMA foreign_keys = ON\nCREATE TABLE t(x);\n"},
		{"no pragma", "CREATE TABLE e(x);\n", "CREATE TABLE e(x);\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripMigrationPragmas(tc.in); got != tc.want {
				t.Fatalf("stripMigrationPragmas(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestLo2WriterCronDBRunsUntilShutdown: the idle group DB cleanup keeps running while
// the batch drain still opens group databases after StopChan closed, and stops once the
// group databases are shut down (F20).
func TestLo2WriterCronDBRunsUntilShutdown(t *testing.T) {
	shared := w0DB(t)
	cfg := *shared.dbconfig
	cfg.DataDir = t.TempDir()
	fresh := &Database{
		dbconfig: &cfg,
		groupDB:  make(map[string]*GroupDB),
		Batch:    shared.Batch,
		StopChan: make(chan struct{}, 1),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		fresh.cronDBEvery(10 * time.Millisecond)
	}()

	close(fresh.StopChan)
	select {
	case <-done:
		t.Fatal("cronDBEvery returned on StopChan; it must keep closing idle group DBs during the final drain")
	case <-time.After(50 * time.Millisecond):
	}

	if err := fresh.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cronDBEvery still running 1s after Shutdown")
	}
}

// TestLo2WriterUpsertSectionID: the id comes from the stored row, also when the section
// already existed, so the section_groups rows that follow hit the right section (F21).
func TestLo2WriterUpsertSectionID(t *testing.T) {
	db := w0DB(t)
	name := w0Name("lo2sec")
	section := &models.Section{Name: name, DisplayName: name, Description: "lo2 writer", ShowInHeader: true, EnableLocalSpool: false, SortOrder: 3}

	first, err := db.UpsertSectionID(section)
	if err != nil {
		t.Fatalf("UpsertSectionID (insert): %v", err)
	}
	if first <= 0 {
		t.Fatalf("UpsertSectionID returned id %d", first)
	}

	// Another INSERT in between: LastInsertId would now point at that row.
	other := w0Name("lo2sec.other")
	if _, err := db.UpsertSectionID(&models.Section{Name: other, DisplayName: other}); err != nil {
		t.Fatalf("UpsertSectionID (other): %v", err)
	}

	again, err := db.UpsertSectionID(section)
	if err != nil {
		t.Fatalf("UpsertSectionID (existing): %v", err)
	}
	if again != first {
		t.Fatalf("UpsertSectionID returned %d, want the stored id %d", again, first)
	}

	// With foreign keys enforced this only works for a section id that exists.
	if _, err := RetryableExec(db.GetMainDB(),
		"INSERT INTO section_groups (section_id, newsgroup_name, group_description, sort_order, is_category_header) VALUES (?, ?, '', 0, 0)",
		again, w0Name("lo2sec.grp")); err != nil {
		t.Fatalf("insert section_groups with id %d: %v", again, err)
	}

	var stored string
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT name FROM sections WHERE id = ?", []interface{}{again}, &stored); err != nil {
		t.Fatalf("read section name: %v", err)
	}
	if stored != name {
		t.Fatalf("section id %d holds %q, want %q", again, stored, name)
	}
}

// TestLo2WriterNNTPAuthDisabledWebUser: NNTP authentication is refused for a disabled
// web user and for an inactive NNTP account, on the cache-hit path too (F22).
func TestLo2WriterNNTPAuthDisabledWebUser(t *testing.T) {
	db := w0DB(t)
	if db.NNTPAuthCache == nil {
		t.Fatal("NNTPAuthCache is nil: the cache-hit path cannot be tested")
	}
	const password = "lo2writer-nntp-pw"

	web := w1APIUser(t)
	linkedName := w0Name("lo2nntp_linked")
	if err := db.InsertNNTPUser(&models.NNTPUser{Username: linkedName, Password: password, MaxConns: 1, WebUserID: web.ID, IsActive: true}); err != nil {
		t.Fatalf("InsertNNTPUser (linked): %v", err)
	}
	t.Cleanup(func() { db.NNTPAuthCache.Remove(linkedName) })

	user, err := db.AuthenticateNNTPUser(linkedName, password)
	if err != nil || user == nil {
		t.Fatalf("AuthenticateNNTPUser (enabled): %v", err)
	}
	if _, found := db.NNTPAuthCache.Get(linkedName, password); !found {
		t.Fatal("successful login was not cached")
	}

	if err := db.UpdateUserStatus(web.ID, 1, 1, 0); err != nil {
		t.Fatalf("UpdateUserStatus(disabled): %v", err)
	}
	if u, err := db.AuthenticateNNTPUser(linkedName, password); err == nil {
		t.Fatalf("AuthenticateNNTPUser with a disabled web user returned %+v, want an error", u)
	}
	if _, found := db.NNTPAuthCache.Get(linkedName, password); found {
		t.Fatal("a refused login stayed in the authentication cache")
	}
	// Still refused after the cache was dropped (the miss path re-checks too).
	if _, err := db.AuthenticateNNTPUser(linkedName, password); err == nil {
		t.Fatal("AuthenticateNNTPUser with a disabled web user succeeded on the cache miss path")
	}

	// A standalone NNTP account: deactivating it must end the cached login as well.
	soloName := w0Name("lo2nntp_solo")
	if err := db.InsertNNTPUser(&models.NNTPUser{Username: soloName, Password: password, MaxConns: 1, IsActive: true}); err != nil {
		t.Fatalf("InsertNNTPUser (solo): %v", err)
	}
	t.Cleanup(func() { db.NNTPAuthCache.Remove(soloName) })
	solo, err := db.AuthenticateNNTPUser(soloName, password)
	if err != nil || solo == nil {
		t.Fatalf("AuthenticateNNTPUser (solo): %v", err)
	}
	if _, found := db.NNTPAuthCache.Get(soloName, password); !found {
		t.Fatal("successful solo login was not cached")
	}
	if err := db.DeactivateNNTPUser(solo.ID); err != nil {
		t.Fatalf("DeactivateNNTPUser: %v", err)
	}
	if u, err := db.AuthenticateNNTPUser(soloName, password); err == nil {
		t.Fatalf("AuthenticateNNTPUser for an inactive NNTP user returned %+v, want an error", u)
	}
	if _, found := db.NNTPAuthCache.Get(soloName, password); found {
		t.Fatal("a refused solo login stayed in the authentication cache")
	}

	// A deleted NNTP user: the cached login must not survive the missing row either.
	goneName := w0Name("lo2nntp_gone")
	if err := db.InsertNNTPUser(&models.NNTPUser{Username: goneName, Password: password, MaxConns: 1, IsActive: true}); err != nil {
		t.Fatalf("InsertNNTPUser (gone): %v", err)
	}
	t.Cleanup(func() { db.NNTPAuthCache.Remove(goneName) })
	gone, err := db.AuthenticateNNTPUser(goneName, password)
	if err != nil || gone == nil {
		t.Fatalf("AuthenticateNNTPUser (gone): %v", err)
	}
	if err := db.DeleteNNTPUser(gone.ID); err != nil {
		t.Fatalf("DeleteNNTPUser: %v", err)
	}
	if u, err := db.AuthenticateNNTPUser(goneName, password); err == nil {
		t.Fatalf("AuthenticateNNTPUser for a deleted NNTP user returned %+v, want an error", u)
	}
	if _, found := db.NNTPAuthCache.Get(goneName, password); found {
		t.Fatal("a deleted NNTP user stayed in the authentication cache")
	}
}
