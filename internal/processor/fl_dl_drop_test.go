package processor

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
)

// D4: the commit barrier of DownloadArticles (commitThenProgress) must hold the progress back
// when the batch writer dropped articles, and only then. The waiter is replaced through the
// downloadWaitCommitted seam, so no main/group DB, no batch writer and no NNTP are needed.

const flDLDropProvider = "prov"
const flDLDropGroup = "test.dl.drop"

// TestFlDLDropHelper carries the helpers of these tests as methods (vet rejects Test* helper funcs).
type TestFlDLDropHelper struct{ t *testing.T }

// progressDB opens a progress DB under t.TempDir() seeded with progress 10.
func (h TestFlDLDropHelper) progressDB() *database.ProgressDB {
	h.t.Helper()
	pdb, err := database.NewProgressDB(h.t.TempDir())
	if err != nil {
		h.t.Fatalf("NewProgressDB: %v", err)
	}
	h.t.Cleanup(func() { _ = pdb.Close() })
	if err := pdb.UpdateProgress(flDLDropProvider, flDLDropGroup, 10); err != nil {
		h.t.Fatalf("seed progress: %v", err)
	}
	return pdb
}

// waiter replaces downloadWaitCommitted with one returning ret and counting its calls;
// the original is restored in t.Cleanup.
func (h TestFlDLDropHelper) waiter(ret error) *atomic.Int64 {
	h.t.Helper()
	orig := downloadWaitCommitted
	h.t.Cleanup(func() { downloadWaitCommitted = orig })
	calls := new(atomic.Int64)
	downloadWaitCommitted = func(sq *database.SQ3batch, m database.CommitMark, stop <-chan struct{}, d time.Duration) error {
		calls.Add(1)
		return ret
	}
	return calls
}

// run calls commitThenProgress and fails the test if it does not return within testWait.
func (h TestFlDLDropHelper) run(pdb *database.ProgressDB, end, gots, gotQueued, dups int64) error {
	h.t.Helper()
	proc := &Processor{}
	stop := make(chan struct{})
	defer close(stop)
	done := make(chan error, 1)
	go func() {
		done <- proc.commitThenProgress(flDLDropGroup, flDLDropProvider, database.CommitMark{}, pdb, end, gots, gotQueued, dups, stop)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(testWait):
		h.t.Fatalf("commitThenProgress did not return within %v", testWait)
		return nil
	}
}

// wantProgress fails the test unless the progress DB holds want for the test provider/group.
func (h TestFlDLDropHelper) wantProgress(pdb *database.ProgressDB, want int64) {
	h.t.Helper()
	got, err := pdb.GetLastArticle(flDLDropProvider, flDLDropGroup)
	if err != nil {
		h.t.Fatalf("GetLastArticle: %v", err)
	}
	if got != want {
		h.t.Errorf("progress = %d, want %d", got, want)
	}
}

// A dropped batch holds the progress back and the error wraps ErrBatchDropped.
func TestFlDLDropHoldsProgress(t *testing.T) {
	h := TestFlDLDropHelper{t}
	pdb := h.progressDB()
	calls := h.waiter(fmt.Errorf("%w: test", database.ErrBatchDropped))
	err := h.run(pdb, 20, 1, 1, 0)
	if !errors.Is(err, database.ErrBatchDropped) {
		t.Fatalf("commitThenProgress = %v, want ErrBatchDropped", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("waiter called %d times, want 1", n)
	}
	h.wantProgress(pdb, 10)
}

// Control: a committed batch moves the progress to end.
func TestFlDLDropCommittedMovesProgress(t *testing.T) {
	h := TestFlDLDropHelper{t}
	pdb := h.progressDB()
	calls := h.waiter(nil)
	if err := h.run(pdb, 20, 1, 1, 0); err != nil {
		t.Fatalf("commitThenProgress = %v, want nil", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("waiter called %d times, want 1", n)
	}
	h.wantProgress(pdb, 20)
}

// Nothing new to store (gots == 0): no barrier, but the progress moves because something
// was queued.
func TestFlDLDropNoGotsSkipsBarrier(t *testing.T) {
	h := TestFlDLDropHelper{t}
	pdb := h.progressDB()
	calls := h.waiter(fmt.Errorf("%w: test", database.ErrBatchDropped))
	if err := h.run(pdb, 20, 0, 1, 0); err != nil {
		t.Fatalf("commitThenProgress = %v, want nil", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("waiter called %d times, want 0", n)
	}
	h.wantProgress(pdb, 20)
}

// Nothing queued and no duplicates: the progress stays.
func TestFlDLDropNothingQueuedKeepsProgress(t *testing.T) {
	h := TestFlDLDropHelper{t}
	pdb := h.progressDB()
	calls := h.waiter(nil)
	if err := h.run(pdb, 20, 0, 0, 0); err != nil {
		t.Fatalf("commitThenProgress = %v, want nil", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("waiter called %d times, want 0", n)
	}
	h.wantProgress(pdb, 10)
}

// A failing UpdateProgress (closed progress DB) is returned, not only logged.
func TestFlDLDropUpdateProgressErrorReturned(t *testing.T) {
	h := TestFlDLDropHelper{t}
	pdb := h.progressDB()
	h.waiter(nil)
	if err := pdb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.run(pdb, 20, 1, 1, 0); err == nil {
		t.Fatal("commitThenProgress on a closed progress DB = nil, want an error")
	}
}
