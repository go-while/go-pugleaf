package processor

// The provider-pass swap of cmd/nntp-fetcher (plan fetcher-group-routing): runPass closes the
// pass's Batch.GetQ once every DownloadArticles call has returned, and the next pass gets a
// fresh BatchQueue. That is only safe because DownloadArticles does not return while its feeder
// (feedDownloadRun, the only sender on Batch.GetQ) may still send. This test pins that: the
// feeder is blocked on a full GetQ when the run ends, and the pass swap that follows (close the
// old GetQ, new queue, next run) must neither panic with a send on a closed channel nor lose
// the next run's articles.
//
// What it does not do: prove the wait for the feeder by itself. The feeder checks runDone
// before every send and a feeder parked on the full queue wakes on runDone, so without that wait
// only a scheduler-sized window is left (between the check and the send), which a test without
// hooks cannot hit reliably (a mutation run without the wait passed 10 of 10). It catches any
// sender that keeps sending on the pass's GetQ after the run returned (a panic on the closed
// queue), and a pass swap that loses the next run's articles.
//
// No wall-clock assertion: testWait (PUGLEAF_TEST_DEADLINE) is only a hang guard.

import (
	"errors"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

func TestDLArtPassSwapFeederJoined(t *testing.T) {
	const high = 300 // more than GetQ holds (128): the feeder blocks on a send
	group := dlFakeNewGroup()
	f := &dlFake{high: high, st: dlFakeStore(group, high)}
	srv := dlFakeStart(t, f)

	db := flNntpdbOpenDB(t)
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: group, Description: "pass swap test", Active: true, Status: "y", LowWater: 1}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", group, err)
	}
	pdb, err := database.NewProgressDB(t.TempDir())
	if err != nil {
		t.Fatalf("NewProgressDB: %v", err)
	}
	t.Cleanup(func() { _ = pdb.Close() })
	pool := nntp.NewPool(&nntp.BackendConfig{
		Host: srv.Host(), Port: srv.Port(), MaxConns: 2, ConnectTimeout: 30 * time.Second,
		Provider: &config.Provider{Name: dlFakeProvider, Host: srv.Host(), Port: srv.Port()},
	})
	t.Cleanup(func() { _ = pool.ClosePool() })
	e := &dlFakeEnv{
		t: t, group: group, fake: f, srv: srv, pool: pool, pdb: pdb,
		proc:  &Processor{DB: db, Pool: pool, MsgIdCache: history.NewMsgIdItemCache()},
		dlPar: make(chan struct{}, 1),
		stop:  make(chan struct{}),
	}
	prev := Batch
	t.Cleanup(func() { Batch = prev })

	// pass 1: no download worker but this one, which holds the first item until the feeder has
	// filled GetQ and is blocked on its next send, then answers it with an expired login: the
	// run stops while its feeder still has items to send
	bq1 := NewBatchQueue()
	Batch = bq1
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		var item *BatchItem
		select {
		case item = <-bq1.GetQ:
		case <-time.After(testWait):
			t.Errorf("pass 1: no item on GetQ within %v", testWait)
			return
		}
		deadline := time.Now().Add(testWait) // hang guard only
		for len(bq1.GetQ) < cap(bq1.GetQ) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if len(bq1.GetQ) < cap(bq1.GetQ) {
			t.Errorf("pass 1: GetQ holds %d of %d items: the feeder did not fill it", len(bq1.GetQ), cap(bq1.GetQ))
		}
		item.Error = nntp.ErrAuthRequired
		item.ReturnQ <- item
	}()
	if err := e.run(1, high); !errors.Is(err, nntp.ErrAuthRequired) {
		t.Fatalf("pass 1: DownloadArticles = %v, want an error wrapping ErrAuthRequired", err)
	}
	<-answered

	// the pass swap of runPass: close the pass's GetQ (a feeder that could still send would
	// panic here), hand back what is left as the article workers do, then a fresh queue
	close(bq1.GetQ)
	left := 0
	for item := range bq1.GetQ {
		left++
		item.Error = errors.New("pass ended")
		select {
		case item.ReturnQ <- item:
		case <-time.After(testWait):
			t.Fatalf("pass 1: handing back a left-over item blocked for %v", testWait)
		}
	}
	if left != cap(bq1.GetQ) {
		t.Errorf("pass 1: %d items left in GetQ, want %d (a full queue)", left, cap(bq1.GetQ))
	}

	// pass 2 on its own queue and workers: the whole group arrives
	bq2 := NewBatchQueue()
	Batch = bq2
	for i := 0; i < 2; i++ {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			for {
				var item *BatchItem
				select {
				case <-e.stop:
					return
				case item = <-bq2.GetQ:
				}
				art, err := pool.GetArticle(item.MessageID, true)
				if err != nil {
					item.Error = err
				} else {
					item.Article = art
				}
				select {
				case item.ReturnQ <- item:
				case <-e.stop:
					return
				}
			}
		}()
	}
	t.Cleanup(e.stopWorkers)
	if err := e.run(1, high); err != nil {
		t.Fatalf("pass 2: DownloadArticles: %v", err)
	}
	e.wantStoredRange(1, high)
	e.wantProgress(high)
}
