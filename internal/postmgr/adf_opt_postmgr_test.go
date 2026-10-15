package postmgr

import (
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// adfOptDB is the database of this test binary (temp data root, no caches).
var adfOptDB *database.Database

// testWait is the hang guard of this package's tests: it only stops a hung test, a passing test
// never waits on it. Override with PUGLEAF_TEST_DEADLINE (a Go duration, e.g. 120s); default 60 s.
// Plan test-timing-hardening D3.
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
		fmt.Println("postmgr: skipped: OpenDatabase refuses to run with euid < 1000")
		os.Exit(0)
	}
	database.NO_CACHE_BOOT = true
	dir, err := os.MkdirTemp("", "pugleaf-postmgr-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "OpenDatabase:", err)
		os.Exit(2)
	}
	adfOptDB = db
	code := m.Run()
	close(db.StopChan)
	db.WG.Wait()
	if err := db.Shutdown(); err != nil {
		fmt.Fprintln(os.Stderr, "Shutdown:", err)
		if code == 0 {
			code = 1
		}
	}
	if code == 0 {
		_ = os.RemoveAll(dir)
	} else {
		fmt.Fprintln(os.Stderr, "test data kept in", dir)
	}
	os.Exit(code)
}

// adfOptGroup creates newsgroup name (active or not) and returns its id; its post_queue rows
// are removed when the test ends.
func adfOptGroup(t *testing.T, name string, active bool) int64 {
	t.Helper()
	g := &models.Newsgroup{Name: name, Active: active, Status: "y", LowWater: 1}
	if err := adfOptDB.InsertNewsgroup(g); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	var id int64
	if err := database.RetryableQueryRowScan(adfOptDB.GetMainDB(),
		"SELECT id FROM newsgroups WHERE name = ?", []interface{}{name}, &id); err != nil {
		t.Fatalf("id of %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := database.RetryableExec(adfOptDB.GetMainDB(), "DELETE FROM post_queue WHERE newsgroup_id = ?", id); err != nil {
			t.Errorf("cleanup post_queue of %s: %v", name, err)
		}
		// Remove the group row too, so the test binary can run with -count>1 (TestMain shares one DB).
		if _, err := database.RetryableExec(adfOptDB.GetMainDB(), "DELETE FROM newsgroups WHERE id = ?", id); err != nil {
			t.Errorf("cleanup newsgroup %s: %v", name, err)
		}
	})
	return id
}

// adfOptQueue queues msgID for groupID and returns the row id.
func adfOptQueue(t *testing.T, groupID int64, msgID string) int64 {
	t.Helper()
	if err := adfOptDB.InsertPostQueueEntry(groupID, msgID); err != nil {
		t.Fatalf("InsertPostQueueEntry %s: %v", msgID, err)
	}
	var id int64
	if err := database.RetryableQueryRowScan(adfOptDB.GetMainDB(),
		"SELECT id FROM post_queue WHERE message_id = ?", []interface{}{msgID}, &id); err != nil {
		t.Fatalf("id of %s: %v", msgID, err)
	}
	return id
}

// adfOptFlags returns posted_to_remote and in_processing of row id.
func adfOptFlags(t *testing.T, id int64) (posted, inProcessing bool) {
	t.Helper()
	if err := database.RetryableQueryRowScan(adfOptDB.GetMainDB(),
		"SELECT posted_to_remote, in_processing FROM post_queue WHERE id = ?", []interface{}{id}, &posted, &inProcessing); err != nil {
		t.Fatalf("flags of post_queue row %d: %v", id, err)
	}
	return posted, inProcessing
}

// TestAdfOptPostmgrStopResetsRemainder: with the stop already requested, ProcessPendingPosts
// selects the batch, returns every selected row to pending (A-intake-10) and reports ErrStopped
// (cmd/post-queue exits 130 only then). Stop is idempotent.
func TestAdfOptPostmgrStopResetsRemainder(t *testing.T) {
	gid := adfOptGroup(t, "adfopt.postmgr.stop", true)
	var ids []int64
	for i := 0; i < 3; i++ {
		ids = append(ids, adfOptQueue(t, gid, fmt.Sprintf("<stop.%d@adfopt.invalid>", i)))
	}

	pm := NewPosterManager(adfOptDB, nil)
	pm.Stop()
	pm.Stop() // second Stop must not panic (once-guarded close)

	n, err := pm.ProcessPendingPosts(10)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("ProcessPendingPosts after stop: err = %v, want ErrStopped", err)
	}
	if n != 0 {
		t.Errorf("ProcessPendingPosts after stop = %d, want 0", n)
	}
	for _, id := range ids {
		if posted, inProc := adfOptFlags(t, id); posted || inProc {
			t.Errorf("entry %d after stop: posted=%v in_processing=%v, want false, false", id, posted, inProc)
		}
	}
}

// TestAdfOptPostmgrFailedAndInactive: a row whose article is missing goes back to pending, a row
// of an inactive group is marked posted (dropped), and nothing stays in_processing.
func TestAdfOptPostmgrFailedAndInactive(t *testing.T) {
	activeID := adfOptGroup(t, "adfopt.postmgr.active", true)
	inactiveID := adfOptGroup(t, "adfopt.postmgr.inactive", false)
	missing := adfOptQueue(t, activeID, "<missing@adfopt.invalid>")
	dropped := adfOptQueue(t, inactiveID, "<inactive@adfopt.invalid>")

	pm := NewPosterManager(adfOptDB, nil)
	n, err := pm.ProcessPendingPosts(10)
	if err != nil {
		t.Fatalf("ProcessPendingPosts: %v", err)
	}
	if n != 2 {
		t.Errorf("ProcessPendingPosts = %d, want 2", n)
	}
	if posted, inProc := adfOptFlags(t, missing); posted || inProc {
		t.Errorf("failed entry %d: posted=%v in_processing=%v, want false, false", missing, posted, inProc)
	}
	if posted, inProc := adfOptFlags(t, dropped); !posted || inProc {
		t.Errorf("inactive-group entry %d: posted=%v in_processing=%v, want true, false", dropped, posted, inProc)
	}
}

// adfOptInSleep reports whether a goroutine is inside PosterManager.sleep (Run's pause between
// batches). Only TestAdfOptPostmgrRunStopsOnShutdown runs Run, one instance at a time.
func adfOptInSleep() bool {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Contains(string(buf[:n]), "postmgr.(*PosterManager).sleep(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// TestAdfOptPostmgrRunStopsOnShutdown: Run returns after shutdownChan is closed, also while it
// sleeps between batches (the idle pause is interruptible); Stop afterwards is safe. Structural:
// the test waits until Run is inside its pause, and with a pause longer than the hang guard a
// return proves the pause was interrupted. No duration is asserted.
func TestAdfOptPostmgrRunStopsOnShutdown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pause time.Duration
	}{
		{"long-pause-interrupted", max(time.Hour, 2*testWait)},
		{"short-pause-loop", time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pm := NewPosterManager(adfOptDB, nil)
			pm.idlePause = tc.pause
			shutdownChan := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go pm.Run(10, shutdownChan, &wg)
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			// Run is in its pause (empty queue: done < limit, so the pause is idlePause)
			deadline := time.Now().Add(testWait)
			for !adfOptInSleep() {
				select {
				case <-done:
					t.Fatal("Run returned before shutdown")
				default:
				}
				if time.Now().After(deadline) {
					t.Fatalf("Run never reached its pause within %v", testWait)
				}
				time.Sleep(time.Millisecond)
			}
			close(shutdownChan)
			select {
			case <-done:
			case <-time.After(testWait):
				t.Fatalf("Run did not return within %v after shutdown (pause %v)", testWait, tc.pause)
			}
			pm.Stop() // stopCh already closed by Run: must not panic
		})
	}
}

// flPqFake is a loopback NNTP server that accepts MODE READER and POST and answers every posted
// article with code (240 or 441). No external network is used.
type flPqFake struct {
	ln    net.Listener
	code  int
	posts atomic.Int64 // articles received
	mu    sync.Mutex
	conns []net.Conn
	wg    sync.WaitGroup
}

// flPqFakeNNTP starts a flPqFake on 127.0.0.1; it stops when the test ends.
func flPqFakeNNTP(t *testing.T, code int) *flPqFake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &flPqFake{ln: ln, code: code}
	f.wg.Add(1)
	go f.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		for _, c := range f.conns {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *flPqFake) port() int {
	return f.ln.Addr().(*net.TCPAddr).Port
}

func (f *flPqFake) serve() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		f.wg.Add(1)
		go f.handle(c)
	}
}

func (f *flPqFake) handle(c net.Conn) {
	defer f.wg.Done()
	defer c.Close()
	tp := textproto.NewConn(c)
	if err := tp.PrintfLine("200 flpq fake ready"); err != nil {
		return
	}
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		switch strings.ToUpper(strings.TrimSpace(line)) {
		case "MODE READER":
			err = tp.PrintfLine("200 reader mode")
		case "POST":
			if err = tp.PrintfLine("340 send article"); err != nil {
				return
			}
			if _, err = tp.ReadDotLines(); err != nil {
				return
			}
			f.posts.Add(1)
			err = tp.PrintfLine("%d flpq fake answer", f.code)
		case "QUIT":
			_ = tp.PrintfLine("205 bye")
			return
		default:
			err = tp.PrintfLine("500 unknown command")
		}
		if err != nil {
			return
		}
	}
}

// flPqClosedPort returns a loopback port nothing listens on (connection refused).
func flPqClosedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// flPqPool returns a posting pool for 127.0.0.1:port, closed when the test ends.
func flPqPool(t *testing.T, name string, port int) *nntp.Pool {
	t.Helper()
	pool := nntp.NewPool(&nntp.BackendConfig{
		Host:     "127.0.0.1",
		Port:     port,
		MaxConns: 1,
		Provider: &config.Provider{Name: name, Host: "127.0.0.1", Port: port, MaxConns: 1, Enabled: true, Posting: true},
	})
	t.Cleanup(func() {
		if err := pool.ClosePool(); err != nil {
			t.Errorf("ClosePool %s: %v", name, err)
		}
	})
	return pool
}

// flPqQueuedArticle creates the active group name with one article msgID in its group DB and
// queues it; it returns the post_queue row id.
func flPqQueuedArticle(t *testing.T, name, msgID string) int64 {
	t.Helper()
	gid := adfOptGroup(t, name, true)
	groupDB, err := adfOptDB.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", name, err)
	}
	defer groupDB.Return()
	if _, err := database.RetryableExec(groupDB.DB,
		`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string,
		"references", bytes, lines, reply_count, path, headers_json, body_text, downloaded)
		VALUES (1, ?, 'flpq subject', 'poster <poster@test.invalid>', ?, 'Thu, 15 Oct 2026 10:00:00 +0000',
		'', 60, 1, 0, '.POSTED!not-for-mail', ?, 'flpq body', 1)`,
		msgID, time.Now().UTC().Format("2006-01-02 15:04:05"), "Newsgroups: "+name); err != nil {
		t.Fatalf("insert article %s: %v", msgID, err)
	}
	return adfOptQueue(t, gid, msgID)
}

// flPqAttempts returns attempts and last_error of post_queue row id.
func flPqAttempts(t *testing.T, id int64) (attempts int, lastError string) {
	t.Helper()
	if err := database.RetryableQueryRowScan(adfOptDB.GetMainDB(),
		"SELECT attempts, COALESCE(last_error, '') FROM post_queue WHERE id = ?", []interface{}{id}, &attempts, &lastError); err != nil {
		t.Fatalf("attempts of post_queue row %d: %v", id, err)
	}
	return attempts, lastError
}

// TestFlPqPostmgrRejectedParks: a 441 from the only provider counts one attempt per run and
// returns the row to pending with last_error; after PostQueueMaxAttempts (5) runs the row is
// parked and no longer posted.
func TestFlPqPostmgrRejectedParks(t *testing.T) {
	if got := adfOptDB.PostQueueMaxAttempts(); got != 5 {
		t.Fatalf("precondition: PostQueueMaxAttempts = %d, want 5", got)
	}
	fake := flPqFakeNNTP(t, 441)
	id := flPqQueuedArticle(t, "flpq.postmgr.rejected", "<rejected@flpq.invalid>")
	pm := NewPosterManager(adfOptDB, []*nntp.Pool{flPqPool(t, "flpq-441", fake.port())})

	for i := 1; i <= 5; i++ {
		n, err := pm.ProcessPendingPosts(10)
		if err != nil || n != 1 {
			t.Fatalf("run %d: ProcessPendingPosts = %d, %v; want 1, nil", i, n, err)
		}
		attempts, lastErr := flPqAttempts(t, id)
		if attempts != i || !strings.Contains(lastErr, "441") {
			t.Errorf("run %d: attempts=%d last_error=%q, want %d and a 441 error", i, attempts, lastErr, i)
		}
		if posted, inProc := adfOptFlags(t, id); posted || inProc {
			t.Errorf("run %d: posted=%v in_processing=%v, want false, false", i, posted, inProc)
		}
	}
	if n, err := pm.ProcessPendingPosts(10); err != nil || n != 0 {
		t.Errorf("run 6 (parked): ProcessPendingPosts = %d, %v; want 0, nil", n, err)
	}
	if got := fake.posts.Load(); got != 5 {
		t.Errorf("provider received %d articles, want 5", got)
	}
}

// TestFlPqPostmgrTransientNotCounted: a connection error stores last_error but counts no
// attempt, also when another provider answered 441 (not every provider rejected the article).
func TestFlPqPostmgrTransientNotCounted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pools func(t *testing.T) []*nntp.Pool
		want  string // in last_error
	}{
		{"refused", func(t *testing.T) []*nntp.Pool {
			return []*nntp.Pool{flPqPool(t, "flpq-refused", flPqClosedPort(t))}
		}, "failed to get connection"},
		{"441-and-refused", func(t *testing.T) []*nntp.Pool {
			fake := flPqFakeNNTP(t, 441)
			return []*nntp.Pool{flPqPool(t, "flpq-441", fake.port()), flPqPool(t, "flpq-refused", flPqClosedPort(t))}
		}, "1 of 2 rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := flPqQueuedArticle(t, "flpq.postmgr.transient."+strings.ReplaceAll(tc.name, "-", ""), "<transient."+tc.name+"@flpq.invalid>")
			pm := NewPosterManager(adfOptDB, tc.pools(t))
			n, err := pm.ProcessPendingPosts(10)
			if err != nil || n != 1 {
				t.Fatalf("ProcessPendingPosts = %d, %v; want 1, nil", n, err)
			}
			attempts, lastErr := flPqAttempts(t, id)
			if attempts != 0 || !strings.Contains(lastErr, tc.want) {
				t.Errorf("attempts=%d last_error=%q, want 0 and %q", attempts, lastErr, tc.want)
			}
			if posted, inProc := adfOptFlags(t, id); posted || inProc {
				t.Errorf("posted=%v in_processing=%v, want false, false", posted, inProc)
			}
		})
	}
}

// TestFlPqPostmgrPosted: a 240 marks the row posted and counts nothing.
func TestFlPqPostmgrPosted(t *testing.T) {
	fake := flPqFakeNNTP(t, 240)
	id := flPqQueuedArticle(t, "flpq.postmgr.posted", "<posted@flpq.invalid>")
	pm := NewPosterManager(adfOptDB, []*nntp.Pool{flPqPool(t, "flpq-240", fake.port())})
	n, err := pm.ProcessPendingPosts(10)
	if err != nil || n != 1 {
		t.Fatalf("ProcessPendingPosts = %d, %v; want 1, nil", n, err)
	}
	if posted, inProc := adfOptFlags(t, id); !posted || inProc {
		t.Errorf("posted=%v in_processing=%v, want true, false", posted, inProc)
	}
	if attempts, lastErr := flPqAttempts(t, id); attempts != 0 || lastErr != "" {
		t.Errorf("attempts=%d last_error=%q, want 0, \"\"", attempts, lastErr)
	}
	if got := fake.posts.Load(); got != 1 {
		t.Errorf("provider received %d articles, want 1", got)
	}
}
