package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
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
	if runtime.GOOS != "windows" && os.Geteuid() < 1000 {
		fmt.Println("post-queue: skipped: OpenDatabase refuses to run with euid < 1000")
		os.Exit(0)
	}
	// the same settings runWith uses, also for the test's own opens (seed and check)
	database.NO_CACHE_BOOT = true
	database.DBidleTimeOut = 15 * time.Second
	os.Exit(m.Run())
}

// pqOpen opens the database of data root dir. Only one Database may be open per process
// (database.INIT), so tests open, seed and close it before runWith opens it again.
func pqOpen(t *testing.T, dir string) *database.Database {
	t.Helper()
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		t.Fatalf("OpenDatabase %s: %v", dir, err)
	}
	return db
}

// pqClose shuts db down the way the tool does (StopChan, WG, Shutdown).
func pqClose(t *testing.T, db *database.Database) {
	t.Helper()
	if !shutdown(db) {
		t.Fatal("database shutdown failed")
	}
}

// pqWith opens the database of dir, runs fn and closes it again.
func pqWith(t *testing.T, dir string, fn func(db *database.Database)) {
	t.Helper()
	db := pqOpen(t, dir)
	defer pqClose(t, db)
	fn(db)
}

// pqGroup creates the active newsgroup name and returns its id.
func pqGroup(t *testing.T, db *database.Database, name string) int64 {
	t.Helper()
	g := &models.Newsgroup{Name: name, Active: true, Status: "y", LowWater: 1}
	if err := db.InsertNewsgroup(g); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	var id int64
	if err := database.RetryableQueryRowScan(db.GetMainDB(),
		"SELECT id FROM newsgroups WHERE name = ?", []interface{}{name}, &id); err != nil {
		t.Fatalf("id of %s: %v", name, err)
	}
	return id
}

// pqQueue queues msgID for groupID and returns the row id.
func pqQueue(t *testing.T, db *database.Database, groupID int64, msgID string) int64 {
	t.Helper()
	if err := db.InsertPostQueueEntry(groupID, msgID); err != nil {
		t.Fatalf("InsertPostQueueEntry %s: %v", msgID, err)
	}
	var id int64
	if err := database.RetryableQueryRowScan(db.GetMainDB(),
		"SELECT id FROM post_queue WHERE message_id = ?", []interface{}{msgID}, &id); err != nil {
		t.Fatalf("id of %s: %v", msgID, err)
	}
	return id
}

// pqQueuedArticles creates the active group name with n articles (article_num 1..n) in its
// group DB, queues each one and returns the post_queue row ids.
func pqQueuedArticles(t *testing.T, db *database.Database, name string, n int) []int64 {
	t.Helper()
	gid := pqGroup(t, db, name)
	groupDB, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", name, err)
	}
	defer groupDB.Return()
	var ids []int64
	for i := 1; i <= n; i++ {
		msgID := fmt.Sprintf("<pq.%d@%s.invalid>", i, name)
		if _, err := database.RetryableExec(groupDB.DB,
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
			"references", bytes, lines, reply_count, path, headers_json, body_text, downloaded)
			VALUES (?, ?, 'pq subject', 'poster <poster@test.invalid>', ?, 'Thu, 15 Oct 2026 10:00:00 +0000',
			'', 60, 1, 0, '.POSTED!not-for-mail', ?, 'pq body', 1)`,
			i, msgID, time.Now().UTC().Format("2006-01-02 15:04:05"), "Newsgroups: "+name); err != nil {
			t.Fatalf("insert article %s: %v", msgID, err)
		}
		ids = append(ids, pqQueue(t, db, gid, msgID))
	}
	return ids
}

// pqProvider adds an enabled posting provider for 127.0.0.1:port with one connection.
func pqProvider(t *testing.T, db *database.Database, name string, port int) {
	t.Helper()
	if err := db.AddProvider(&models.Provider{
		Name: name, Grp: name, Host: "127.0.0.1", Port: port, MaxConns: 1, Enabled: true, Posting: true,
	}); err != nil {
		t.Fatalf("AddProvider %s: %v", name, err)
	}
}

// pqRow is the state of one post_queue row.
type pqRow struct {
	posted, inProcessing bool
	attempts             int
	lastError            string
}

// pqState returns the rows of post_queue by id.
func pqState(t *testing.T, db *database.Database) map[int64]pqRow {
	t.Helper()
	rows, err := database.RetryableQuery(db.GetMainDB(),
		"SELECT id, posted_to_remote, in_processing, attempts, COALESCE(last_error, '') FROM post_queue")
	if err != nil {
		t.Fatalf("query post_queue: %v", err)
	}
	defer rows.Close()
	out := make(map[int64]pqRow)
	for rows.Next() {
		var id int64
		var r pqRow
		if err := rows.Scan(&id, &r.posted, &r.inProcessing, &r.attempts, &r.lastError); err != nil {
			t.Fatalf("scan post_queue: %v", err)
		}
		out[id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows post_queue: %v", err)
	}
	return out
}

// pqStateOf reopens the database of dir and returns pqState.
func pqStateOf(t *testing.T, dir string) map[int64]pqRow {
	t.Helper()
	var st map[int64]pqRow
	pqWith(t, dir, func(db *database.Database) { st = pqState(t, db) })
	return st
}

// pqStart runs runWith(args, stop) in a goroutine; the exit code arrives on the returned channel.
func pqStart(args []string, stop <-chan struct{}) <-chan int {
	ch := make(chan int, 1)
	go func() { ch <- runWith(args, stop) }()
	return ch
}

// pqWait waits for the exit code of a pqStart run, bounded by testWait.
func pqWait(t *testing.T, ch <-chan int) int {
	t.Helper()
	select {
	case code := <-ch:
		return code
	case <-time.After(testWait):
		t.Fatalf("runWith did not return within %v", testWait)
		return -1
	}
}

// pqRun runs the tool with args and a shutdown channel that is never closed.
func pqRun(t *testing.T, args ...string) int {
	t.Helper()
	return pqWait(t, pqStart(args, make(chan struct{})))
}

// TestPostQueueHelp: -help prints the usage examples and returns 0 without opening a database.
func TestPostQueueHelp(t *testing.T) {
	dir := t.TempDir()
	if code := pqRun(t, "-help", "-data", dir); code != 0 {
		t.Fatalf("-help exit = %d, want 0", code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("-help created %d entries in the data dir (first %q), want none", len(entries), entries[0].Name())
	}
}

// TestPostQueueBadFlag: an unknown flag returns 2 and -h (not defined, flag.ErrHelp) returns 0,
// as flag.ExitOnError exited before; neither opens a database.
func TestPostQueueBadFlag(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want int
	}{
		{"-no-such-flag", 2},
		{"-h", 0},
	} {
		dir := t.TempDir()
		if code := pqRun(t, tc.flag, "-data", dir); code != tc.want {
			t.Errorf("%s: exit = %d, want %d", tc.flag, code, tc.want)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("%s: data dir entries %d, err %v; want empty", tc.flag, len(entries), err)
		}
	}
}

// TestPostQueueCleanup: the cleanup flags delete rows and exit 0 without posting.
// CleanupPostedEntries only ever deletes posted rows: -cleanup-older N deletes posted rows older
// than N days (a pending old row stays), and together with -cleanup-posted-entries the age limit
// applies too (the help text: "Delete posted entries older than 30 days").
func TestPostQueueCleanup(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		gone []string // rows deleted, of: newPending, newPosted, oldPending, oldPosted
	}{
		{"posted", []string{"-cleanup-posted-entries"}, []string{"newPosted", "oldPosted"}},
		{"older", []string{"-cleanup-older", "7"}, []string{"oldPosted"}},
		{"both", []string{"-cleanup-posted-entries", "-cleanup-older", "7"}, []string{"oldPosted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ids := make(map[string]int64)
			pqWith(t, dir, func(db *database.Database) {
				gid := pqGroup(t, db, "pq.cleanup")
				for _, n := range []string{"newPending", "newPosted", "oldPending", "oldPosted"} {
					ids[n] = pqQueue(t, db, gid, "<"+n+"@pq.invalid>")
				}
				for _, n := range []string{"newPosted", "oldPosted"} {
					if err := db.MarkPostQueueAsPostedToRemote(ids[n]); err != nil {
						t.Fatalf("MarkPostQueueAsPostedToRemote %s: %v", n, err)
					}
				}
				for _, n := range []string{"oldPending", "oldPosted"} {
					if _, err := database.RetryableExec(db.GetMainDB(),
						"UPDATE post_queue SET created = datetime('now', '-30 days') WHERE id = ?", ids[n]); err != nil {
						t.Fatalf("backdate %s: %v", n, err)
					}
				}
			})

			if code := pqRun(t, append(tc.args, "-data", dir)...); code != 0 {
				t.Fatalf("exit = %d, want 0", code)
			}

			st := pqStateOf(t, dir)
			gone := make(map[string]bool)
			for _, n := range tc.gone {
				gone[n] = true
			}
			for n, id := range ids {
				if _, ok := st[id]; ok == gone[n] {
					t.Errorf("row %s: present=%v, want %v", n, ok, !gone[n])
				}
			}
			if len(st) != len(ids)-len(tc.gone) {
				t.Errorf("%d rows left, want %d", len(st), len(ids)-len(tc.gone))
			}
		})
	}
}

// TestPostQueueOpenFails: a -data that cannot hold the database (a regular file) returns 1.
func TestPostQueueOpenFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-data", file},
		{"-cleanup-posted-entries", "-data", file},
	} {
		if code := pqRun(t, args...); code != 1 {
			t.Errorf("%v: exit = %d, want 1", args, code)
		}
	}
}

// TestPostQueueNoProvider: without a posting-enabled provider the run returns 0 and posts
// nothing, but rows a killed earlier run left in_processing are back to pending first.
func TestPostQueueNoProvider(t *testing.T) {
	dir := t.TempDir()
	var ids []int64
	pqWith(t, dir, func(db *database.Database) {
		// precondition: the default providers OpenDatabase adds have posting off
		for _, p := range mustProviders(t, db) {
			if p.Posting {
				t.Fatalf("precondition: default provider %s has posting enabled", p.Name)
			}
		}
		ids = pqQueuedArticles(t, db, "pq.noprovider", 2)
		if _, err := database.RetryableExec(db.GetMainDB(),
			"UPDATE post_queue SET in_processing = 1 WHERE id = ?", ids[1]); err != nil {
			t.Fatalf("set in_processing: %v", err)
		}
	})

	if code := pqRun(t, "-data", dir); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	st := pqStateOf(t, dir)
	for _, id := range ids {
		if r := st[id]; r != (pqRow{}) {
			t.Errorf("row %d = %+v, want pending untouched", id, r)
		}
	}
}

func mustProviders(t *testing.T, db *database.Database) []*models.Provider {
	t.Helper()
	ps, err := db.GetProviders()
	if err != nil {
		t.Fatalf("GetProviders: %v", err)
	}
	return ps
}

// TestPostQueueOneShot: one run against a fake provider. A 240 marks the rows posted; a 441
// counts one attempt and stores the error; a refused connection (closed port) stores the error
// without an attempt. A failed post is not a tool error: every case exits 0 (the closed port too:
// the pool connects lazily, so pool creation succeeds and postToProvider fails with
// "failed to get connection", recorded as a transient failure).
func TestPostQueueOneShot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		port      func(t *testing.T) (port int, posts func() int64)
		want      pqRow
		wantPosts int64
	}{
		{"240", func(t *testing.T) (int, func() int64) {
			f := flPqFakeNNTP(t, 240)
			return f.port(), f.posts.Load
		}, pqRow{posted: true}, 2},
		{"441", func(t *testing.T) (int, func() int64) {
			f := flPqFakeNNTP(t, 441)
			return f.port(), f.posts.Load
		}, pqRow{attempts: 1, lastError: "441"}, 2},
		{"closed-port", func(t *testing.T) (int, func() int64) {
			return flPqClosedPort(t), func() int64 { return 0 }
		}, pqRow{lastError: "failed to get connection"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			port, posts := tc.port(t)
			var ids []int64
			pqWith(t, dir, func(db *database.Database) {
				ids = pqQueuedArticles(t, db, "pq.oneshot", 2)
				pqProvider(t, db, "pq-fake-"+tc.name, port)
			})

			if code := pqRun(t, "-data", dir); code != 0 {
				t.Fatalf("exit = %d, want 0", code)
			}
			st := pqStateOf(t, dir)
			for _, id := range ids {
				r, ok := st[id]
				if !ok {
					t.Fatalf("row %d missing", id)
				}
				if r.posted != tc.want.posted || r.inProcessing || r.attempts != tc.want.attempts ||
					!strings.Contains(r.lastError, tc.want.lastError) || (tc.want.lastError == "") != (r.lastError == "") {
					t.Errorf("row %d = %+v, want %+v (lastError as substring)", id, r, tc.want)
				}
			}
			if got := posts(); got != tc.wantPosts {
				t.Errorf("provider received %d articles, want %d", got, tc.wantPosts)
			}
		})
	}
}

// TestPostQueueMaxBatch: -max-batch 2 works on exactly 2 of 3 pending rows in a one-shot run.
func TestPostQueueMaxBatch(t *testing.T) {
	dir := t.TempDir()
	fake := flPqFakeNNTP(t, 240)
	var ids []int64
	pqWith(t, dir, func(db *database.Database) {
		ids = pqQueuedArticles(t, db, "pq.maxbatch", 3)
		pqProvider(t, db, "pq-fake-maxbatch", fake.port())
	})

	if code := pqRun(t, "-max-batch", "2", "-data", dir); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	st := pqStateOf(t, dir)
	posted, pending := 0, 0
	for _, id := range ids {
		switch st[id] {
		case pqRow{posted: true}:
			posted++
		case pqRow{}:
			pending++
		default:
			t.Errorf("row %d = %+v, want posted or pending", id, st[id])
		}
	}
	if posted != 2 || pending != 1 {
		t.Errorf("posted %d, pending %d; want 2, 1", posted, pending)
	}
	if got := fake.posts.Load(); got != 2 {
		t.Errorf("provider received %d articles, want 2", got)
	}
}

// TestPostQueueShutdownBeforeBatch: a shutdown requested before the batch starts returns 130
// and posts nothing.
func TestPostQueueShutdownBeforeBatch(t *testing.T) {
	dir := t.TempDir()
	fake := flPqFakeNNTP(t, 240)
	var ids []int64
	pqWith(t, dir, func(db *database.Database) {
		ids = pqQueuedArticles(t, db, "pq.stopbefore", 2)
		pqProvider(t, db, "pq-fake-stopbefore", fake.port())
	})

	stop := make(chan struct{})
	close(stop)
	if code := pqWait(t, pqStart([]string{"-data", dir}, stop)); code != 130 {
		t.Fatalf("exit = %d, want 130", code)
	}
	st := pqStateOf(t, dir)
	for _, id := range ids {
		if r := st[id]; r != (pqRow{}) {
			t.Errorf("row %d = %+v, want pending untouched", id, r)
		}
	}
	if got := fake.posts.Load(); got != 0 {
		t.Errorf("provider received %d articles, want 0", got)
	}
}

// TestPostQueueShutdownDuringPost: a shutdown while the provider holds back its answer to the
// first article interrupts the POST, returns 130 and puts every row of the batch back to pending
// without an attempt (TestAdfOptPostmgrStopResetsRemainder at the tool level).
func TestPostQueueShutdownDuringPost(t *testing.T) {
	dir := t.TempDir()
	fake := flPqFakeHang(t)
	var ids []int64
	pqWith(t, dir, func(db *database.Database) {
		ids = pqQueuedArticles(t, db, "pq.stopduring", 3)
		pqProvider(t, db, "pq-fake-hang", fake.port())
	})

	stop := make(chan struct{})
	ch := pqStart([]string{"-data", dir}, stop)
	select {
	case <-fake.received:
	case code := <-ch:
		t.Fatalf("runWith returned %d before the provider received an article", code)
	case <-time.After(testWait):
		t.Fatalf("provider did not receive an article within %v", testWait)
	}
	close(stop)
	if code := pqWait(t, ch); code != 130 {
		t.Fatalf("exit = %d, want 130", code)
	}
	st := pqStateOf(t, dir)
	for _, id := range ids {
		if r := st[id]; r != (pqRow{}) {
			t.Errorf("row %d = %+v, want pending untouched", id, r)
		}
	}
	if got := fake.posts.Load(); got != 1 {
		t.Errorf("provider received %d articles, want 1", got)
	}
}
