package history

// Remembered rejects (plan nntp-peering, slice history-rejects): the reject token round trip,
// an accept replacing a remembered reject, the refresh of an older reject and the prune of
// expired rejects. The reject day comes from the injected clock (setClock), never from a sleep.

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rejTestNow is the fixed base day of the test clock (2026-11-08 12:00:00 UTC)
var rejTestNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// rejTestClockAhead is far enough ahead of rejTestNow and of any real clock a running writer
// may have seen, so a step scheduled on either is due (no sleeps, no wall-clock asserts)
const rejTestClockAhead = 4000

// rejTestSetDay points the instance's clock at rejTestNow + days days
func rejTestSetDay(h *History, days int) {
	t := rejTestNow.AddDate(0, 0, days)
	h.setClock(func() time.Time { return t })
}

// rejTestFastPrune makes the prune goroutine wake up often, so one cycle over the 4096 tables
// takes about a second instead of minutes. Must be called before the instance is created.
func rejTestFastPrune(t *testing.T) {
	t.Helper()
	old := rejectPruneTick
	rejectPruneTick = 5 * time.Millisecond
	t.Cleanup(func() { rejectPruneTick = old })
}

// rejTestAddAndWait remembers messageID as rejected and waits for the commit
func rejTestAddAndWait(t *testing.T, h *History, messageID string) {
	t.Helper()
	h.AddReject(messageID)
	histTestWaitIdle(t, h)
}

func rejTestRow(t *testing.T, dir, messageID string) string {
	t.Helper()
	row, found := histTestRawRow(t, dir, messageID)
	if !found {
		t.Fatalf("no row for %s", messageID)
	}
	return row.String
}

func TestRejectTokenHelpers(t *testing.T) {
	if got := unixDay(time.Unix(secondsPerDay*20399+123, 0)); got != 20399 {
		t.Errorf("unixDay = %d, want 20399", got)
	}
	if got := rejectToken(20399); got != "r20399" {
		t.Errorf("rejectToken = %q, want r20399", got)
	}
	for _, tok := range []string{"r20399", "r0"} {
		if !isRejectToken(tok) {
			t.Errorf("isRejectToken(%q) = false", tok)
		}
	}
	for _, tok := range []string{"", "r", "5", "5:7"} {
		if isRejectToken(tok) {
			t.Errorf("isRejectToken(%q) = true", tok)
		}
	}
	cases := []struct {
		csv  string
		day  int64
		ok   bool
		locs int
	}{
		{"r20399", 20399, true, 0},
		{" r20399 ", 20399, true, 0},
		{"rx", 0, false, 0},
		{"", 0, false, 0},
		{"5:7", 0, false, 1},
		{"r20399,5:7", 0, false, 1}, // never written, but a mixed row keeps its locations
	}
	for _, c := range cases {
		day, ok := rejectCSVDay(c.csv)
		if day != c.day || ok != c.ok {
			t.Errorf("rejectCSVDay(%q) = %d, %v; want %d, %v", c.csv, day, ok, c.day, c.ok)
		}
		if locs := parseLocations(c.csv); len(locs) != c.locs {
			t.Errorf("parseLocations(%q) = %v, want %d locations", c.csv, locs, c.locs)
		}
	}
}

func TestRejectRoundTrip(t *testing.T) {
	h, dir := histTestNew(t, nil)
	rejTestSetDay(h, 0)
	const m = "<reject-roundtrip@test.invalid>"

	rejTestAddAndWait(t, h, m)

	if want := rejectToken(unixDay(rejTestNow)); rejTestRow(t, dir, m) != want {
		t.Errorf("row = %q, want %q", rejTestRow(t, dir, m), want)
	}
	// known to the feed path: CHECK 438 / IHAVE 435 / TAKETHIS 439
	if ok, err := h.Exists(m); err != nil || !ok {
		t.Errorf("Exists = %v, %v; want true, nil", ok, err)
	}
	// but without any location
	if ids := histTestGroups(t, h, m); len(ids) != 0 || ids == nil {
		t.Errorf("LookupGroups = %v, want an empty non-nil slice", ids)
	}
	locs, err := h.LookupLocations(m)
	if err != nil || len(locs) != 0 || locs == nil {
		t.Errorf("LookupLocations = %v, %v; want an empty non-nil slice", locs, err)
	}
	batch, err := h.LookupLocationsBatch([]string{m, "<reject-roundtrip-unknown@test.invalid>"})
	if err != nil {
		t.Fatalf("LookupLocationsBatch: %v", err)
	}
	got, ok := batch[m]
	if !ok || len(got) != 0 {
		t.Errorf("LookupLocationsBatch[%s] = %v, %v; want an empty slice", m, got, ok)
	}
	if len(batch) != 1 {
		t.Errorf("LookupLocationsBatch = %v, want only the known id", batch)
	}
	// the row is recognisable as a reject from the CSV alone (IsRejectToken is what readers
	// outside this package use)
	if !IsRejectToken(rejTestRow(t, dir, m)) {
		t.Errorf("IsRejectToken(%q) = false", rejTestRow(t, dir, m))
	}
	// an empty id is never queued
	h.AddReject("")
	histTestWaitIdle(t, h)
	if n := h.GetStats().TotalRejects; n != 1 {
		t.Errorf("TotalRejects = %d, want 1", n)
	}
}

func TestRejectRefreshAndAccept(t *testing.T) {
	h, dir := histTestNew(t, nil)

	t.Run("RefreshKeepsOneToken", func(t *testing.T) {
		const m = "<reject-refresh@test.invalid>"
		rejTestSetDay(h, 0)
		rejTestAddAndWait(t, h, m)
		rejTestSetDay(h, 3)
		rejTestAddAndWait(t, h, m)
		want := rejectToken(unixDay(rejTestNow.AddDate(0, 0, 3)))
		if got := rejTestRow(t, dir, m); got != want {
			t.Fatalf("row = %q, want %q", got, want)
		}
	})

	t.Run("AcceptReplacesReject", func(t *testing.T) {
		const m = "<reject-then-accept@test.invalid>"
		rejTestSetDay(h, 0)
		rejTestAddAndWait(t, h, m)
		h.AddArticleAt(m, 5, 9)
		histTestWaitIdle(t, h)
		if got := rejTestRow(t, dir, m); got != "5:9" {
			t.Fatalf("row = %q, want 5:9", got)
		}
		if ids := histTestGroups(t, h, m); !reflect.DeepEqual(ids, []int64{5}) {
			t.Fatalf("LookupGroups = %v, want [5]", ids)
		}
	})

	t.Run("AcceptWithoutNumberReplacesReject", func(t *testing.T) {
		const m = "<reject-then-accept-plain@test.invalid>"
		rejTestSetDay(h, 0)
		rejTestAddAndWait(t, h, m)
		h.AddArticle(m, 7)
		histTestWaitIdle(t, h)
		if got := rejTestRow(t, dir, m); got != "7" {
			t.Fatalf("row = %q, want 7", got)
		}
	})

	t.Run("RejectLeavesAnAcceptedRowAlone", func(t *testing.T) {
		const m = "<reject-after-accept@test.invalid>"
		h.AddArticleAt(m, 4, 11)
		histTestWaitIdle(t, h)
		rejTestSetDay(h, 0)
		rejTestAddAndWait(t, h, m)
		if got := rejTestRow(t, dir, m); got != "4:11" {
			t.Fatalf("row = %q, want 4:11 (an accepted row is known anyway)", got)
		}
	})
}

func TestRejectPrune(t *testing.T) {
	h, dir := histTestNew(t, nil)
	const (
		old    = "<reject-prune-old@test.invalid>"
		fresh  = "<reject-prune-fresh@test.invalid>"
		stored = "<reject-prune-stored@test.invalid>"
	)
	rejTestSetDay(h, -40)
	rejTestAddAndWait(t, h, old)
	rejTestSetDay(h, -1)
	rejTestAddAndWait(t, h, fresh)
	h.AddArticleAt(stored, 3, 17)
	histTestWaitIdle(t, h)
	rejTestSetDay(h, 0)

	// 0 = keep rejects as long as history
	if n, err := h.PruneRejects(0, nil); err != nil || n != 0 {
		t.Fatalf("PruneRejects(0) = %d, %v; want 0, nil", n, err)
	}
	if ok, err := h.Exists(old); err != nil || !ok {
		t.Fatalf("Exists(old) after PruneRejects(0) = %v, %v; want true", ok, err)
	}

	n, err := h.PruneRejects(30, nil)
	if err != nil {
		t.Fatalf("PruneRejects(30): %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneRejects(30) = %d, want 1", n)
	}
	if ok, err := h.Exists(old); err != nil || ok {
		t.Errorf("Exists(old) = %v, %v; want false (pruned)", ok, err)
	}
	if _, found := histTestRawRow(t, dir, old); found {
		t.Errorf("row of the expired reject still exists")
	}
	if ok, err := h.Exists(fresh); err != nil || !ok {
		t.Errorf("Exists(fresh) = %v, %v; want true", ok, err)
	}
	if got := rejTestRow(t, dir, stored); got != "3:17" {
		t.Errorf("row of the stored article = %q, want 3:17", got)
	}
	if got := h.GetStats().RejectsPruned; got != 1 {
		t.Errorf("RejectsPruned = %d, want 1", got)
	}

	// an already closed stop channel ends the walk right away
	stop := make(chan struct{})
	close(stop)
	if n, err := h.PruneRejectsBefore(unixDay(rejTestNow)+1, stop); err != nil || n != 0 {
		t.Errorf("PruneRejectsBefore with a closed stop = %d, %v; want 0, nil", n, err)
	}
	if ok, err := h.Exists(fresh); err != nil || !ok {
		t.Errorf("Exists(fresh) after the stopped walk = %v, %v; want true", ok, err)
	}

	// without the stop channel the cutoff takes the fresh reject too
	if n, err := h.PruneRejectsBefore(unixDay(rejTestNow)+1, nil); err != nil || n != 1 {
		t.Fatalf("PruneRejectsBefore = %d, %v; want 1, nil", n, err)
	}
	if ok, err := h.Exists(fresh); err != nil || ok {
		t.Errorf("Exists(fresh) = %v, %v; want false (pruned)", ok, err)
	}
}

func TestRejectPruneManyBatches(t *testing.T) {
	h, _ := histTestNew(t, nil)
	rejTestSetDay(h, -40)
	// more ids than one batch of one table can hold: they spread over the shard tables, so
	// this only proves that a multi-row prune deletes every expired reject
	const n = pruneRejectBatch + 50
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("<reject-batch-%d@test.invalid>", i)
		ids = append(ids, id)
		h.AddReject(id)
	}
	histTestWaitIdle(t, h)
	rejTestSetDay(h, 0)
	deleted, err := h.PruneRejects(30, nil)
	if err != nil {
		t.Fatalf("PruneRejects: %v", err)
	}
	if deleted != int64(n) {
		t.Fatalf("PruneRejects = %d, want %d", deleted, n)
	}
	for _, id := range ids {
		if ok, err := h.Exists(id); err != nil || ok {
			t.Fatalf("Exists(%s) = %v, %v; want false", id, ok, err)
		}
	}
}

func TestRejectBackgroundPrune(t *testing.T) {
	rejTestFastPrune(t)
	h, dir := histTestNew(t, nil)
	h.SetRejectDaysFunc(func() int { return 30 })
	const m = "<reject-writer-prune@test.invalid>"
	rejTestSetDay(h, -40)
	rejTestAddAndWait(t, h, m)

	// The prune goroutine schedules its first cycle rejectPruneFirstDelay after its start,
	// measured on the clock it saw then (the real one until the test replaced it): a clock far
	// enough ahead of both is past that schedule without any sleep.
	rejTestSetDay(h, rejTestClockAhead)
	deadline := time.Now().Add(testWait)
	for {
		if _, found := histTestRawRow(t, dir, m); !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the prune goroutine did not remove the expired reject")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ok, err := h.Exists(m); err != nil || ok {
		t.Errorf("Exists = %v, %v; want false (pruned in the background)", ok, err)
	}
}

func TestRejectBackgroundPruneKeepsRejectsWithoutRetention(t *testing.T) {
	h, dir := histTestNew(t, nil)
	const m = "<reject-writer-keep@test.invalid>"
	rejTestSetDay(h, -400)
	rejTestAddAndWait(t, h, m)
	// no SetRejectDaysFunc: nothing is ever pruned automatically
	rejTestSetDay(h, rejTestClockAhead)
	for i := 0; i < 5; i++ {
		h.AddReject(fmt.Sprintf("<reject-writer-keep-%d@test.invalid>", i))
		histTestWaitIdle(t, h)
	}
	if _, found := histTestRawRow(t, dir, m); !found {
		t.Fatalf("a reject was pruned although no retention is wired")
	}
}

func TestRejectDisabledHistory(t *testing.T) {
	histTestEnable(t, false)
	h, err := NewHistory(histTestConfig(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	h.AddReject("<reject-disabled@test.invalid>")
	if ok, err := h.Exists("<reject-disabled@test.invalid>"); err != nil || ok {
		t.Errorf("Exists = %v, %v; want false, nil", ok, err)
	}
	if n, err := h.PruneRejects(30, nil); err != nil || n != 0 {
		t.Errorf("PruneRejects = %d, %v; want 0, nil", n, err)
	}
	if n := h.GetStats().TotalRejects; n != 0 {
		t.Errorf("TotalRejects = %d, want 0", n)
	}
}

func TestRejectDeleteQuery(t *testing.T) {
	q := deleteRejectsQuery("_ab", 3)
	if !strings.Contains(q, "DELETE FROM _ab WHERE message_id IN (?,?,?)") {
		t.Errorf("query = %q", q)
	}
	if !strings.Contains(q, sqlRejectOnlyRow) {
		t.Errorf("query without the reject-only condition: %q", q)
	}
}

// TestRejectPartialIndex: every table carries the partial index over the reject tokens, the
// expired-reject select uses it, and the prune re-creates it on an index written before this
// plan (lazily, one table per step).
func TestRejectPartialIndex(t *testing.T) {
	h, dir := histTestNew(t, nil)
	raw := rejTestRawDB(t, dir, 0)

	if !rejTestIndexExists(t, raw, "_00") {
		t.Fatalf("a newly created table has no reject index")
	}
	plan := rejTestQueryPlan(t, raw, fmt.Sprintf(sqlSelectExpiredRejects, "_00"), 1, pruneRejectBatch)
	if !strings.Contains(plan, "_00_rej") {
		t.Errorf("the expired-reject select does not use the partial index:\n%s", plan)
	}

	// an index written before this plan has no reject index: the prune creates it
	if _, err := raw.Exec("DROP INDEX _00_rej"); err != nil {
		t.Fatalf("DROP INDEX: %v", err)
	}
	if rejTestIndexExists(t, raw, "_00") {
		t.Fatalf("the reject index is still there after DROP INDEX")
	}
	if _, err := h.PruneRejectsBefore(unixDay(rejTestNow), nil); err != nil {
		t.Fatalf("PruneRejectsBefore: %v", err)
	}
	if !rejTestIndexExists(t, raw, "_00") {
		t.Errorf("the prune walk did not create the reject index of _00")
	}
}

// rejTestRawDB opens one history database file with its own connection
func rejTestRawDB(t *testing.T, dir string, dbIndex int) *sql.DB {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("hashdb_%x.sqlite3", dbIndex))
	db, err := sql.Open(historyDriverName, path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func rejTestIndexExists(t *testing.T, db *sql.DB, tableName string) bool {
	t.Helper()
	var n int
	err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", tableName+"_rej").Scan(&n)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	return n == 1
}

func rejTestQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return plan.String()
}

// rejTestLogBuf captures the standard logger (the prune goroutine logs from its own goroutine)
type rejTestLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *rejTestLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *rejTestLogBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func rejTestCaptureLog(t *testing.T) *rejTestLogBuf {
	t.Helper()
	l := &rejTestLogBuf{}
	prev := log.Writer()
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(prev) })
	return l
}

// TestRejectBackgroundPruneStopsAtZeroRetention: the retention is read again before every batch,
// so setting HistoryRejectDays to 0 ends the running walk and keeps the remaining rejects.
func TestRejectBackgroundPruneStopsAtZeroRetention(t *testing.T) {
	rejTestFastPrune(t)
	logs := rejTestCaptureLog(t)
	h, _ := histTestNew(t, nil)
	// 30 days when the cycle starts, 0 from the first batch on
	var calls atomic.Int64
	h.SetRejectDaysFunc(func() int {
		if calls.Add(1) == 1 {
			return 30
		}
		return 0
	})
	const m = "<reject-zero-retention@test.invalid>"
	rejTestSetDay(h, -40)
	rejTestAddAndWait(t, h, m)
	rejTestSetDay(h, rejTestClockAhead)

	deadline := time.Now().Add(testWait)
	for !strings.Contains(logs.String(), "retention is 0") {
		if time.Now().After(deadline) {
			t.Fatalf("the walk did not stop on a retention of 0:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ok, err := h.Exists(m); err != nil || !ok {
		t.Errorf("Exists = %v, %v; want true (the stopped walk keeps the reject)", ok, err)
	}
}
