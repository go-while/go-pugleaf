package database

import (
	"strings"
	"testing"
	"time"
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
	if stats["total"] != stats["pending"]+stats["processing"]+stats["completed"] {
		t.Errorf("GetPostQueueStats = %v: total != pending+processing+completed", stats)
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
	for !logs.contains("SQLite retry attempt", "for transaction") {
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
