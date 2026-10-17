package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// testWait is the hang guard of this package's tests: it only stops a hung test, a passing test
// never waits on it. Override with PUGLEAF_TEST_DEADLINE (a Go duration, e.g. 120s); default 60 s.
var testWait = testWaitFromEnv(60 * time.Second)

func testWaitFromEnv(def time.Duration) time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func TestMain(m *testing.M) {
	// the same settings runWith uses, also for the test's own opens (check); the batch
	// interval is lowered before any database is opened (it is read at open)
	database.NO_CACHE_BOOT = true
	database.DBidleTimeOut = 15 * time.Second
	oldInterval := database.BatchInterval
	database.BatchInterval = 100 * time.Millisecond
	code := m.Run()
	database.BatchInterval = oldInterval
	os.Exit(code)
}

// miNeedDB skips a test that opens a database: OpenDatabase refuses euid < 1000. Only these
// tests are skipped, the parser tests of the package still run.
func miNeedDB(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" && os.Geteuid() < 1000 {
		t.Skip("OpenDatabase refuses to run with euid < 1000")
	}
}

// miArticle is one mbox message; subject "" leaves the Subject header out.
func miArticle(msgid, newsgroups, subject string, extra ...string) string {
	var b strings.Builder
	b.WriteString("From nobody Thu Dec  8 13:43:59 2016\n")
	b.WriteString("Path: a.example!not-for-mail\n")
	b.WriteString("From: Tester <tester@example.invalid>\n")
	b.WriteString("Newsgroups: " + newsgroups + "\n")
	if subject != "" {
		b.WriteString("Subject: " + subject + "\n")
	}
	b.WriteString("Date: Tue, 24 Jun 2003 19:30:12 +0200\n")
	b.WriteString("Message-ID: " + msgid + "\n")
	for _, h := range extra {
		b.WriteString(h + "\n")
	}
	b.WriteString("Xref: news.example " + strings.Split(newsgroups, ",")[0] + ":1\n")
	b.WriteString("\nbody of " + msgid + "\n\n")
	return b.String()
}

const (
	miAlpha = "t.mbox.alpha"
	miBeta  = "t.mbox.beta"
	miID1   = "<one@mbox.test.invalid>"
	miID2   = "<two@mbox.test.invalid>"
	miID3   = "<three@mbox.test.invalid>"
)

// miFixture is three articles: one for alpha (crossposted to beta), one for beta and one
// for alpha without a Subject (skipped).
func miFixture() string {
	return miArticle(miID1, miAlpha+","+miBeta, "first") +
		miArticle(miID2, miBeta, "Re: first", "References: "+miID1) +
		miArticle(miID3, miAlpha, "")
}

// miWrite writes content to dir/name and returns the path.
func miWrite(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func miExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// miSetup sets the hostname and replaces newProcessor with a hand-built processor (no DNS
// lookup, history under <data>/history), both restored by the test cleanup.
func miSetup(t *testing.T) {
	t.Helper()
	oldHost := processor.LocalNNTPHostname
	oldNew := newProcessor
	processor.LocalNNTPHostname = "test.invalid"
	newProcessor = miNewProcessor
	t.Cleanup(func() {
		processor.LocalNNTPHostname = oldHost
		newProcessor = oldNew
	})
}

// miNewProcessor is processor.NewProcessorE without the hostname check
// (cmd/nntp-server/server_e2e_test.go does the same).
func miNewProcessor(db *database.Database, hashLen int) (*processor.Processor, error) {
	cfg := history.DefaultConfig()
	cfg.HistoryDir = filepath.Join(db.GetDataDir(), "history")
	cfg.BatchTimeout = 50 // ms
	cfg.MaxConnections = 4
	cfg.UseShortHashLen = hashLen
	hist, err := history.NewHistory(cfg, db.WG)
	if err != nil {
		return nil, err
	}
	proc := &processor.Processor{
		DB:            db,
		History:       hist,
		MsgIdCache:    history.NewMsgIdItemCache(),
		ThreadCounter: processor.NewCounter(),
		LegacyCounter: processor.NewCounter(),
	}
	db.Batch.SetProcessor(proc)
	hist.SetDatabaseWorkChecker(db.Batch)
	return proc, nil
}

// miRun runs the tool with args and a fresh stop channel; it fails the test when the run
// does not return within testWait.
func miRun(t *testing.T, args ...string) (int, *runSummary) {
	t.Helper()
	return miRunStop(t, make(chan struct{}), args...)
}

func miRunStop(t *testing.T, stop chan struct{}, args ...string) (int, *runSummary) {
	t.Helper()
	type result struct {
		code int
		sum  *runSummary
	}
	done := make(chan result, 1)
	go func() {
		code := runWith(args, stop)
		done <- result{code, lastRun}
	}()
	select {
	case r := <-done:
		return r.code, r.sum
	case <-time.After(testWait):
		t.Fatalf("runWith %q did not return within %v", args, testWait)
		return 0, nil
	}
}

// miWithDB opens the database of dir, runs fn and shuts it down again (one Database per
// process: never while runWith runs).
func miWithDB(t *testing.T, dir string, fn func(db *database.Database)) {
	t.Helper()
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		t.Fatalf("OpenDatabase %s: %v", dir, err)
	}
	defer func() {
		if !shutdown(nil, db) {
			t.Error("database shutdown failed")
		}
	}()
	fn(db)
}

// miStoredIn reports whether msgid is stored in group.
func miStoredIn(t *testing.T, db *database.Database, group, msgid string) bool {
	t.Helper()
	g, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer g.Return()
	ok, err := g.LookupMsgIdInArticlesDB(msgid)
	if err != nil {
		t.Fatalf("lookup %s in %s: %v", msgid, group, err)
	}
	return ok
}

// miCheckGroups checks that every group of want exists and holds exactly the given ids
// among ids, and that no group of absent exists.
func miCheckGroups(t *testing.T, dir string, want map[string][]string, ids []string, absent ...string) {
	t.Helper()
	miWithDB(t, dir, func(db *database.Database) {
		for _, g := range absent {
			if _, err := db.MainDBGetNewsgroup(g); err == nil {
				t.Errorf("group %s exists", g)
			}
		}
		for group, has := range want {
			if _, err := db.MainDBGetNewsgroup(group); err != nil {
				t.Errorf("group %s: %v", group, err)
				continue
			}
			for _, id := range ids {
				wantIn := false
				for _, h := range has {
					wantIn = wantIn || h == id
				}
				if got := miStoredIn(t, db, group, id); got != wantIn {
					t.Errorf("%s in %s = %t, want %t", id, group, got, wantIn)
				}
			}
		}
	})
}

// miCheckHistory checks that the history index under dir knows every id.
func miCheckHistory(t *testing.T, dir string, ids ...string) {
	t.Helper()
	history.ENABLE_HISTORY = true
	cfg := history.DefaultConfig()
	cfg.HistoryDir = filepath.Join(dir, "history")
	cfg.BatchTimeout = 50
	h, err := history.NewHistory(cfg, nil)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	defer func() {
		if err := h.Close(); err != nil {
			t.Errorf("history close: %v", err)
		}
	}()
	for _, id := range ids {
		ok, err := h.Exists(id)
		if err != nil || !ok {
			t.Errorf("history Exists(%s) = %t, %v", id, ok, err)
		}
	}
}

// miCheck compares the totals of a run.
func miCheck(t *testing.T, sum *runSummary, read, stored, present, rejected, errs int, skipped string) {
	t.Helper()
	if sum == nil || sum.Total == nil {
		t.Fatal("no run summary")
	}
	c := sum.Total
	if c.Read != read || c.Stored != stored || c.Present != present || c.Rejected != rejected || c.Errors != errs || c.skippedDetail() != skipped {
		t.Errorf("totals read=%d stored=%d present=%d rejected=%d errors=%d skipped=%s, want %d %d %d %d %d %s",
			c.Read, c.Stored, c.Present, c.Rejected, c.Errors, c.skippedDetail(), read, stored, present, rejected, errs, skipped)
	}
}

func TestMboxCmdCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"help", []string{"-help"}, 0},
		{"h", []string{"-h"}, 0},
		{"unknown flag", []string{"-nope", "x.mbox"}, 2},
		{"no input", []string{"-data", t.TempDir()}, 2},
		{"bad group", []string{"-group", "bad/group!name", "x.mbox"}, 2},
		{"bad hash length", []string{"-useshorthashlen", "9", "x.mbox"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := miRun(t, tc.args...); code != tc.want {
				t.Errorf("exit %d, want %d", code, tc.want)
			}
		})
	}
}

func TestMboxCmdDryRun(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	in := miWrite(t, t.TempDir(), "g.mbox", miFixture())

	var buf bytes.Buffer
	log.SetOutput(&buf)
	code, sum := miRun(t, "-data", data, "-dry-run", in)
	log.SetOutput(os.Stderr) // the buffer is read only after the logger stopped writing to it

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if miExists(data) {
		t.Errorf("dry run created %s", data)
	}
	if !miExists(in) || miExists(in+".done") {
		t.Errorf("dry run renamed the input")
	}
	miCheck(t, sum, 3, 2, 0, 0, 0, "no-subject=1")
	out := buf.String()
	for _, want := range []string{
		fmt.Sprintf("[MBOX-IMPORT] dry-run group=%q articles=1", miAlpha),
		fmt.Sprintf("[MBOX-IMPORT] dry-run group=%q articles=1", miBeta),
		fmt.Sprintf("[MBOX-IMPORT] file=%q read=3 stored=2 present=0 skipped=1 (no-subject=1) rejected=0 errors=0 parser-skipped=0 near-misses=0 stopped-early=false unread=false marked-done=false", in),
		"[MBOX-IMPORT] done inputs=1 marked-done=0 input-errors=0 read=3 stored=2 present=0 skipped=1 rejected=0 errors=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n%s", want, out)
		}
	}
}

func TestMboxCmdImportAndRerun(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	in := miWrite(t, t.TempDir(), "g.mbox", miFixture())

	code, sum := miRun(t, "-data", data, in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 3, 2, 0, 0, 0, "no-subject=1")
	if miExists(in) || !miExists(in+".done") || sum.MarkedDone != 1 {
		t.Fatalf("input not renamed to .done (marked %d)", sum.MarkedDone)
	}
	ids := []string{miID1, miID2, miID3}
	miCheckGroups(t, data, map[string][]string{miAlpha: {miID1}, miBeta: {miID2}}, ids)
	miCheckHistory(t, data, miID1, miID2)

	// the renamed input again: everything present, nothing stored, the name is kept
	code, sum = miRun(t, "-data", data, in+".done")
	if code != 0 {
		t.Fatalf("rerun exit %d", code)
	}
	miCheck(t, sum, 3, 0, 2, 0, 0, "no-subject=1")
	if !miExists(in+".done") || miExists(in+".done.done") || sum.MarkedDone != 0 {
		t.Errorf("rerun renamed the .done input (marked %d)", sum.MarkedDone)
	}
}

// TestMboxCmdNoMarkDoneBadGroup: -mark-done=false keeps the name; an article whose first
// group is invalid is skipped (no fallback to a later group).
func TestMboxCmdNoMarkDoneBadGroup(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	const id4 = "<four@mbox.test.invalid>"
	in := miWrite(t, t.TempDir(), "g.mbox", miFixture()+miArticle(id4, "bad/group!name,"+miAlpha, "bad first group"))

	code, sum := miRun(t, "-data", data, "-mark-done=false", in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 4, 2, 0, 0, 0, "no-subject=1,bad-group=1")
	if !miExists(in) || miExists(in+".done") || sum.MarkedDone != 0 {
		t.Error("input renamed with -mark-done=false")
	}
	miCheckGroups(t, data, map[string][]string{miAlpha: {miID1}, miBeta: {miID2}}, []string{miID1, miID2, miID3, id4})
}

func TestMboxCmdForcedGroup(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	const forced = "t.mbox.forced"
	in := miWrite(t, t.TempDir(), "g.mbox", miFixture()+
		strings.Replace(miArticle("<four@mbox.test.invalid>", miAlpha, "no newsgroups"), "Newsgroups: "+miAlpha+"\n", "", 1))

	code, sum := miRun(t, "-data", data, "-group", forced, in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 4, 2, 0, 0, 0, "no-subject=1,no-newsgroups=1")
	miCheckGroups(t, data, map[string][]string{forced: {miID1, miID2}}, []string{miID1, miID2, miID3}, miAlpha, miBeta)
}

func TestMboxCmdStopped(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	dir := t.TempDir()
	a := miWrite(t, dir, "a.mbox", miFixture())
	b := miWrite(t, dir, "b.mbox", miFixture())
	stop := make(chan struct{})
	close(stop)

	code, sum := miRunStop(t, stop, "-data", data, a, b)
	if code != 130 {
		t.Fatalf("exit %d, want 130", code)
	}
	if !sum.Stopped || sum.Inputs != 0 || sum.Total.Read != 0 {
		t.Errorf("summary stopped=%t inputs=%d read=%d", sum.Stopped, sum.Inputs, sum.Total.Read)
	}
	if !miExists(a) || !miExists(b) || miExists(a+".done") || miExists(b+".done") {
		t.Error("an input was renamed")
	}
}

func TestMboxCmdDirectory(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	dir := t.TempDir()
	a := miWrite(t, dir, "a.mbox", miArticle(miID1, miAlpha, "a"))
	b := miWrite(t, dir, "b.mbox", miArticle(miID2, miBeta, "b"))

	code, sum := miRun(t, "-data", data, dir)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 2, 2, 0, 0, 0, "-")
	if sum.Inputs != 2 || sum.MarkedDone != 2 || !miExists(a+".done") || !miExists(b+".done") {
		t.Errorf("inputs=%d marked=%d", sum.Inputs, sum.MarkedDone)
	}
	miCheckGroups(t, data, map[string][]string{miAlpha: {miID1}, miBeta: {miID2}}, []string{miID1, miID2})
}

func TestMboxCmdMissingInput(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.mbox")
	in := miWrite(t, dir, "g.mbox", miFixture())

	code, sum := miRun(t, "-data", data, missing, in)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if sum.InputErrors != 1 || sum.MarkedDone != 1 {
		t.Errorf("input-errors=%d marked=%d", sum.InputErrors, sum.MarkedDone)
	}
	miCheck(t, sum, 3, 2, 0, 0, 0, "no-subject=1")
	if !miExists(in + ".done") {
		t.Error("the existing input was not imported and renamed")
	}
}

// TestMboxCmdOnlyMissingInput: no database is opened when no input exists.
func TestMboxCmdOnlyMissingInput(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	code, sum := miRun(t, "-data", data, filepath.Join(t.TempDir(), "missing.mbox"))
	if code != 1 || sum == nil || sum.InputErrors != 1 {
		t.Fatalf("exit %d summary %+v", code, sum)
	}
	if miExists(data) {
		t.Error("data directory created without an input")
	}
}

// TestMboxCmdSinkCases covers the per-article outcomes of readMbox with a fake sink.
func TestMboxCmdSinkCases(t *testing.T) {
	in := miWrite(t, t.TempDir(), "g.mbox", miArticle(miID1, miAlpha, "a")+miArticle(miID2, miAlpha, "b")+
		miArticle(miID3, miAlpha, "c")+miArticle("<four@mbox.test.invalid>", miAlpha, "d"))
	src, err := openSource(in, "7z")
	if err != nil {
		t.Fatal(err)
	}
	sink := &fakeSink{results: map[string]fakeResult{
		miID1:                      {stored: true},
		miID2:                      {present: true},
		miID3:                      {err: fmt.Errorf("%w: refused", errRejected)},
		"<four@mbox.test.invalid>": {err: errors.New("database down")},
	}}
	c, err := readMbox(src, &runOptions{}, sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := src.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if c.Read != 4 || c.Stored != 1 || c.Present != 1 || c.Rejected != 1 || c.Errors != 1 {
		t.Errorf("counters %+v", c)
	}
}

type fakeResult struct {
	stored, present bool
	err             error
}

type fakeSink struct {
	results map[string]fakeResult
}

func (f *fakeSink) Handle(msgid string, _ func() (*models.Article, error), _ string) (bool, bool, error) {
	r := f.results[msgid]
	return r.stored, r.present, r.err
}

func (f *fakeSink) beginInput() {}

func (f *fakeSink) endInput(time.Duration) error { return nil }

// stopSink closes stop when it sees its first article.
type stopSink struct {
	*countingSink
	stop chan struct{}
}

func (s *stopSink) Handle(msgid string, art func() (*models.Article, error), group string) (bool, bool, error) {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	return s.countingSink.Handle(msgid, art, group)
}

// TestMboxCmdStopBetweenArticles: a stop during an input finishes the current article, reads
// no further one, does not rename the input and starts no further input.
func TestMboxCmdStopBetweenArticles(t *testing.T) {
	dir := t.TempDir()
	a := miWrite(t, dir, "a.mbox", miFixture())
	b := miWrite(t, dir, "b.mbox", miFixture())
	stop := make(chan struct{})
	sink := &stopSink{countingSink: newCountingSink(), stop: stop}
	res := importInputs([]string{a, b}, &runOptions{markDone: true}, sink, stop)
	if !res.Stopped || res.exitCode() != 130 || res.Inputs != 1 || res.Total.Read != 1 || res.Total.Stored != 1 {
		t.Errorf("stopped=%t exit=%d inputs=%d read=%d stored=%d", res.Stopped, res.exitCode(), res.Inputs, res.Total.Read, res.Total.Stored)
	}
	if miExists(a+".done") || miExists(b+".done") {
		t.Error("an input was renamed after the stop")
	}
}
