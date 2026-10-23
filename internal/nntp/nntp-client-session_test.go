package nntp

// Session-fault tests of plan fetcher-hostile-peer, slice client-session-faults:
// what Pool and BackendConn do when a provider answers a single command of a
// running session with a fault (plan "Design" point 2).
//
// Expected behaviour pinned here:
//   - a failing N-th ARTICLE is reported for that article only; the pool
//     connection is dropped (5xx, EOF) or kept (430) and the next request is
//     served, on a fresh connection when the old one was dropped;
//   - a 480 on an authenticated connection (the kit ends the login on any 480
//     it writes) closes that pool connection; the next Get dials and
//     authenticates again and the command is served;
//   - a server-side connection cap (the kit's Options.MaxConns) makes the extra
//     connect fail with its "502 Too many connections" greeting; the pool does
//     not count it as open and does not retry it in a tight loop, and once a
//     connection is free the next Get succeeds.
//
// All servers are kit Store servers on 127.0.0.1:0 (cliStoreServer /
// newCliFakeServer). There is no wall-clock assertion: cliWait
// (PUGLEAF_TEST_DEADLINE) is a hang guard only.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// cliSessGroup is the group every store server of this file serves.
const cliSessGroup = "misc.test"

// cliSessRun runs f in a goroutine and fails the test when it does not return
// within cliWait (hang guard: a faulted command must never block forever).
func cliSessRun(t *testing.T, name string, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("%s: panic: %v", name, r)
			}
		}()
		done <- f()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(cliWait):
		t.Fatalf("%s: no return within %v", name, cliWait)
		return nil
	}
}

// cliSessStore returns a store holding n articles of cliSessGroup and their
// message-ids in order (ids[0] is article 1).
func cliSessStore(t *testing.T, n int) (*cliFakeStore, []string) {
	t.Helper()
	st := cliFakeNewStore()
	st.AddGroup(cliSessGroup, "session fault tests")
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("<sess-%d@test.invalid>", i)
		st.AddArticle(cliSessGroup, cliFakeArticle{
			MessageID: id,
			Headers: []string{
				"From: tester <t@test.invalid>",
				fmt.Sprintf("Subject: session %d", i),
				"Message-ID: " + id,
			},
			Body: []string{fmt.Sprintf("body %d", i)},
		})
		ids = append(ids, id)
	}
	return st, ids
}

// cliSessPool builds a pool on cliBackend(s) (MaxConns 2) with the AUTHINFO
// back-off and the idle expiry disabled, then mutate; closed by t.Cleanup.
func cliSessPool(t *testing.T, s *cliFakeServer, mutate func(*BackendConfig)) *Pool {
	t.Helper()
	cfg := cliBackend(s)
	cfg.AuthFailureBackoff = -1
	cfg.IdleExpire = -1 // pooled connections never expire while the test runs
	if mutate != nil {
		mutate(cfg)
	}
	pool := NewPool(cfg)
	t.Cleanup(func() { _ = pool.ClosePool() })
	return pool
}

// cliSessGet fetches one article through the pool within the hang guard.
func cliSessGet(t *testing.T, pool *Pool, id string) (*models.Article, error) {
	t.Helper()
	var art *models.Article
	err := cliSessRun(t, "GetArticle "+id, func() error {
		var err error
		art, err = pool.GetArticle(&id, true)
		return err
	})
	return art, err
}

// cliSessWant fetches one article through the pool and fails unless it arrives.
func cliSessWant(t *testing.T, pool *Pool, id string) {
	t.Helper()
	art, err := cliSessGet(t, pool, id)
	if err != nil {
		t.Fatalf("GetArticle %s: %v", id, err)
	}
	if art == nil || art.MessageID != id {
		t.Fatalf("GetArticle %s returned %+v", id, art)
	}
}

// cliSessWaitStats polls pool.Stats() until ok(st) holds. ForceCloseConn frees
// its slot from its own goroutine, so the counters settle asynchronously;
// cliWait is the hang guard, never the assertion.
func cliSessWaitStats(t *testing.T, pool *Pool, what string, ok func(PoolStats) bool) {
	t.Helper()
	deadline := time.Now().Add(cliWait)
	var st PoolStats
	for {
		st = pool.Stats()
		if ok(st) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pool stats %+v, want %s (within %v)", st, what, cliWait)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCliSessArticleFaults: one faulted ARTICLE in a running session (kit
// Faults.OverrideFrom / OverrideTimes / Override / OverrideLine / CloseAfter).
// Only that article fails; the connection is dropped or kept by reply class.
func TestCliSessArticleFaults(t *testing.T) {
	// 503 on the third ARTICLE of a connection: a 5xx drops the connection and
	// the following articles are served on a fresh one.
	t.Run("Override503", func(t *testing.T) {
		st, ids := cliSessStore(t, 6)
		s := cliStoreServer(t, st, cliFakeStoreOpts{}, cliFakeFaults{
			Override:      map[string][]string{"ARTICLE": {"503 fault"}},
			OverrideFrom:  map[string]int{"ARTICLE": 3},
			OverrideTimes: map[string]int{"ARTICLE": 1}, // the redialled connection must not fault again
		})
		pool := cliSessPool(t, s, nil)

		cliSessWant(t, pool, ids[0])
		cliSessWant(t, pool, ids[1])
		if got := s.Accepted(); got != 1 {
			t.Fatalf("Accepted() = %d before the fault, want 1", got)
		}

		if _, err := cliSessGet(t, pool, ids[2]); err == nil {
			t.Fatalf("GetArticle %s: no error, want the 503", ids[2])
		} else if !strings.Contains(err.Error(), "503") {
			t.Fatalf("GetArticle %s: err = %v, want it to carry the 503", ids[2], err)
		}

		// The faulted connection is gone: the next articles arrive on a new one.
		cliSessWant(t, pool, ids[3])
		cliSessWant(t, pool, ids[4])
		if got := s.Accepted(); got != 2 {
			t.Fatalf("Accepted() = %d after the 503, want 2 (connection dropped, one redial)", got)
		}
		cliSessWaitStats(t, pool, "1 active, 1 idle, 1 closed", func(ps PoolStats) bool {
			return ps.ActiveConnections == 1 && ps.IdleConnections == 1 && ps.TotalClosed == 1
		})
	})

	// 430 on the third ARTICLE: ErrArticleNotFound, the connection is healthy
	// and keeps serving (OverrideTimes 1: the fault fires once).
	t.Run("Override430", func(t *testing.T) {
		st, ids := cliSessStore(t, 6)
		s := cliStoreServer(t, st, cliFakeStoreOpts{}, cliFakeFaults{
			Override:      map[string][]string{"ARTICLE": {"430 no such article"}},
			OverrideFrom:  map[string]int{"ARTICLE": 3},
			OverrideTimes: map[string]int{"ARTICLE": 1},
		})
		pool := cliSessPool(t, s, nil)

		cliSessWant(t, pool, ids[0])
		cliSessWant(t, pool, ids[1])
		if _, err := cliSessGet(t, pool, ids[2]); !errors.Is(err, ErrArticleNotFound) {
			t.Fatalf("GetArticle %s: err = %v, want ErrArticleNotFound", ids[2], err)
		}
		cliSessWant(t, pool, ids[3])
		cliSessWant(t, pool, ids[4])

		if got := s.Accepted(); got != 1 {
			t.Fatalf("Accepted() = %d, want 1: a 430 keeps the connection", got)
		}
		cliSessWaitStats(t, pool, "1 active, 1 idle, 0 closed", func(ps PoolStats) bool {
			return ps.ActiveConnections == 1 && ps.IdleConnections == 1 && ps.TotalClosed == 0
		})
	})

	// EOF in the middle of the session (kit CloseAfter counts every client
	// line: MODE READER plus two ARTICLEs): the third request fails and the
	// pool dials again for the next one.
	t.Run("CloseAfterEOF", func(t *testing.T) {
		st, ids := cliSessStore(t, 6)
		s := cliStoreServer(t, st, cliFakeStoreOpts{}, cliFakeFaults{CloseAfter: 3})
		pool := cliSessPool(t, s, nil)

		cliSessWant(t, pool, ids[0])
		cliSessWant(t, pool, ids[1])
		if _, err := cliSessGet(t, pool, ids[2]); err == nil {
			t.Fatalf("GetArticle %s: no error on the closed connection", ids[2])
		}
		cliSessWant(t, pool, ids[3])
		cliSessWant(t, pool, ids[4])
		if got := s.Accepted(); got != 2 {
			t.Fatalf("Accepted() = %d after the EOF, want 2 (connection dropped, one redial)", got)
		}
		cliSessWaitStats(t, pool, "1 active, 1 closed", func(ps PoolStats) bool {
			return ps.ActiveConnections == 1 && ps.TotalClosed == 1
		})
	})

	// OverrideLine hits one article only: the other articles are untouched.
	t.Run("OverrideLine423", func(t *testing.T) {
		st, ids := cliSessStore(t, 6)
		s := cliStoreServer(t, st, cliFakeStoreOpts{}, cliFakeFaults{
			OverrideLine: map[string][]string{
				"ARTICLE " + ids[2]: {"423 no article with that number"},
			},
		})
		pool := cliSessPool(t, s, nil)

		cliSessWant(t, pool, ids[0])
		cliSessWant(t, pool, ids[1])
		before := s.Accepted()
		if _, err := cliSessGet(t, pool, ids[2]); err == nil {
			t.Fatalf("GetArticle %s: no error, want the 423", ids[2])
		} else if !strings.Contains(err.Error(), "423") {
			t.Fatalf("GetArticle %s: err = %v, want it to carry the 423", ids[2], err)
		}
		cliSessWant(t, pool, ids[3])
		cliSessWant(t, pool, ids[4])
		cliSessWant(t, pool, ids[5])

		// Finding (reported, not fixed here): BackendConn.GetArticle keeps the
		// connection only for 430/451; every other unexpected code - a 4xx like
		// this one included - is fatal for it, so the pool force-closes and
		// redials. Mapping more 4xx replies to a "connection healthy" error
		// needs a typed error in nntp-client-commands.go, outside this slice.
		t.Logf("after the 423 on one article: Accepted() %d -> %d (the connection is dropped)", before, s.Accepted())
	})
}

// TestCliSessAuthExpired: the provider answers 480 in a running authenticated
// session (the kit ends the login on any 480 it writes). The pool closes that
// connection, the next Get dials and authenticates again, and the command is
// served. Plan design point 2.
func TestCliSessAuthExpired(t *testing.T) {
	const user, pass = "fetcher", "secret"
	withCreds := func(cfg *BackendConfig) {
		cfg.Username = user
		cfg.Password = pass
	}

	// Pool.SelectGroup maps the 480 to ErrAuthRequired (nntp-backend-pool.go).
	t.Run("SelectGroup", func(t *testing.T) {
		st, _ := cliSessStore(t, 4)
		s := cliStoreServer(t, st, cliFakeStoreOpts{User: user, Pass: pass}, cliFakeFaults{
			Override:      map[string][]string{"GROUP": {"480 login expired"}},
			OverrideFrom:  map[string]int{"GROUP": 2}, // the first GROUP of a connection is served
			OverrideTimes: map[string]int{"GROUP": 1}, // and the fault fires once per connection
		})
		pool := cliSessPool(t, s, withCreds)

		sel := func() (*GroupInfo, error) {
			t.Helper()
			var gi *GroupInfo
			err := cliSessRun(t, "SelectGroup", func() error {
				var err error
				gi, err = pool.SelectGroup(cliSessGroup)
				return err
			})
			return gi, err
		}

		gi, err := sel()
		if err != nil {
			t.Fatalf("SelectGroup: %v", err)
		}
		if gi.Count != 4 {
			t.Fatalf("SelectGroup: count %d, want 4", gi.Count)
		}
		if got := s.Count("AUTHINFO"); got != 2 {
			t.Fatalf("AUTHINFO lines = %d after the first connection, want 2 (USER and PASS)", got)
		}

		if _, err := sel(); !errors.Is(err, ErrAuthRequired) {
			t.Fatalf("SelectGroup after the 480: err = %v, want ErrAuthRequired", err)
		}

		// Once: the next Get dials a new connection, authenticates again and
		// the group is served.
		gi, err = sel()
		if err != nil {
			t.Fatalf("SelectGroup after the re-dial: %v", err)
		}
		if gi.Count != 4 {
			t.Fatalf("SelectGroup after the re-dial: count %d, want 4", gi.Count)
		}
		if got := s.Accepted(); got != 2 {
			t.Fatalf("Accepted() = %d, want 2 (the 480 connection is closed, one re-dial)", got)
		}
		if got := s.Count("AUTHINFO"); got != 4 {
			t.Fatalf("AUTHINFO lines = %d, want 4 (the new connection logs in again)", got)
		}
		cliSessWaitStats(t, pool, "1 active, 1 idle, 1 closed", func(ps PoolStats) bool {
			return ps.ActiveConnections == 1 && ps.IdleConnections == 1 && ps.TotalClosed == 1
		})
	})

	// The same 480 on ARTICLE: the pool drops the connection and re-authenticates
	// on the next Get, so the caller sees the failure once and the fetch goes on.
	t.Run("GetArticle", func(t *testing.T) {
		st, ids := cliSessStore(t, 4)
		s := cliStoreServer(t, st, cliFakeStoreOpts{User: user, Pass: pass}, cliFakeFaults{
			Override:      map[string][]string{"ARTICLE": {"480 login expired"}},
			OverrideFrom:  map[string]int{"ARTICLE": 2},
			OverrideTimes: map[string]int{"ARTICLE": 1},
		})
		pool := cliSessPool(t, s, withCreds)

		cliSessWant(t, pool, ids[0])
		_, err := cliSessGet(t, pool, ids[1])
		if err == nil {
			t.Fatalf("GetArticle %s: no error, want the 480", ids[1])
		}
		if !strings.Contains(err.Error(), "480") {
			t.Fatalf("GetArticle %s: err = %v, want it to carry the 480", ids[1], err)
		}
		// Every reader command maps a 480 to ErrAuthRequired (authRequiredErr in
		// nntp-client-commands.go), not only GROUP, so the fetcher can tell an
		// expired login from any other failure through the pool's wrapping.
		if !errors.Is(err, ErrAuthRequired) {
			t.Fatalf("GetArticle %s after the 480: err = %v, want ErrAuthRequired", ids[1], err)
		}

		cliSessWant(t, pool, ids[2])
		if got := s.Accepted(); got != 2 {
			t.Fatalf("Accepted() = %d, want 2 (the 480 connection is closed, one re-dial)", got)
		}
		if got := s.Count("AUTHINFO"); got != 4 {
			t.Fatalf("AUTHINFO lines = %d, want 4 (the new connection logs in again)", got)
		}
		cliSessWaitStats(t, pool, "1 active, 1 idle, 1 closed", func(ps PoolStats) bool {
			return ps.ActiveConnections == 1 && ps.IdleConnections == 1 && ps.TotalClosed == 1
		})
	})
}

// TestCliSessServerMaxConns: the provider allows fewer connections than the
// pool (kit Options.MaxConns 1, pool MaxConns 2). The extra connect gets the
// "502 Too many connections" greeting; Pool.Get reports it, does not count the
// connection and does not retry it, and once a connection is free the next Get
// succeeds. Plan design point 2.
func TestCliSessServerMaxConns(t *testing.T) {
	st, ids := cliSessStore(t, 2)
	s := newCliFakeServer(t, cliFakeOpts{Store: st, MaxConns: 1})
	pool := cliSessPool(t, s, nil)

	get := func(what string) (*BackendConn, error) {
		t.Helper()
		var c *BackendConn
		err := cliSessRun(t, what, func() error {
			var err error
			c, err = pool.Get(MODE_READER_MV)
			return err
		})
		return c, err
	}

	c1, err := get("Get first")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	c2, err := get("Get over the server cap")
	if err == nil {
		t.Fatalf("second Get succeeded although the server allows one connection")
	}
	if c2 != nil {
		t.Fatalf("second Get returned a connection with err = %v", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("second Get: err = %v, want the 502 greeting in it", err)
	}
	if got := s.Accepted(); got != 2 {
		t.Fatalf("Accepted() = %d, want 2 (one rejected connect, no retry loop)", got)
	}
	ps := pool.Stats()
	if ps.ActiveConnections != 1 || ps.IdleConnections != 0 || ps.TotalCreated != 1 {
		t.Fatalf("Stats = %+v, want 1 active, 0 idle, 1 created: the rejected connect must not count", ps)
	}

	// After the Put the next Get is served from the pool: no second connect, so
	// the server cap is not hit again (Accepted stays 2, which also shows the
	// failed connect above was not retried in the background).
	if err := pool.Put(c1); err != nil {
		t.Fatalf("Put: %v", err)
	}
	c3, err := get("Get after Put")
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	if got := s.Accepted(); got != 2 {
		t.Fatalf("Accepted() = %d after the Put, want 2 (the idle connection is reused)", got)
	}

	// Only when the connection is really gone does the pool dial again, and now
	// the server accepts.
	c3.ForceCloseConn()
	s.WaitOpen(t, 0, cliWait)
	cliSessWaitStats(t, pool, "no connection left", func(st PoolStats) bool {
		return st.ActiveConnections == 0 && st.IdleConnections == 0
	})
	c4, err := get("Get after the close")
	if err != nil {
		t.Fatalf("Get after the close: %v", err)
	}
	if got := s.Accepted(); got != 3 {
		t.Fatalf("Accepted() = %d, want 3 (a new connect once a slot is free)", got)
	}
	art, err := c4.GetArticle(&ids[0], true)
	if err != nil {
		t.Fatalf("GetArticle on the new connection: %v", err)
	}
	if art.MessageID != ids[0] {
		t.Fatalf("GetArticle returned %q, want %q", art.MessageID, ids[0])
	}
	if err := pool.Put(c4); err != nil {
		t.Fatalf("Put: %v", err)
	}
}
