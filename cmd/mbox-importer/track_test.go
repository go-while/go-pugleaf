package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mtQuiet sends the log output of a test to io.Discard and restores it afterwards (the
// tracker installs its own log tee, the cleanup puts the test's writer back).
func mtQuiet(t *testing.T) {
	t.Helper()
	old := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(old) })
}

// mtOpen opens a tracking DB at dir/mbox-import.sq3 with the given options.
func mtOpen(t *testing.T, dir string, reimport, logDB bool) *tracker {
	t.Helper()
	tr, err := openTracker(filepath.Join(dir, trackDBName), []string{"-data", dir}, reimport, logDB, 0)
	if err != nil {
		t.Fatalf("openTracker: %v", err)
	}
	return tr
}

// mtPlanned is one planned input for path, with the size and mtime it has on disk.
func mtPlanned(t *testing.T, path string) plannedInput {
	t.Helper()
	in := plannedFromPaths([]string{path})
	if len(in) != 1 {
		t.Fatalf("plannedFromPaths(%q): %d inputs", path, len(in))
	}
	return in[0]
}

// mtRaw opens the tracking DB of dir for the test's own reads.
func mtRaw(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, trackDBName)+"?_busy_timeout=10000")
	if err != nil {
		t.Fatalf("open tracking db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close tracking db: %v", err)
		}
	})
	return db
}

// mtStatusOf returns the status and reason of the input row of path.
func mtStatusOf(t *testing.T, db *sql.DB, path string) (string, string) {
	t.Helper()
	var status, reason string
	err := db.QueryRow(`SELECT status, COALESCE(reason, '') FROM inputs WHERE path = ?`, path).Scan(&status, &reason)
	if err != nil {
		t.Fatalf("status of %q: %v", path, err)
	}
	return status, reason
}

func TestMboxTrackSchema(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	tr := mtOpen(t, dir, false, false)
	if tr.runID <= 0 {
		t.Errorf("run id %d", tr.runID)
	}
	tr.close(&runSummary{Inputs: 2, Total: newCounters("")}, 0)

	db := mtRaw(t, dir)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != trackSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, trackSchemaVersion)
	}
	for _, table := range []string{"runs", "inputs", "log"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
	var (
		finished string
		code     int
		inputs   int
	)
	if err := db.QueryRow(`SELECT COALESCE(finished, ''), COALESCE(exit_code, -1), inputs FROM runs WHERE id = ?`,
		tr.runID).Scan(&finished, &code, &inputs); err != nil {
		t.Fatalf("run row: %v", err)
	}
	if finished == "" || code != 0 || inputs != 2 {
		t.Errorf("run row finished=%q exit=%d inputs=%d", finished, code, inputs)
	}

	// a second open on the same file keeps the schema and adds a run
	tr2 := mtOpen(t, dir, false, false)
	if tr2.runID == tr.runID {
		t.Errorf("second run got the same id %d", tr2.runID)
	}
	tr2.close(nil, 0)
}

func TestMboxTrackNilTracker(t *testing.T) {
	var tr *tracker
	if err := tr.recordPlanned(plannedInput{Path: "x"}); err != nil {
		t.Errorf("recordPlanned on a nil tracker: %v", err)
	}
	id, state, err := tr.claimInput(plannedInput{Path: "x"})
	if id != 0 || state != claimTake || err != nil {
		t.Errorf("claimInput on a nil tracker = %d, %v, %v", id, state, err)
	}
	tr.finishInput(0, nil, false, false)
	tr.finishRun(nil, 0)
	tr.close(nil, 0)
}

func TestMboxTrackDBPath(t *testing.T) {
	if got := trackDBPath(trackDBOff, "/data"); got != "" {
		t.Errorf("trackDBPath(off) = %q", got)
	}
	if got := trackDBPath("", "/data"); got != filepath.Join("/data", "cfg", trackDBName) {
		t.Errorf("trackDBPath(\"\") = %q", got)
	}
	if got := trackDBPath("/t/x.sq3", "/data"); got != "/t/x.sq3" {
		t.Errorf("trackDBPath(path) = %q", got)
	}
}

func TestMboxTrackRecordPlanned(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	tr := mtOpen(t, dir, false, false)
	unsupported := plannedInput{Path: "/mirror/item/cd.iso", Source: sourceIAFetch, Identifier: "item",
		Name: "cd.iso", Size: 700, Reason: "iso"}
	supported := plannedInput{Path: "/mirror/item/g.mbox.zip", Source: sourceIAFetch, Identifier: "item",
		Name: "g.mbox.zip", Size: 10, Mtime: 5, MD5: "abc", Supported: true}
	for _, in := range []plannedInput{unsupported, supported} {
		if err := tr.recordPlanned(in); err != nil {
			t.Fatalf("recordPlanned %q: %v", in.Path, err)
		}
	}
	// recording twice must not duplicate the row
	if err := tr.recordPlanned(supported); err != nil {
		t.Fatalf("recordPlanned again: %v", err)
	}
	// a done row keeps its status when it is recorded again
	id, state, err := tr.claimInput(supported)
	if err != nil || state != claimTake {
		t.Fatalf("claimInput = %v, %v", state, err)
	}
	tr.finishInput(id, newCounters(supported.Path), false, false)
	if err := tr.recordPlanned(supported); err != nil {
		t.Fatalf("recordPlanned on a done row: %v", err)
	}
	tr.close(nil, 0)

	db := mtRaw(t, dir)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inputs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d input rows, want 2", n)
	}
	if status, reason := mtStatusOf(t, db, unsupported.Path); status != trackUnsupported || reason != "iso" {
		t.Errorf("iso row status=%q reason=%q", status, reason)
	}
	if status, _ := mtStatusOf(t, db, supported.Path); status != trackDone {
		t.Errorf("done row status=%q after recordPlanned", status)
	}
	var (
		identifier, name, md5 string
		size, mtime           int64
	)
	if err := db.QueryRow(`SELECT identifier, name, md5, size, mtime FROM inputs WHERE path = ?`,
		supported.Path).Scan(&identifier, &name, &md5, &size, &mtime); err != nil {
		t.Fatal(err)
	}
	if identifier != "item" || name != "g.mbox.zip" || md5 != "abc" || size != 10 || mtime != 5 {
		t.Errorf("row = %q %q %q %d %d", identifier, name, md5, size, mtime)
	}
}

func TestMboxTrackDoneSkipAndReimport(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	file := miWrite(t, t.TempDir(), "g.mbox", miFixture())

	tr := mtOpen(t, dir, false, false)
	in := mtPlanned(t, file)
	id, state, err := tr.claimInput(in)
	if err != nil || state != claimTake || id == 0 {
		t.Fatalf("first claim = %d, %v, %v", id, state, err)
	}
	c := newCounters(file)
	c.Read, c.Stored, c.Group = 3, 2, "t.mbox.alpha"
	c.Skipped[skipNoSubject] = 1
	tr.finishInput(id, c, false, false)
	tr.close(nil, 0)

	db := mtRaw(t, dir)
	var (
		status, detail, group string
		read, stored, skipped int
	)
	if err := db.QueryRow(`SELECT status, COALESCE(skipped_detail, ''), COALESCE(group_name, ''), read, stored, skipped
		FROM inputs WHERE path = ?`, file).Scan(&status, &detail, &group, &read, &stored, &skipped); err != nil {
		t.Fatal(err)
	}
	if status != trackDone || read != 3 || stored != 2 || skipped != 1 || detail != "no-subject=1" || group != "t.mbox.alpha" {
		t.Errorf("row = %q read=%d stored=%d skipped=%d detail=%q group=%q", status, read, stored, skipped, detail, group)
	}

	// the same file: already imported
	tr = mtOpen(t, dir, false, false)
	if _, state, err := tr.claimInput(mtPlanned(t, file)); err != nil || state != claimDone {
		t.Errorf("second claim = %v, %v, want claimDone", state, err)
	}
	// a changed file is imported again
	changed := in
	changed.Mtime++
	if _, state, err := tr.claimInput(changed); err != nil || state != claimTake {
		t.Errorf("claim of a changed file = %v, %v, want claimTake", state, err)
	}
	tr.finishInput(0, nil, false, false) // id 0: no row touched
	tr.close(nil, 0)

	// -reimport ignores the done state
	tr = mtOpen(t, dir, false, false)
	id, state, err = tr.claimInput(in)
	if err != nil || state != claimTake {
		t.Fatalf("claim after the change = %v, %v", state, err)
	}
	tr.finishInput(id, newCounters(file), false, false)
	tr.close(nil, 0)
	tr = mtOpen(t, dir, true, false)
	if _, state, err := tr.claimInput(in); err != nil || state != claimTake {
		t.Errorf("claim with -reimport = %v, %v, want claimTake", state, err)
	}
	tr.close(nil, 0)

	// a different md5 for the same size and mtime is a different file
	withMD5 := in
	withMD5.MD5 = "deadbeef"
	tr = mtOpen(t, dir, false, false)
	id, _, err = tr.claimInput(withMD5)
	if err != nil {
		t.Fatal(err)
	}
	tr.finishInput(id, newCounters(file), false, false)
	other := withMD5
	other.MD5 = "f00d"
	if _, state, err := tr.claimInput(other); err != nil || state != claimTake {
		t.Errorf("claim with another md5 = %v, %v, want claimTake", state, err)
	}
	tr.close(nil, 0)
}

func TestMboxTrackFinishStatus(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	tr := mtOpen(t, dir, false, false)
	errs := newCounters("/e")
	errs.Errors = 2
	commit := newCounters("/c")
	commit.CommitFailed = true
	cases := []struct {
		path       string
		c          *counters
		inputErr   bool
		stopped    bool
		status     string
		reasonPart string
	}{
		{"/ok", newCounters("/ok"), false, false, trackDone, ""},
		{"/stop", newCounters("/stop"), false, true, trackStopped, "signal"},
		{"/inputerr", nil, true, false, trackFailed, "input error"},
		{"/notopened", nil, false, false, trackFailed, "not opened"},
		{"/e", errs, false, false, trackFailed, "article error"},
		{"/c", commit, false, false, trackFailed, "commit"},
	}
	for _, tc := range cases {
		in := plannedInput{Path: tc.path, Source: sourceCLI, Supported: true}
		id, _, err := tr.claimInput(in)
		if err != nil {
			t.Fatalf("claim %s: %v", tc.path, err)
		}
		tr.finishInput(id, tc.c, tc.inputErr, tc.stopped)
	}
	tr.close(nil, 1)

	db := mtRaw(t, dir)
	for _, tc := range cases {
		status, reason := mtStatusOf(t, db, tc.path)
		if status != tc.status || !strings.Contains(reason, tc.reasonPart) {
			t.Errorf("%s = %q/%q, want %q containing %q", tc.path, status, reason, tc.status, tc.reasonPart)
		}
	}
}

func TestMboxTrackInterruptedAndHeld(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	in := plannedInput{Path: "/mirror/a.mbox", Source: sourceCLI, Size: 1, Mtime: 2, Supported: true}

	// a run that claims an input and never finishes it (a crash): the row stays importing
	tr := mtOpen(t, dir, false, false)
	if _, state, err := tr.claimInput(in); err != nil || state != claimTake {
		t.Fatalf("claim = %v, %v", state, err)
	}
	db := mtRaw(t, dir)
	if status, _ := mtStatusOf(t, db, in.Path); status != trackImporting {
		t.Fatalf("status after the claim = %q", status)
	}
	// the crashed run's row is still unfinished, but this process is alive, so pretend it
	// was another process by clearing the pid of the run
	runID := tr.runID
	tr.close(nil, 1)
	if _, err := db.Exec(`UPDATE runs SET finished = NULL WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE inputs SET status = ? WHERE path = ?`, trackImporting, in.Path); err != nil {
		t.Fatal(err)
	}

	// a live run of this host holds the row: it is left alone and skipped
	if _, err := db.Exec(`UPDATE runs SET pid = ? WHERE id = ?`, os.Getpid(), runID); err != nil {
		t.Fatal(err)
	}
	tr = mtOpen(t, dir, false, false)
	if status, _ := mtStatusOf(t, db, in.Path); status != trackImporting {
		t.Errorf("a live run's input became %q", status)
	}
	if _, state, err := tr.claimInput(in); err != nil || state != claimHeld {
		t.Errorf("claim of a held input = %v, %v, want claimHeld", state, err)
	}
	tr.close(nil, 0)

	// the holder is gone (pid 0 is never alive): the row becomes failed/interrupted
	if _, err := db.Exec(`UPDATE runs SET pid = 0 WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}
	tr = mtOpen(t, dir, false, false)
	status, reason := mtStatusOf(t, db, in.Path)
	if status != trackFailed || reason != "interrupted" {
		t.Errorf("after the sweep = %q/%q, want failed/interrupted", status, reason)
	}
	if _, state, err := tr.claimInput(in); err != nil || state != claimTake {
		t.Errorf("claim of a swept input = %v, %v, want claimTake", state, err)
	}
	tr.close(nil, 0)
}

func TestMboxTrackMarkDonePermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions")
	}
	mtQuiet(t)
	dir := t.TempDir()
	file := miWrite(t, dir, "g.mbox", miFixture())
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})

	// without the tracking DB the refused rename is an input error (today's behaviour)
	if marked, inputErr := markDoneInput(file, false); marked || !inputErr {
		t.Errorf("markDoneInput(tracked=false) = %t, %t, want false, true", marked, inputErr)
	}
	// with it, the DB holds the state and the rename failure is not an error
	if marked, inputErr := markDoneInput(file, true); marked || inputErr {
		t.Errorf("markDoneInput(tracked=true) = %t, %t, want false, false", marked, inputErr)
	}
	if miExists(file + ".done") {
		t.Error("the input was renamed after all")
	}
}

func TestMboxTrackMarkDoneExists(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	file := miWrite(t, dir, "g.mbox", miFixture())
	miWrite(t, dir, "g.mbox.done", "")
	// an existing INPUT.done is no input error, with or without the tracking DB
	for _, tracked := range []bool{false, true} {
		if marked, inputErr := markDoneInput(file, tracked); marked || inputErr {
			t.Errorf("markDoneInput(tracked=%t) = %t, %t, want false, false", tracked, marked, inputErr)
		}
	}
	// a rename that works
	other := miWrite(t, dir, "h.mbox", miFixture())
	if marked, inputErr := markDoneInput(other, true); !marked || inputErr {
		t.Errorf("markDoneInput of a writable input = %t, %t", marked, inputErr)
	}
	if !miExists(other + ".done") {
		t.Error("the input was not renamed")
	}
}

func TestMboxTrackFilePath(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`[MBOX-IMPORT] importing file="/a/b.mbox"`, "/a/b.mbox"},
		{`[MBOX-IMPORT] file="/a/b.mbox" read=3 stored=2`, "/a/b.mbox"},
		{`[MBOX-IMPORT] input error: file="/a/b.mbox": broken`, "/a/b.mbox"},
		{`[MBOX-IMPORT] file="/a/with \"quote\".mbox" read=1`, `/a/with "quote".mbox`},
		{`[MBOX-IMPORT] rejected msgid=<x@y> group=a.b: nope`, ""},
		{`[MBOX-IMPORT] file="unterminated`, ""},
		{"", ""},
	} {
		if got := trackFilePath(tc.line); got != tc.want {
			t.Errorf("trackFilePath(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestMboxTrackLogRows(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	tr := mtOpen(t, dir, false, true)
	if tr.tee == nil {
		t.Fatal("no log tee")
	}
	in := plannedInput{Path: "/mirror/a.mbox", Source: sourceCLI, Size: 1, Mtime: 2, Supported: true}
	id, _, err := tr.claimInput(in)
	if err != nil {
		t.Fatal(err)
	}
	log.Printf("[MBOX-IMPORT] importing file=%q", in.Path)
	log.Printf("[MBOX-IMPORT] rejected msgid=<x@y> group=a.b: nope")
	tr.finishInput(id, newCounters(in.Path), false, false)
	tr.close(&runSummary{Inputs: 1, Total: newCounters("")}, 0)
	// close drained and closed the writer
	select {
	case <-tr.tee.done:
	default:
		t.Error("the log writer is still running after close")
	}

	db := mtRaw(t, dir)
	rows, err := db.Query(`SELECT run_id, COALESCE(input_id, 0), msg FROM log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var withID, withoutID int
	for rows.Next() {
		var (
			runID, inputID int64
			msg            string
		)
		if err := rows.Scan(&runID, &inputID, &msg); err != nil {
			t.Fatal(err)
		}
		if runID != tr.runID {
			t.Errorf("log row run_id = %d, want %d", runID, tr.runID)
		}
		switch {
		case strings.Contains(msg, "importing file="):
			withID++
			if inputID != id {
				t.Errorf("log row input_id = %d, want %d (%q)", inputID, id, msg)
			}
		case strings.Contains(msg, "rejected msgid="):
			withoutID++
			if inputID != 0 {
				t.Errorf("log row input_id = %d, want none (%q)", inputID, msg)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if withID != 1 || withoutID != 1 {
		t.Errorf("log rows with an input id = %d, without = %d, want 1 and 1", withID, withoutID)
	}
}

func TestMboxTrackLogDisabled(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	tr := mtOpen(t, dir, false, false)
	if tr.tee != nil {
		t.Fatal("a log tee with -log-db=false")
	}
	log.Printf("[MBOX-IMPORT] nothing of this is stored")
	tr.close(nil, 0)
	db := mtRaw(t, dir)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d log rows with -log-db=false", n)
	}
}

// TestMboxTrackLogDrop pins that a log writer that cannot keep up drops lines instead of
// blocking the caller: the tee below has no reader, so its channel fills up.
func TestMboxTrackLogDrop(t *testing.T) {
	tee := &logTee{lines: make(chan trackLogLine, 2), done: make(chan struct{})}
	for i := 0; i < 10; i++ {
		if _, err := fmt.Fprintf(tee, "[MBOX-IMPORT] line %d\n", i); err != nil {
			t.Fatalf("tee write: %v", err)
		}
	}
	if got := tee.dropped.Load(); got != 8 {
		t.Errorf("dropped = %d, want 8 (capacity 2 of 10 lines)", got)
	}
	// a multi-line write is split, an empty line is not stored
	tee2 := &logTee{lines: make(chan trackLogLine, 10), done: make(chan struct{})}
	if _, err := tee2.Write([]byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}
	if n := len(tee2.lines); n != 2 {
		t.Errorf("%d queued lines, want 2", n)
	}
	// after closed, a write is a no-op (never a send on a closed channel)
	tee2.closed.Store(true)
	if _, err := tee2.Write([]byte("three\n")); err != nil {
		t.Fatal(err)
	}
	if n := len(tee2.lines); n != 2 {
		t.Errorf("%d queued lines after closed, want 2", n)
	}
}

// TestMboxTrackLogDropCounted pins that a dropped line is reported in the log table.
func TestMboxTrackLogDropCounted(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	tr := mtOpen(t, dir, false, true)
	tr.tee.dropped.Store(42)
	tr.close(nil, 0)
	db := mtRaw(t, dir)
	var msg string
	if err := db.QueryRow(`SELECT msg FROM log WHERE msg LIKE '%dropped%' ORDER BY id DESC LIMIT 1`).Scan(&msg); err != nil {
		t.Fatalf("drop count row: %v", err)
	}
	if !strings.Contains(msg, "42 log line(s) dropped") {
		t.Errorf("drop count row = %q", msg)
	}
}

func TestMboxTrackLogKeepRuns(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	path := filepath.Join(dir, trackDBName)
	var runIDs []int64
	for i := 0; i < 3; i++ {
		tr, err := openTracker(path, nil, false, true, 0)
		if err != nil {
			t.Fatalf("openTracker: %v", err)
		}
		log.Printf("[MBOX-IMPORT] run %d line", i)
		runIDs = append(runIDs, tr.runID)
		tr.close(nil, 0)
	}
	db := mtRaw(t, dir)
	for _, id := range runIDs {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM log WHERE run_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("run %d has no log rows", id)
		}
	}
	// the fourth run keeps the log rows of the newest two runs (itself and run 3)
	tr, err := openTracker(path, nil, false, true, 2)
	if err != nil {
		t.Fatalf("openTracker: %v", err)
	}
	last := tr.runID
	log.Printf("[MBOX-IMPORT] run %d line", len(runIDs))
	tr.close(nil, 0)
	for i, id := range runIDs {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM log WHERE run_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		wantRows := i == len(runIDs)-1
		if (n > 0) != wantRows {
			t.Errorf("run %d has %d log rows, want rows: %t", id, n, wantRows)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM log WHERE run_id = ?`, last).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Errorf("the newest run has no log rows")
	}
}

func TestMboxTrackStatus(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	path := filepath.Join(dir, trackDBName)

	// no database yet
	var buf strings.Builder
	if err := writeStatus(&buf, path, false); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	if !strings.Contains(buf.String(), "no tracking database yet") {
		t.Errorf("status of a missing db:\n%s", buf.String())
	}

	tr := mtOpen(t, dir, false, true)
	ok := plannedInput{Path: "/mirror/a.mbox", Source: sourceCLI, Size: 2048, Mtime: 1, Supported: true}
	bad := plannedInput{Path: "/mirror/b.mbox", Source: sourceCLI, Size: 1024, Mtime: 1, Supported: true}
	iso := plannedInput{Path: "/mirror/c.iso", Source: sourceIAFetch, Size: 4096, Reason: "iso"}
	id, _, err := tr.claimInput(ok)
	if err != nil {
		t.Fatal(err)
	}
	log.Printf("[MBOX-IMPORT] importing file=%q", ok.Path)
	c := newCounters(ok.Path)
	c.Read, c.Stored = 7, 6
	tr.finishInput(id, c, false, false)
	id, _, err = tr.claimInput(bad)
	if err != nil {
		t.Fatal(err)
	}
	tr.finishInput(id, nil, true, false)
	if err := tr.recordPlanned(iso); err != nil {
		t.Fatal(err)
	}
	tr.close(&runSummary{Inputs: 2, Total: c}, 1)

	buf.Reset()
	if err := writeStatus(&buf, path, false); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"tracking db: " + path,
		"inputs by status:",
		trackDone,
		trackFailed,
		trackUnsupported,
		"last failed inputs:",
		"/mirror/b.mbox",
		`reason="input error"`,
		"last run: id=",
		"exit=1",
		"read=7",
		"log rows: ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}

	buf.Reset()
	if err := writeStatus(&buf, path, true); err != nil {
		t.Fatalf("writeStatus json: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(buf.String()), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, buf.String())
	}
	if !report.Exists || report.DB != path {
		t.Errorf("report db=%q exists=%t", report.DB, report.Exists)
	}
	bytesOf := map[string]int64{}
	countOf := map[string]int64{}
	for _, c := range report.Inputs {
		bytesOf[c.Status] = c.Bytes
		countOf[c.Status] = c.Inputs
	}
	if countOf[trackDone] != 1 || bytesOf[trackDone] != 2048 {
		t.Errorf("done: %d inputs, %d bytes", countOf[trackDone], bytesOf[trackDone])
	}
	if countOf[trackUnsupported] != 1 || bytesOf[trackUnsupported] != 4096 {
		t.Errorf("unsupported: %d inputs, %d bytes", countOf[trackUnsupported], bytesOf[trackUnsupported])
	}
	if len(report.Failed) != 1 || report.Failed[0].Path != bad.Path {
		t.Errorf("failed inputs = %+v", report.Failed)
	}
	if report.LastRun == nil || report.LastRun.ExitCode == nil || *report.LastRun.ExitCode != 1 {
		t.Errorf("last run = %+v", report.LastRun)
	}
	if report.LogRows == 0 {
		t.Error("no log rows in the report")
	}
}

func TestMboxTrackBytes(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{2048, "2.0 KiB"},
		{3 * 1024 * 1024, "3.0 MiB"},
		{5 * 1024 * 1024 * 1024, "5.0 GiB"},
	} {
		if got := trackBytes(tc.n); got != tc.want {
			t.Errorf("trackBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestMboxTrackImportLoop pins the import loop with a tracking DB: an unsupported input is
// only recorded, a done one is not read again, a held one is left to the other run.
func TestMboxTrackImportLoop(t *testing.T) {
	mtQuiet(t)
	dir := t.TempDir()
	inDir := t.TempDir()
	a := miWrite(t, inDir, "a.mbox", miFixture())
	b := miWrite(t, inDir, "b.mbox", miFixture())

	tr := mtOpen(t, dir, false, false)
	planned := plannedFromPaths([]string{a, b})
	iso := plannedInput{Path: filepath.Join(inDir, "c.iso"), Source: sourceIAFetch, Name: "c.iso", Reason: "iso"}
	res := importPlanned(append(planned, iso), &runOptions{tracked: true}, newCountingSink(), nil, tr)
	if res.Inputs != 2 || res.Unsupported != 1 || res.AlreadyImported != 0 {
		t.Fatalf("first run: inputs=%d unsupported=%d already=%d", res.Inputs, res.Unsupported, res.AlreadyImported)
	}
	if res.Total.Read != 6 {
		t.Errorf("read = %d, want 6", res.Total.Read)
	}
	tr.close(res, 0)

	// a second run imports nothing: both inputs are done, the iso is still unsupported
	tr = mtOpen(t, dir, false, false)
	res = importPlanned(append(plannedFromPaths([]string{a, b}), iso), &runOptions{tracked: true}, newCountingSink(), nil, tr)
	if res.Inputs != 0 || res.AlreadyImported != 2 || res.Unsupported != 1 || res.Total.Read != 0 {
		t.Errorf("second run: inputs=%d already=%d unsupported=%d read=%d",
			res.Inputs, res.AlreadyImported, res.Unsupported, res.Total.Read)
	}
	tr.close(res, 0)

	// an input a live run holds is skipped
	db := mtRaw(t, dir)
	if _, err := db.Exec(`UPDATE inputs SET status = ? WHERE path = ?`, trackImporting, a); err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err := db.QueryRow(`SELECT last_run FROM inputs WHERE path = ?`, a).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET finished = NULL, pid = ? WHERE id = ?`, os.Getpid(), runID); err != nil {
		t.Fatal(err)
	}
	tr = mtOpen(t, dir, false, false)
	res = importPlanned(plannedFromPaths([]string{a, b}), &runOptions{tracked: true}, newCountingSink(), nil, tr)
	if res.HeldByRun != 1 || res.AlreadyImported != 1 || res.Inputs != 0 {
		t.Errorf("third run: held=%d already=%d inputs=%d", res.HeldByRun, res.AlreadyImported, res.Inputs)
	}
	tr.close(res, 0)
}

// TestMboxTrackRunWith pins the wiring of runWith: the run records its input, writes log
// rows and skips the input on the next run; -trackdb off writes nothing.
func TestMboxTrackRunWith(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	mtQuiet(t)
	data := filepath.Join(t.TempDir(), "data")
	in := miWrite(t, t.TempDir(), "g.mbox", miFixture())

	code, sum := miRun(t, "-data", data, "-mark-done=false", in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 3, 2, 0, 0, 0, "no-subject=1")
	trackPath := filepath.Join(data, "cfg", trackDBName)
	if !miExists(trackPath) {
		t.Fatalf("no tracking database at %s", trackPath)
	}
	db := mtRaw(t, filepath.Join(data, "cfg"))
	status, _ := mtStatusOf(t, db, in)
	if status != trackDone {
		t.Errorf("input status = %q, want %q", status, trackDone)
	}
	var logRows, withInput int
	if err := db.QueryRow(`SELECT COUNT(*) FROM log`).Scan(&logRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM log WHERE input_id IS NOT NULL`).Scan(&withInput); err != nil {
		t.Fatal(err)
	}
	if logRows == 0 || withInput == 0 {
		t.Errorf("%d log rows, %d with an input id", logRows, withInput)
	}
	var exit int
	if err := db.QueryRow(`SELECT exit_code FROM runs ORDER BY id DESC LIMIT 1`).Scan(&exit); err != nil {
		t.Fatal(err)
	}
	if exit != 0 {
		t.Errorf("run exit_code = %d", exit)
	}

	// the second run finds the same file done
	code, sum = miRun(t, "-data", data, "-mark-done=false", in)
	if code != 0 {
		t.Fatalf("second run exit %d", code)
	}
	if sum.AlreadyImported != 1 || sum.Inputs != 0 {
		t.Errorf("second run: already=%d inputs=%d", sum.AlreadyImported, sum.Inputs)
	}
	// -reimport reads it again
	code, sum = miRun(t, "-data", data, "-mark-done=false", "-reimport", in)
	if code != 0 {
		t.Fatalf("reimport exit %d", code)
	}
	if sum.AlreadyImported != 0 || sum.Inputs != 1 || sum.Total.Present != 2 {
		t.Errorf("reimport: already=%d inputs=%d present=%d", sum.AlreadyImported, sum.Inputs, sum.Total.Present)
	}

	// -trackdb off leaves the database alone
	var runsBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runsBefore); err != nil {
		t.Fatal(err)
	}
	if code, _ := miRun(t, "-data", data, "-mark-done=false", "-trackdb", trackDBOff, in); code != 0 {
		t.Fatalf("-trackdb off exit %d", code)
	}
	var runsAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runsAfter); err != nil {
		t.Fatal(err)
	}
	if runsAfter != runsBefore {
		t.Errorf("-trackdb off added %d run row(s)", runsAfter-runsBefore)
	}
}

// TestMboxTrackStatusFlag pins -status as a command line (it imports nothing and exits 0).
func TestMboxTrackStatusFlag(t *testing.T) {
	mtQuiet(t)
	data := filepath.Join(t.TempDir(), "data")
	if code, _ := miRun(t, "-data", data, "-status"); code != 0 {
		t.Errorf("-status on an empty data dir: exit %d", code)
	}
	if code, _ := miRun(t, "-data", data, "-status", "-json"); code != 0 {
		t.Errorf("-status -json: exit %d", code)
	}
	if code, _ := miRun(t, "-data", data, "-status", "-trackdb", trackDBOff); code != 2 {
		t.Errorf("-status -trackdb off: exit %d, want 2", code)
	}
	if miExists(data) {
		t.Errorf("-status created %s", data)
	}
}
