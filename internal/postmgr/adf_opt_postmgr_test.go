package postmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
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
// selects the batch and returns every selected row to pending (A-intake-10). Stop is idempotent.
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
	if err != nil {
		t.Fatalf("ProcessPendingPosts: %v", err)
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
