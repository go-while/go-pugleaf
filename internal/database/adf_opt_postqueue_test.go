package database

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
)

// adfOptPostQueueGroup inserts an active newsgroup for the post_queue foreign key and removes
// it, together with its post_queue rows, when the test ends.
func adfOptPostQueueGroup(t *testing.T) (name string, id int64) {
	t.Helper()
	db := w0DB(t)
	name = w0Name("adfopt.postqueue")
	if _, err := RetryableExec(db.GetMainDB(),
		`INSERT INTO newsgroups(name, description, active, hierarchy) VALUES (?, '', 1, 'adfopt')`, name); err != nil {
		t.Fatalf("insert newsgroup %s: %v", name, err)
	}
	id = fuQueriesNewsgroupID(t, name)
	if id == 0 {
		t.Fatalf("precondition: newsgroup %s has no id", name)
	}
	t.Cleanup(func() {
		if _, err := RetryableExec(db.GetMainDB(), "DELETE FROM post_queue WHERE newsgroup_id = ?", id); err != nil {
			t.Errorf("cleanup post_queue of %s: %v", name, err)
		}
		if _, err := RetryableExec(db.GetMainDB(), "DELETE FROM newsgroups WHERE id = ?", id); err != nil {
			t.Errorf("cleanup newsgroup %s: %v", name, err)
		}
	})
	return name, id
}

// adfOptPostQueueInsert queues n message-ids for groupID and returns their row ids in order.
func adfOptPostQueueInsert(t *testing.T, groupID int64, prefix string, n int) []int64 {
	t.Helper()
	db := w0DB(t)
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		msgID := "<" + prefix + "." + string(rune('a'+i)) + "@adfopt.invalid>"
		if err := db.InsertPostQueueEntry(groupID, msgID); err != nil {
			t.Fatalf("InsertPostQueueEntry %s: %v", msgID, err)
		}
		var id int64
		if err := RetryableQueryRowScan(db.GetMainDB(),
			"SELECT id FROM post_queue WHERE message_id = ?", []interface{}{msgID}, &id); err != nil {
			t.Fatalf("id of %s: %v", msgID, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// adfOptPostQueueFlags returns posted_to_remote and in_processing of row id.
func adfOptPostQueueFlags(t *testing.T, id int64) (posted, inProcessing bool) {
	t.Helper()
	if err := RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT posted_to_remote, in_processing FROM post_queue WHERE id = ?", []interface{}{id}, &posted, &inProcessing); err != nil {
		t.Fatalf("flags of post_queue row %d: %v", id, err)
	}
	return posted, inProcessing
}

// adfOptPostQueueMine returns the ids of entries that belong to groupID, and fails on duplicates.
func adfOptPostQueueMine(t *testing.T, entries []PostQueueEntry, groupID int64) map[int64]bool {
	t.Helper()
	mine := make(map[int64]bool)
	for _, e := range entries {
		if e.NewsgroupID != groupID {
			continue
		}
		if mine[e.ID] {
			t.Errorf("entry %d returned twice", e.ID)
		}
		if !e.InProcessing {
			t.Errorf("entry %d: InProcessing = false, want true", e.ID)
		}
		mine[e.ID] = true
	}
	return mine
}

// TestAdfOptPostQueueLifecycle: select marks rows in_processing, a reset (by id or all) returns
// them to pending, Mark sets posted, and the admin readers see the states (A-intake-10).
func TestAdfOptPostQueueLifecycle(t *testing.T) {
	db := w0DB(t)
	_, gid := adfOptPostQueueGroup(t)
	ids := adfOptPostQueueInsert(t, gid, "lifecycle", 3)

	entries, err := db.GetPendingPostQueueEntries(1000)
	if err != nil {
		t.Fatalf("GetPendingPostQueueEntries: %v", err)
	}
	mine := adfOptPostQueueMine(t, entries, gid)
	for _, id := range ids {
		if !mine[id] {
			t.Errorf("entry %d not selected", id)
		}
		if posted, inProc := adfOptPostQueueFlags(t, id); posted || !inProc {
			t.Errorf("entry %d after select: posted=%v in_processing=%v, want false, true", id, posted, inProc)
		}
	}

	// rows in processing are not selected again
	entries, err = db.GetPendingPostQueueEntries(1000)
	if err != nil {
		t.Fatalf("GetPendingPostQueueEntries (2nd): %v", err)
	}
	if mine := adfOptPostQueueMine(t, entries, gid); len(mine) != 0 {
		t.Errorf("rows in processing selected again: %v", mine)
	}

	// reset by id
	if err := db.ResetPostQueueProcessing([]int64{ids[0]}); err != nil {
		t.Fatalf("ResetPostQueueProcessing: %v", err)
	}
	if _, inProc := adfOptPostQueueFlags(t, ids[0]); inProc {
		t.Errorf("entry %d still in_processing after ResetPostQueueProcessing", ids[0])
	}
	if err := db.ResetPostQueueProcessing(nil); err != nil {
		t.Errorf("ResetPostQueueProcessing(nil): %v", err)
	}

	// posted
	if err := db.MarkPostQueueAsPostedToRemote(ids[1]); err != nil {
		t.Fatalf("MarkPostQueueAsPostedToRemote: %v", err)
	}
	if posted, inProc := adfOptPostQueueFlags(t, ids[1]); !posted || inProc {
		t.Errorf("entry %d after Mark: posted=%v in_processing=%v, want true, false", ids[1], posted, inProc)
	}

	// startup reset: the stale row goes back to pending, the posted row stays posted
	if err := db.ResetAllPostQueueProcessing(); err != nil {
		t.Fatalf("ResetAllPostQueueProcessing: %v", err)
	}
	if posted, inProc := adfOptPostQueueFlags(t, ids[2]); posted || inProc {
		t.Errorf("entry %d after ResetAll: posted=%v in_processing=%v, want false, false", ids[2], posted, inProc)
	}
	if posted, inProc := adfOptPostQueueFlags(t, ids[1]); !posted || inProc {
		t.Errorf("entry %d after ResetAll: posted=%v in_processing=%v, want true, false", ids[1], posted, inProc)
	}

	// admin readers
	list, err := db.GetAllPostQueueEntries(0, 0, "", "lifecycle.")
	if err != nil {
		t.Fatalf("GetAllPostQueueEntries: %v", err)
	}
	status := make(map[int64]string)
	for _, e := range list {
		if e.NewsgroupID == gid {
			status[e.ID] = e.Status
		}
	}
	want := map[int64]string{ids[0]: "pending", ids[1]: "completed", ids[2]: "pending"}
	for id, w := range want {
		if status[id] != w {
			t.Errorf("GetAllPostQueueEntries: entry %d status %q, want %q", id, status[id], w)
		}
	}
	stats, err := db.GetPostQueueStats()
	if err != nil {
		t.Fatalf("GetPostQueueStats: %v", err)
	}
	if stats["total"] < 3 || stats["completed"] < 1 || stats["pending"] < 2 {
		t.Errorf("GetPostQueueStats = %v, want total >= 3, completed >= 1, pending >= 2", stats)
	}
	if stats["total"] != stats["pending"]+stats["parked"]+stats["processing"]+stats["completed"] {
		t.Errorf("GetPostQueueStats = %v: total != pending+parked+processing+completed", stats)
	}

	// cleanup of posted rows and the limit
	if err := db.DeletePostQueueEntry(ids[1]); err != nil {
		t.Fatalf("DeletePostQueueEntry: %v", err)
	}
	var n int
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT COUNT(*) FROM post_queue WHERE newsgroup_id = ?", []interface{}{gid}, &n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("rows after DeletePostQueueEntry = %d, want 2", n)
	}
	entries, err = db.GetPendingPostQueueEntries(1)
	if err != nil {
		t.Fatalf("GetPendingPostQueueEntries(1): %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("GetPendingPostQueueEntries(1) returned %d entries, want 1", len(entries))
	}
	if err := db.ResetAllPostQueueProcessing(); err != nil {
		t.Fatalf("ResetAllPostQueueProcessing: %v", err)
	}
}

// TestAdfOptPostQueueAdminNullMessageID: GetAllPostQueueEntries returns scan errors instead of
// skipping rows, so a row with a NULL message_id (the column is nullable) must still scan.
func TestAdfOptPostQueueAdminNullMessageID(t *testing.T) {
	db := w0DB(t)
	_, gid := adfOptPostQueueGroup(t)
	// posted_to_remote = 1: the row is never selected for posting by other tests
	res, err := RetryableExec(db.GetMainDB(),
		`INSERT INTO post_queue (newsgroup_id, message_id, posted_to_remote, in_processing) VALUES (?, NULL, 1, 0)`, gid)
	if err != nil {
		t.Fatalf("insert NULL message_id row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	list, err := db.GetAllPostQueueEntries(0, 0, "completed", "")
	if err != nil {
		t.Fatalf("GetAllPostQueueEntries: %v", err)
	}
	found := false
	for _, e := range list {
		if e.ID == id {
			found = true
			if e.MessageID != "" || e.Status != "completed" {
				t.Errorf("entry %d: message_id %q status %q, want \"\", \"completed\"", id, e.MessageID, e.Status)
			}
		}
	}
	if !found {
		t.Errorf("entry %d with NULL message_id not listed", id)
	}
}

// TestAdfOptPostQueueSelectRetry: the select-and-mark transaction runs while another connection
// holds the write lock and commits a new row. The UPDATE then fails with SQLITE_BUSY (stale
// snapshot) and RetryableTransactionExec runs the closure again: the result must hold every row
// once and every returned row must be marked.
func TestAdfOptPostQueueSelectRetry(t *testing.T) {
	db := w0DB(t)
	_, gid := adfOptPostQueueGroup(t)
	ids := adfOptPostQueueInsert(t, gid, "retry", 2)

	tx, err := db.GetMainDB().Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	const lateMsgID = "<retry.late@adfopt.invalid>"
	if _, err := tx.Exec(`INSERT INTO post_queue (newsgroup_id, message_id, posted_to_remote, in_processing) VALUES (?, ?, 0, 0)`,
		gid, lateMsgID); err != nil {
		t.Fatalf("insert under held write lock: %v", err)
	}

	type result struct {
		entries []PostQueueEntry
		err     error
	}
	// Commit only after the reader has hit the held lock and retried: retryBackoff logs the
	// first retry (retryLogThrottle(1)), so wait for that line instead of a fixed sleep that
	// a loaded machine could outrun without any retry (as fu_queries_test E10c does).
	logs := fuCleanupCaptureLog(t)
	resCh := make(chan result, 1)
	go func() {
		e, err := db.GetPendingPostQueueEntries(1000)
		resCh <- result{e, err}
	}()
	retried := time.Now().Add(testWait)
	for !logs.contains("SQLite retry attempt", "for transaction: database is locked") {
		select {
		case r := <-resCh:
			t.Fatalf("GetPendingPostQueueEntries returned (%d entries, err %v) while the write lock was held, without a logged retry", len(r.entries), r.err)
		default:
		}
		if time.Now().After(retried) {
			t.Fatalf("GetPendingPostQueueEntries logged no SQLite retry within %v while the write lock was held", testWait)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	committed = true

	var res result
	select {
	case res = <-resCh:
	case <-time.After(testWait):
		t.Fatalf("GetPendingPostQueueEntries did not return within %v", testWait)
	}
	if res.err != nil {
		t.Fatalf("GetPendingPostQueueEntries: %v", res.err)
	}
	mine := adfOptPostQueueMine(t, res.entries, gid)
	for _, id := range ids {
		if !mine[id] {
			t.Errorf("entry %d not selected", id)
		}
	}
	// every returned row is marked in the DB
	for id := range mine {
		if _, inProc := adfOptPostQueueFlags(t, id); !inProc {
			t.Errorf("returned entry %d is not in_processing in the DB", id)
		}
	}
	late := false
	for _, e := range res.entries {
		if e.NewsgroupID != gid {
			continue
		}
		if !strings.HasPrefix(e.MessageID, "<retry.") {
			t.Errorf("unexpected message-id %q", e.MessageID)
		}
		if e.MessageID == lateMsgID {
			late = true
		}
	}
	// the selection that finally committed ran after the holder's commit, so it sees the late row
	if !late {
		t.Errorf("late row %s not selected: %d of my entries returned", lateMsgID, len(mine))
	}
	if err := db.ResetAllPostQueueProcessing(); err != nil {
		t.Fatalf("ResetAllPostQueueProcessing: %v", err)
	}
}

// flPqSetMax sets PostQueueMaxAttempts to v and restores "5" (the migration default) when the
// test ends: the test DB is shared by every test of the package.
func flPqSetMax(t *testing.T, v string) {
	t.Helper()
	db := w0DB(t)
	if err := db.SetConfigValue(config.CFG_KEY_POSTQUEUE_MAXATTEMPTS, v); err != nil {
		t.Fatalf("SetConfigValue(%s, %q): %v", config.CFG_KEY_POSTQUEUE_MAXATTEMPTS, v, err)
	}
	t.Cleanup(func() {
		if err := db.SetConfigValue(config.CFG_KEY_POSTQUEUE_MAXATTEMPTS, strconv.Itoa(DefaultPostQueueMaxAttempts)); err != nil {
			t.Errorf("restore %s: %v", config.CFG_KEY_POSTQUEUE_MAXATTEMPTS, err)
		}
	})
}

// flPqRow returns attempts and last_error ("" for NULL) of post_queue row id.
func flPqRow(t *testing.T, id int64) (attempts int, lastError string) {
	t.Helper()
	if err := RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT attempts, COALESCE(last_error, '') FROM post_queue WHERE id = ?", []interface{}{id}, &attempts, &lastError); err != nil {
		t.Fatalf("attempts of post_queue row %d: %v", id, err)
	}
	return attempts, lastError
}

// flPqSelect runs GetPendingPostQueueEntries and returns the entry with id, or nil when it was
// not selected. Every other selected entry of groupID is a test failure.
func flPqSelect(t *testing.T, groupID, id int64) *PostQueueEntry {
	t.Helper()
	entries, err := w0DB(t).GetPendingPostQueueEntries(1000)
	if err != nil {
		t.Fatalf("GetPendingPostQueueEntries: %v", err)
	}
	var found *PostQueueEntry
	for i := range entries {
		if entries[i].NewsgroupID != groupID {
			continue
		}
		if entries[i].ID != id {
			t.Errorf("unexpected entry %d selected", entries[i].ID)
			continue
		}
		found = &entries[i]
	}
	return found
}

// flPqStatus returns the admin status of row id (filter "" = all) or "" when it is not listed.
func flPqStatus(t *testing.T, id int64, filter, search string) string {
	t.Helper()
	list, err := w0DB(t).GetAllPostQueueEntries(0, 0, filter, search)
	if err != nil {
		t.Fatalf("GetAllPostQueueEntries(%q): %v", filter, err)
	}
	for _, e := range list {
		if e.ID == id {
			return e.Status
		}
	}
	return ""
}

// TestFlPqMigration0030Fresh: migration 0030 applies to an empty main database and adds
// post_queue.attempts (NOT NULL DEFAULT 0), post_queue.last_error and the config key.
func TestFlPqMigration0030Fresh(t *testing.T) {
	shared := w0DB(t)
	mainDB, err := sql.Open(driverNameMain, filepath.Join(t.TempDir(), "main.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	fresh := &Database{mainDB: mainDB, dbconfig: shared.dbconfig}
	if err := fresh.migrateMainDB(); err != nil {
		t.Fatalf("main migrations: %v", err)
	}

	rows, err := mainDB.Query("PRAGMA table_info(post_queue)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	type col struct {
		typ     string
		notNull bool
		dflt    sql.NullString
	}
	cols := make(map[string]col)
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		cols[name] = col{typ: typ, notNull: notNull == 1, dflt: dflt}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if c, ok := cols["attempts"]; !ok {
		t.Errorf("post_queue has no attempts column: %v", cols)
	} else if c.typ != "INTEGER" || !c.notNull || c.dflt.String != "0" {
		t.Errorf("attempts column = %+v, want INTEGER NOT NULL DEFAULT 0", c)
	}
	if c, ok := cols["last_error"]; !ok {
		t.Errorf("post_queue has no last_error column: %v", cols)
	} else if c.typ != "TEXT" || c.notNull {
		t.Errorf("last_error column = %+v, want nullable TEXT", c)
	}

	var v string
	if err := mainDB.QueryRow("SELECT value FROM config WHERE key = ?", config.CFG_KEY_POSTQUEUE_MAXATTEMPTS).Scan(&v); err != nil {
		t.Fatalf("config %s: %v", config.CFG_KEY_POSTQUEUE_MAXATTEMPTS, err)
	}
	if v != "5" {
		t.Errorf("config %s = %q, want \"5\"", config.CFG_KEY_POSTQUEUE_MAXATTEMPTS, v)
	}
	if got := fresh.PostQueueMaxAttempts(); got != 5 {
		t.Errorf("PostQueueMaxAttempts on the fresh DB = %d, want 5", got)
	}
}

// TestFlPqMaxAttempts: a missing, non-numeric or <= 0 value reads as the default 5.
func TestFlPqMaxAttempts(t *testing.T) {
	db := w0DB(t)
	for _, c := range []struct {
		v    string
		want int
	}{
		{"7", 7}, {" 3 ", 3}, {"0", 5}, {"-2", 5}, {"abc", 5}, {"", 5}, {"5", 5},
	} {
		flPqSetMax(t, c.v)
		if got := db.PostQueueMaxAttempts(); got != c.want {
			t.Errorf("PostQueueMaxAttempts with %q = %d, want %d", c.v, got, c.want)
		}
	}
}

// TestFlPqAttemptsParkRetry: a counted failure increments attempts and returns the row to
// pending; at PostQueueMaxAttempts the row is parked (not selected, admin status "parked");
// an uncounted failure only stores last_error; raising the key un-parks; RetryPostQueueEntry
// resets the counter and refuses posted and in-processing rows.
func TestFlPqAttemptsParkRetry(t *testing.T) {
	db := w0DB(t)
	_, gid := adfOptPostQueueGroup(t)
	ids := adfOptPostQueueInsert(t, gid, "flpqpark", 1)
	id := ids[0]
	defer func() {
		if err := db.ResetAllPostQueueProcessing(); err != nil {
			t.Errorf("ResetAllPostQueueProcessing: %v", err)
		}
	}()
	flPqSetMax(t, "3")

	for i := 1; i <= 3; i++ {
		e := flPqSelect(t, gid, id)
		if e == nil {
			t.Fatalf("round %d: entry %d not selected", i, id)
		}
		if e.Attempts != i-1 {
			t.Errorf("round %d: selected entry Attempts = %d, want %d", i, e.Attempts, i-1)
		}
		if err := db.RecordPostQueueFailure(id, "441 rejected round "+strconv.Itoa(i), true); err != nil {
			t.Fatalf("RecordPostQueueFailure: %v", err)
		}
		attempts, lastErr := flPqRow(t, id)
		if attempts != i || lastErr != "441 rejected round "+strconv.Itoa(i) {
			t.Errorf("round %d: attempts=%d last_error=%q", i, attempts, lastErr)
		}
		if posted, inProc := adfOptPostQueueFlags(t, id); posted || inProc {
			t.Errorf("round %d: posted=%v in_processing=%v, want false, false", i, posted, inProc)
		}
	}

	// parked: not selected, status parked, counted in the parked stat
	if e := flPqSelect(t, gid, id); e != nil {
		t.Fatalf("parked entry %d selected (attempts %d)", id, e.Attempts)
	}
	if st := flPqStatus(t, id, "", "flpqpark."); st != "parked" {
		t.Errorf("status = %q, want parked", st)
	}
	if st := flPqStatus(t, id, "parked", "flpqpark."); st != "parked" {
		t.Errorf("filter parked: status %q, want parked", st)
	}
	if st := flPqStatus(t, id, "pending", "flpqpark."); st != "" {
		t.Errorf("filter pending lists the parked entry (status %q)", st)
	}
	stats, err := db.GetPostQueueStats()
	if err != nil {
		t.Fatalf("GetPostQueueStats: %v", err)
	}
	if stats["parked"] < 1 || stats["total"] != stats["pending"]+stats["parked"]+stats["processing"]+stats["completed"] {
		t.Errorf("GetPostQueueStats = %v, want parked >= 1 and total = the sum", stats)
	}

	// raising the key un-parks: the entry is selected again
	flPqSetMax(t, "4")
	if st := flPqStatus(t, id, "", "flpqpark."); st != "pending" {
		t.Errorf("status after raising the max = %q, want pending", st)
	}
	if e := flPqSelect(t, gid, id); e == nil {
		t.Fatalf("entry %d not selected after raising the max", id)
	}

	// an uncounted failure (connection error) stores last_error only
	if err := db.RecordPostQueueFailure(id, "dial tcp: connection refused", false); err != nil {
		t.Fatalf("RecordPostQueueFailure(uncounted): %v", err)
	}
	if attempts, lastErr := flPqRow(t, id); attempts != 3 || lastErr != "dial tcp: connection refused" {
		t.Errorf("after uncounted failure: attempts=%d last_error=%q, want 3, the connection error", attempts, lastErr)
	}
	if _, inProc := adfOptPostQueueFlags(t, id); inProc {
		t.Errorf("uncounted failure left in_processing set")
	}

	// a long error text is capped
	if err := db.RecordPostQueueFailure(id, strings.Repeat("x", 3*postQueueLastErrorMax), false); err != nil {
		t.Fatalf("RecordPostQueueFailure(long): %v", err)
	}
	if _, lastErr := flPqRow(t, id); len(lastErr) != postQueueLastErrorMax {
		t.Errorf("stored last_error has %d bytes, want %d", len(lastErr), postQueueLastErrorMax)
	}

	// retry resets attempts and last_error
	flPqSetMax(t, "3")
	if err := db.RecordPostQueueFailure(id, "441 again", true); err != nil {
		t.Fatal(err)
	}
	if st := flPqStatus(t, id, "", "flpqpark."); st != "parked" {
		t.Fatalf("status = %q, want parked before the retry", st)
	}
	if err := db.RetryPostQueueEntry(id); err != nil {
		t.Fatalf("RetryPostQueueEntry: %v", err)
	}
	if attempts, lastErr := flPqRow(t, id); attempts != 0 || lastErr != "" {
		t.Errorf("after retry: attempts=%d last_error=%q, want 0, \"\"", attempts, lastErr)
	}
	if st := flPqStatus(t, id, "pending", "flpqpark."); st != "pending" {
		t.Errorf("status after retry = %q, want pending", st)
	}

	// retry and delete refuse a row in processing
	if e := flPqSelect(t, gid, id); e == nil {
		t.Fatalf("entry %d not selected after retry", id)
	}
	if err := db.RetryPostQueueEntry(id); !errors.Is(err, ErrPostQueueEntryUnchanged) {
		t.Errorf("RetryPostQueueEntry(in processing) = %v, want ErrPostQueueEntryUnchanged", err)
	}
	if err := db.DeletePostQueueEntry(id); !errors.Is(err, ErrPostQueueEntryUnchanged) {
		t.Errorf("DeletePostQueueEntry(in processing) = %v, want ErrPostQueueEntryUnchanged", err)
	}

	// retry refuses a posted row
	if err := db.MarkPostQueueAsPostedToRemote(id); err != nil {
		t.Fatal(err)
	}
	if err := db.RetryPostQueueEntry(id); !errors.Is(err, ErrPostQueueEntryUnchanged) {
		t.Errorf("RetryPostQueueEntry(posted) = %v, want ErrPostQueueEntryUnchanged", err)
	}
	if posted, _ := adfOptPostQueueFlags(t, id); !posted {
		t.Errorf("RetryPostQueueEntry reset posted_to_remote of a posted row")
	}
	if err := db.RetryPostQueueEntry(-1); !errors.Is(err, ErrPostQueueEntryUnchanged) {
		t.Errorf("RetryPostQueueEntry(unknown id) = %v, want ErrPostQueueEntryUnchanged", err)
	}
}

// TestFlPqDeletePending: a pending (never posted) row can be deleted now; before, only posted
// rows could, although the admin page offered the button for every row not in processing.
func TestFlPqDeletePending(t *testing.T) {
	db := w0DB(t)
	_, gid := adfOptPostQueueGroup(t)
	id := adfOptPostQueueInsert(t, gid, "flpqdelete", 1)[0]
	if err := db.DeletePostQueueEntry(id); err != nil {
		t.Fatalf("DeletePostQueueEntry(pending): %v", err)
	}
	var n int
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT COUNT(*) FROM post_queue WHERE id = ?", []interface{}{id}, &n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("pending row %d still there after DeletePostQueueEntry", id)
	}
	if err := db.DeletePostQueueEntry(id); !errors.Is(err, ErrPostQueueEntryUnchanged) {
		t.Errorf("second DeletePostQueueEntry = %v, want ErrPostQueueEntryUnchanged", err)
	}
}
