package postmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adfOptDB is the database of this test binary (temp data root, no caches).
var adfOptDB *database.Database

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

// TestAdfOptPostmgrRunStopsOnShutdown: Run returns soon after shutdownChan is closed, also
// while it sleeps between batches (the 10 s pause is interruptible); Stop afterwards is safe.
func TestAdfOptPostmgrRunStopsOnShutdown(t *testing.T) {
	pm := NewPosterManager(adfOptDB, nil)
	shutdownChan := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go pm.Run(10, shutdownChan, &wg)

	time.Sleep(200 * time.Millisecond) // Run is in its 10 s pause by now (empty queue)
	start := time.Now()
	close(shutdownChan)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s after shutdown")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Run returned %v after shutdown, want < 2s", d)
	}
	pm.Stop() // stopCh already closed by Run: must not panic
}
