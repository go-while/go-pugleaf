package processor

// Processor.DownloadArticles against a fake provider (plan fetcher-hostile-peer, wave 2,
// slice fetcher-download-fake). The provider is proc_DLArt_fake_srv_test.go (a kit
// fakeserver on 127.0.0.1:0 over a kit Store), the database is a scratch database under
// t.TempDir() and the download workers are the ones cmd/nntp-fetcher runs
// (Batch.GetQ -> Pool.GetArticle -> item.ReturnQ).
//
// Pinned here: a normal run over a generated group, a lagging frontend (GROUP numbers
// below what the frontend has), a provider whose XHDR message-ids are out of step with
// its articles, one poisoned article and an expired login mid-run.
//
// No wall-clock assertion: testWait (PUGLEAF_TEST_DEADLINE) is only a hang guard.

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/nntp-testkit/fakeserver"
)

// dlFakeProvider is the provider name the progress DB is keyed by.
const dlFakeProvider = "hostile-fake"

// dlFakeGroupSeq makes every test its own group: the message-id cache
// (history.MsgIdCache) and the batch writer are process-global.
var dlFakeGroupSeq atomic.Int64

// dlFakeNewGroup returns a group name used by no other test of this process.
func dlFakeNewGroup() string {
	return fmt.Sprintf("hostile.dl.t%d%d", time.Now().UnixNano(), dlFakeGroupSeq.Add(1))
}

// dlFakeEnv is one fetcher against one fake provider.
type dlFakeEnv struct {
	t     *testing.T
	group string
	fake  *dlFake
	srv   *fakeserver.Server
	proc  *Processor
	pool  *nntp.Pool
	pdb   *database.ProgressDB
	dlPar chan struct{}

	stop chan struct{} // stops the download workers
	wg   sync.WaitGroup
}

// dlFakeSetup starts the fake provider, opens a scratch database and a scratch progress
// DB, builds the pool and starts MaxConns download workers. tune may change the backend
// config before the pool is built.
func dlFakeSetup(t *testing.T, group string, f *dlFake, tune func(*nntp.BackendConfig)) *dlFakeEnv {
	t.Helper()
	if f.st == nil {
		f.st = dlFakeStore(group, f.high)
	}
	srv := dlFakeStart(t, f)

	db := flNntpdbOpenDB(t)
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: group, Description: "hostile-peer fetcher test", Active: true, Status: "y", LowWater: 1}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", group, err)
	}
	pdb, err := database.NewProgressDB(t.TempDir())
	if err != nil {
		t.Fatalf("NewProgressDB: %v", err)
	}
	t.Cleanup(func() { _ = pdb.Close() })

	cfg := &nntp.BackendConfig{
		Host:           srv.Host(),
		Port:           srv.Port(),
		MaxConns:       2,
		ConnectTimeout: 30 * time.Second,
		Username:       f.user,
		Password:       f.pass,
		Provider:       &config.Provider{Name: dlFakeProvider, Host: srv.Host(), Port: srv.Port()},
	}
	if tune != nil {
		tune(cfg)
	}
	pool := nntp.NewPool(cfg)
	t.Cleanup(func() { _ = pool.ClosePool() })

	e := &dlFakeEnv{
		t:     t,
		group: group,
		fake:  f,
		srv:   srv,
		proc:  &Processor{DB: db, Pool: pool, MsgIdCache: history.NewMsgIdItemCache()},
		pool:  pool,
		pdb:   pdb,
		dlPar: make(chan struct{}, 1),
		stop:  make(chan struct{}),
	}
	e.startWorkers(cfg.MaxConns)
	return e
}

// startWorkers runs n download workers like cmd/nntp-fetcher's: take an item from the
// global Batch.GetQ, fetch the article through the pool, answer on item.ReturnQ. They
// stop when the test ends.
func (e *dlFakeEnv) startWorkers(n int) {
	for i := 0; i < n; i++ {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			for {
				var item *BatchItem
				select {
				case <-e.stop:
					return
				case item = <-Batch.GetQ:
				}
				if item == nil {
					continue
				}
				art, err := e.pool.GetArticle(item.MessageID, true)
				if err != nil {
					item.Error = err
				} else if art == nil {
					item.Error = fmt.Errorf("nil article without error")
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
	e.t.Cleanup(e.stopWorkers)
}

// stopWorkers stops the download workers and waits for them (idempotent).
func (e *dlFakeEnv) stopWorkers() {
	select {
	case <-e.stop:
	default:
		close(e.stop)
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(testWait):
		e.t.Errorf("download workers did not stop within %v", testWait)
	}
}

// run calls DownloadArticles for start-end and fails the test if it does not return
// within testWait (a hang guard, not a timing assertion).
func (e *dlFakeEnv) run(start, end int64) error {
	e.t.Helper()
	shutdown := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.proc.DownloadArticles(e.group, e.dlPar, e.pdb, start, end, shutdown)
	}()
	select {
	case err := <-done:
		close(shutdown)
		return err
	case <-time.After(testWait):
		close(shutdown)
		e.t.Fatalf("DownloadArticles(%s, %d-%d) did not return within %v", e.group, start, end, testWait)
		return nil
	}
}

// stored returns the message-ids in the group database.
func (e *dlFakeEnv) stored() map[string]int {
	e.t.Helper()
	groupDB, err := e.proc.DB.GetGroupDB(e.group)
	if err != nil {
		e.t.Fatalf("GetGroupDB(%s): %v", e.group, err)
	}
	defer groupDB.Return()
	rows, err := database.RetryableQuery(groupDB.DB, "SELECT message_id FROM articles", nil)
	if err != nil {
		e.t.Fatalf("select message_id: %v", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatalf("scan message_id: %v", err)
		}
		out[id]++
	}
	if err := rows.Err(); err != nil {
		e.t.Fatalf("rows: %v", err)
	}
	return out
}

// wantStoredRange fails unless the group database holds exactly the articles lo..hi,
// each once.
func (e *dlFakeEnv) wantStoredRange(lo, hi int64) {
	e.t.Helper()
	got := e.stored()
	if int64(len(got)) != hi-lo+1 {
		e.t.Fatalf("%d articles stored, want %d", len(got), hi-lo+1)
	}
	for n := lo; n <= hi; n++ {
		id := dlFakeMsgID(e.group, n)
		switch got[id] {
		case 1:
		case 0:
			e.t.Fatalf("article %d (%s) not stored", n, id)
		default:
			e.t.Fatalf("article %d (%s) stored %d times", n, id, got[id])
		}
	}
}

// progress returns the stored progress of the test provider and group.
func (e *dlFakeEnv) progress() int64 {
	e.t.Helper()
	got, err := e.pdb.GetLastArticle(dlFakeProvider, e.group)
	if err != nil {
		e.t.Fatalf("GetLastArticle: %v", err)
	}
	return got
}

// wantProgress fails unless the progress DB holds want.
func (e *dlFakeEnv) wantProgress(want int64) {
	e.t.Helper()
	if got := e.progress(); got != want {
		e.t.Errorf("progress = %d, want %d", got, want)
	}
}

// 1. Normal run: a generated group of 2000 articles is downloaded completely, every
// article is in the group database once and the progress DB holds the end of the range.
// XHdrStreamed asks for the range in batches of 1000 (it does not stop after one).
func TestDLArtFakeNormalRun(t *testing.T) {
	const high = 2000
	group := dlFakeNewGroup()
	e := dlFakeSetup(t, group, &dlFake{high: high}, nil)

	if err := e.run(1, high); err != nil {
		t.Fatalf("DownloadArticles: %v", err)
	}
	e.wantStoredRange(1, high)
	e.wantProgress(high)

	if n := e.srv.Count("ARTICLE"); n != high {
		t.Errorf("ARTICLE commands = %d, want %d", n, high)
	}
	if n := e.srv.Count("XHDR"); n != 2 {
		t.Errorf("XHDR commands = %d, want 2 (batches of 1000)", n)
	}
}

// 2. Lagging frontend: GROUP reports numbers below what the frontend already fetched
// ("211 0 0 0" and a High below the progress). The run ends with ErrOutOfRange, the
// progress is not rewound, nothing is deleted and one line is logged for the group.
func TestDLArtFakeLaggingFrontend(t *testing.T) {
	const high = 200
	group := dlFakeNewGroup()
	var lag atomic.Int64 // 0 = the real numbers, 1 = "211 0 0 0", 2 = High below progress
	f := &dlFake{
		high: high,
		groupNumbers: func(conn int, g fakeserver.Group) fakeserver.Group {
			switch lag.Load() {
			case 1:
				g.Low, g.High, g.Count = 0, 0, 0
			case 2:
				g.Low, g.High, g.Count = 1, high/2, high/2
			}
			return g
		},
	}
	e := dlFakeSetup(t, group, f, nil)

	if err := e.run(1, high); err != nil {
		t.Fatalf("first run: %v", err)
	}
	e.wantStoredRange(1, high)
	e.wantProgress(high)

	for _, tc := range []struct {
		name string
		mode int64
	}{
		{"211 0 0 0", 1},
		{"high below progress", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lag.Store(tc.mode)
			defer lag.Store(0)
			logs := postCommitCaptureLog(t)
			err := e.run(high+1, high+100)
			if !errors.Is(err, nntp.ErrOutOfRange) {
				t.Fatalf("DownloadArticles = %v, want ErrOutOfRange", err)
			}
			e.wantProgress(high)       // no rewind
			e.wantStoredRange(1, high) // nothing deleted
			if n := strings.Count(logs.String(), "progress not updated"); n != 1 {
				t.Errorf("%d 'progress not updated' log lines, want 1\n%s", n, logs.String())
			}
		})
	}
}

// 3. The provider's XHDR message-ids are out of step with its articles.
func TestDLArtFakeOverviewOutOfStep(t *testing.T) {
	// 3a: every XHDR line carries the next article's message-id. The fetcher asks for
	// articles by message-id, so the rotation only changes the order: every article is
	// stored once, under its own message-id.
	t.Run("rotated message-ids", func(t *testing.T) {
		const high = 100
		group := dlFakeNewGroup()
		f := &dlFake{
			high: high,
			xhdrID: func(conn int, g string, n int64, id string) string {
				return dlFakeMsgID(g, n%high+1)
			},
		}
		e := dlFakeSetup(t, group, f, nil)
		if err := e.run(1, high); err != nil {
			t.Fatalf("DownloadArticles: %v", err)
		}
		e.wantStoredRange(1, high)
		e.wantProgress(high)
	})

	// 3b: ARTICLE answers with an article whose own Message-ID header is not the one it
	// was asked for. Nothing may be stored under the id the overview gave, and the
	// mismatch is logged once for the run, not once per article.
	t.Run("foreign message-id header", func(t *testing.T) {
		const high = 50
		group := dlFakeNewGroup()
		f := &dlFake{
			high: high,
			articleHeaders: func(n int64, headers []string) []string {
				for i, h := range headers {
					if strings.HasPrefix(h, "Message-ID:") {
						headers[i] = fmt.Sprintf("Message-ID: <alien-%d.%s@other.invalid>", n, group)
					}
				}
				return headers
			},
		}
		e := dlFakeSetup(t, group, f, nil)
		logs := postCommitCaptureLog(t)
		if err := e.run(1, high); err != nil {
			t.Fatalf("DownloadArticles: %v", err)
		}
		if got := e.stored(); len(got) != 0 {
			t.Errorf("%d articles stored although every Message-ID header differs: %v", len(got), got)
		}
		if n := strings.Count(logs.String(), "provider out of step"); n != 1 {
			t.Errorf("%d 'provider out of step' log lines, want 1 (once per run, not per article)", n)
		}
	})

	// 3c: the articles are served without a Subject header. ProcessArticle rejects them
	// one by one ([HDR-SPAM]); nothing is stored. Pinned, not changed.
	t.Run("blank subject", func(t *testing.T) {
		const high = 20
		group := dlFakeNewGroup()
		f := &dlFake{
			high: high,
			articleHeaders: func(n int64, headers []string) []string {
				out := headers[:0]
				for _, h := range headers {
					if strings.HasPrefix(h, "Subject:") {
						continue
					}
					out = append(out, h)
				}
				return out
			},
		}
		e := dlFakeSetup(t, group, f, nil)
		logs := postCommitCaptureLog(t)
		if err := e.run(1, high); err != nil {
			t.Fatalf("DownloadArticles: %v", err)
		}
		if got := e.stored(); len(got) != 0 {
			t.Errorf("%d articles stored although every Subject header is missing", len(got))
		}
		if n := strings.Count(logs.String(), "empty subject"); n != high {
			t.Errorf("%d 'empty subject' log lines, want %d (ProcessArticle logs one per article)", n, high)
		}
	})
}

// 4. One poisoned article: ARTICLE for article 7 answers 220 and then never stops. The
// client's octet budget ends that one fetch, the article is skipped, the rest of the
// group is stored and no goroutine is left behind when the pool is closed.
func TestDLArtFakePoisonedArticle(t *testing.T) {
	const high = 100
	const poisoned = 7
	group := dlFakeNewGroup()
	f := &dlFake{high: high, endlessID: dlFakeMsgID(group, poisoned)}
	e := dlFakeSetup(t, group, f, func(cfg *nntp.BackendConfig) {
		cfg.MaxArticleResponseBytes = 256 << 10 // the default 64 MiB would only be slower
	})

	runtime.GC()
	before := runtime.NumGoroutine()

	if err := e.run(1, high); err != nil {
		t.Fatalf("DownloadArticles: %v", err)
	}
	got := e.stored()
	if len(got) != high-1 {
		t.Errorf("%d articles stored, want %d", len(got), high-1)
	}
	if n := got[dlFakeMsgID(group, poisoned)]; n != 0 {
		t.Errorf("the poisoned article is stored %d times, want 0", n)
	}
	for n := int64(1); n <= high; n++ {
		if n == poisoned {
			continue
		}
		if got[dlFakeMsgID(group, n)] != 1 {
			t.Fatalf("article %d not stored exactly once (%d)", n, got[dlFakeMsgID(group, n)])
		}
	}
	// the endless reply is answered once, not retried in a loop
	if n := e.srv.Count("ARTICLE"); n != high {
		t.Errorf("ARTICLE commands = %d, want %d (no retry of the poisoned article)", n, high)
	}

	e.stopWorkers()
	if err := e.pool.ClosePool(); err != nil {
		t.Errorf("ClosePool: %v", err)
	}
	// poll (hang guard only): the fake's writer and the pool's connections end when the
	// client sockets are closed
	deadline := time.Now().Add(testWait)
	var after int
	for {
		runtime.GC()
		after = runtime.NumGoroutine()
		if after <= before+2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after > before+2 {
		t.Errorf("goroutines after the run: %d, before: %d", after, before)
	}
}

// 5. The provider's login expires mid-run: ARTICLE answers 480 from the 50th article on.
// The run must stop the group with ErrAuthRequired instead of asking for every remaining
// article, the progress must not move, and a re-run after the provider accepts again
// must complete.
func TestDLArtFakeAuthExpiredMidRun(t *testing.T) {
	const high = 400
	for _, tc := range []struct {
		name   string
		budget int64 // 480 replies the provider has left
	}{
		{"one expired login", 1},
		{"every article refused", 1 << 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := dlFakeNewGroup()
			f := &dlFake{high: high, user: dlFakeUser, pass: dlFakePass, auth480From: 50}
			f.auth480Left.Store(tc.budget)
			e := dlFakeSetup(t, group, f, nil)

			logs := postCommitCaptureLog(t)
			err := e.run(1, high)
			if !errors.Is(err, nntp.ErrAuthRequired) {
				t.Fatalf("DownloadArticles = %v, want an error wrapping ErrAuthRequired", err)
			}
			e.wantProgress(0) // the range was not fetched: no progress
			if n := strings.Count(logs.String(), "authentication expired"); n != 1 {
				t.Errorf("%d 'authentication expired' log lines, want 1", n)
			}
			// the run stops instead of walking the rest of the group (that would be about
			// high-50 refusals). What is still asked for after the stop: the items in the
			// global Batch.GetQ (its depth), one in flight per worker, and what the feeder
			// pushed before it saw the run end; the workers keep draining the queue while
			// this line runs. Two queue depths cover that on a slow 2-core host (the
			// distro matrix saw queue+12 on Alpine 3.24) and still rule out the walk.
			if n := f.sent480.Load(); n > 2*int64(cap(Batch.GetQ)) {
				t.Errorf("%d articles were refused with 480, want at most two queue depths (%d)", n, 2*cap(Batch.GetQ))
			}
			if got := e.stored(); int64(len(got)) >= high {
				t.Errorf("%d articles stored, want fewer than the whole group %d", len(got), high)
			}

			// the provider accepts again: the next run completes the group
			f.auth480Left.Store(0)
			if err := e.run(1, high); err != nil {
				t.Fatalf("re-run: %v", err)
			}
			e.wantStoredRange(1, high)
			e.wantProgress(high)
		})
	}
}
