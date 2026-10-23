package nntp

// Hostile-peer read bounds of plan fetcher-hostile-peer, slice
// client-read-bounds: design point 1 against the kit's fault hooks
// (fakeserver.Faults LongLine, Endless, Drip, LineDelay) in Store mode.
//
//   - a reply line over BackendConfig.MaxResponseLineBytes fails with
//     ErrResponseLineTooLong, the connection is unusable afterwards (the
//     sticky line error of nntp-client-conn.go) and a pool never hands that
//     connection out again;
//   - an endless multi-line reply ends in ErrResponseTooLarge (the octet
//     budget) or the MaxReadLines* error, never in a hang;
//   - a dripped reply that keeps making progress completes and parses
//     unchanged: ReadTimeout is the idle timeout per Read, not per reply;
//   - a pause between the reply lines over ReadTimeout is a timeout error and
//     the pooled connection is closed.
//
// The caps are set through the exported BackendConfig fields only; the
// package constants stay as they are. There is no wall-clock assertion: the
// deadlines (cliWait, the kit's Options.Deadline) are hang guards.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const cliHostileGroup = "hostile.test"

// cliHostileLine is the line length of the LongLine fault; cliHostileMaxLine
// is the response line cap of those tests (well under it).
const (
	cliHostileLine    = 4096
	cliHostileMaxLine = 512
	// cliHostileBudget is the response octet budget of the Endless tests: an
	// endless reply crosses it after a few hundred lines.
	cliHostileBudget = 8 << 10
)

// cliHostileMsgID is the message-id of the n-th stored article.
func cliHostileMsgID(n int) string {
	return fmt.Sprintf("<hostile%d@test.invalid>", n)
}

// cliHostileStore returns a store with cliHostileGroup and n articles
// numbered 1..n; body is the body of every article (one line when empty).
func cliHostileStore(n int, body []string) *cliFakeStore {
	st := cliFakeNewStore()
	st.AddGroup(cliHostileGroup, "hostile peer test group")
	for i := 1; i <= n; i++ {
		b := body
		if len(b) == 0 {
			b = []string{fmt.Sprintf("body of article %d", i)}
		}
		st.AddArticle(cliHostileGroup, cliFakeArticle{
			MessageID: cliHostileMsgID(i),
			Headers: []string{
				"From: hostile <h@test.invalid>",
				fmt.Sprintf("Subject: hostile %d", i),
			},
			Body: b,
		})
	}
	return st
}

// cliHostileLongLine returns Faults with the LongLine hook set for cmd.
func cliHostileLongLine(cmd string) cliFakeFaults {
	return cliFakeFaults{LongLine: map[string]int{cmd: cliHostileLine}}
}

// cliHostileCap sets the response line cap of a hostile test.
func cliHostileCap(cfg *BackendConfig) {
	cfg.MaxResponseLineBytes = cliHostileMaxLine
}

// cliHostileCall is one client command run against the fake.
type cliHostileCall struct {
	name string                   // subtest name
	cmd  string                   // NNTP command the fault is keyed on
	conn func(*BackendConn) error // run it on a bare connection
	pool func(*Pool) error        // run it through the pool
}

// cliHostileReaders are the multi-line commands of design point 1 that go
// through a pool helper as well.
func cliHostileReaders() []cliHostileCall {
	msgID := cliHostileMsgID(1)
	return []cliHostileCall{
		{
			name: "article",
			cmd:  "ARTICLE",
			conn: func(c *BackendConn) error { _, err := c.GetArticle(&msgID, true); return err },
			pool: func(p *Pool) error { _, err := p.GetArticle(&msgID, true); return err },
		},
		{
			name: "xover",
			cmd:  "XOVER",
			conn: func(c *BackendConn) error { _, err := c.XOver(cliHostileGroup, 1, 3, false); return err },
			pool: func(p *Pool) error { _, err := p.XOver(cliHostileGroup, 1, 3, false); return err },
		},
		{
			name: "list active",
			cmd:  "LIST",
			conn: func(c *BackendConn) error { _, err := c.ListGroups(); return err },
			pool: func(p *Pool) error { _, err := p.ListNewsgroups(); return err },
		},
	}
}

// A line over MaxResponseLineBytes fails with ErrResponseLineTooLong, already
// in the greeting (the cap is armed before the welcome is read).
func TestCliHostileLongLineGreeting(t *testing.T) {
	// The kit applies Faults to command replies only, so the over-long
	// greeting is configured as the greeting itself.
	s := newCliFakeServer(t, cliFakeOpts{
		Greeting:  "200 " + strings.Repeat("x", cliHostileLine),
		Store:     cliHostileStore(3, nil),
		StoreOpts: cliFakeStoreOpts{},
	})
	var c *BackendConn
	_, err := cliCmdRun(t, func() error {
		var e error
		c, e = cliDial(t, s, func(cfg *BackendConfig) {
			cfg.AuthFailureBackoff = -1
			cliHostileCap(cfg)
		})
		return e
	})
	if err == nil {
		_ = c.CloseFromPoolOnly()
		t.Fatal("Connect accepted a greeting over MaxResponseLineBytes")
	}
	if !errors.Is(err, ErrResponseLineTooLong) {
		t.Fatalf("Connect: err = %v, want ErrResponseLineTooLong", err)
	}
}

// A long line in a multi-line reply fails with ErrResponseLineTooLong and the
// connection stays unusable: the next command on it fails with the same
// sticky error.
func TestCliHostileLongLineSticky(t *testing.T) {
	for _, tc := range cliHostileReaders() {
		t.Run(tc.name, func(t *testing.T) {
			s := cliStoreServer(t, cliHostileStore(3, nil), cliFakeStoreOpts{}, cliHostileLongLine(tc.cmd))
			c := cliCmdDial(t, s, cliHostileCap)
			if _, err := cliCmdRun(t, func() error { return tc.conn(c) }); !errors.Is(err, ErrResponseLineTooLong) {
				t.Fatalf("%s: err = %v, want ErrResponseLineTooLong", tc.cmd, err)
			}
			// sticky: every later read on this connection fails the same way
			_, err := cliCmdRun(t, func() error { _, _, e := c.SelectGroup(cliHostileGroup); return e })
			if !errors.Is(err, ErrResponseLineTooLong) {
				t.Fatalf("GROUP after %s: err = %v, want the sticky ErrResponseLineTooLong", tc.cmd, err)
			}
		})
	}
}

// The pool never hands out a connection poisoned by an over-long line: the
// next Get dials a new connection (s.Accepted() grows) and that one works.
func TestCliHostileLongLinePool(t *testing.T) {
	for _, tc := range cliHostileReaders() {
		t.Run(tc.name, func(t *testing.T) {
			s := cliStoreServer(t, cliHostileStore(3, nil), cliFakeStoreOpts{}, cliHostileLongLine(tc.cmd))
			pool := cliCmdPool(t, s, cliHostileCap)
			if _, err := cliCmdRun(t, func() error { return tc.pool(pool) }); !errors.Is(err, ErrResponseLineTooLong) {
				t.Fatalf("pool %s: err = %v, want ErrResponseLineTooLong", tc.cmd, err)
			}
			if idle := pool.Stats().IdleConnections; idle != 0 {
				t.Fatalf("IdleConnections = %d after %s, want 0", idle, tc.cmd)
			}
			accepted := s.Accepted()
			var conn *BackendConn
			if _, err := cliCmdRun(t, func() error {
				var e error
				conn, e = pool.Get(MODE_READER_MV)
				return e
			}); err != nil {
				t.Fatalf("pool.Get after %s: %v", tc.cmd, err)
			}
			defer func() { _ = pool.Put(conn) }()
			if got := s.Accepted(); got <= accepted {
				t.Fatalf("Accepted() = %d after pool.Get, was %d: the poisoned connection was handed out again", got, accepted)
			}
			// the fresh connection is usable (GROUP is not faulted)
			if _, err := cliCmdRun(t, func() error { _, _, e := conn.SelectGroup(cliHostileGroup); return e }); err != nil {
				t.Fatalf("GROUP on the connection from pool.Get: %v", err)
			}
		})
	}
}

// cliHostileBounded accepts the two error classes design point 1 allows for an
// endless reply: the octet budget (ErrResponseTooLarge) and the MaxReadLines*
// line limit of readMultilineResponse.
func cliHostileBounded(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error on an endless reply", what)
	}
	if errors.Is(err, ErrResponseTooLarge) || strings.Contains(err.Error(), "too many lines in response") {
		return
	}
	t.Fatalf("%s: err = %v, want ErrResponseTooLarge or the MaxReadLines* limit", what, err)
}

// An endless multi-line reply ends in an error, not in a hang: cliCmdRun's
// deadline is the hang guard only.
func TestCliHostileEndless(t *testing.T) {
	msgID := cliHostileMsgID(1)
	cases := []cliHostileCall{
		{name: "article", cmd: "ARTICLE", conn: func(c *BackendConn) error { _, err := c.GetArticle(&msgID, true); return err }},
		{name: "body", cmd: "BODY", conn: func(c *BackendConn) error { _, err := c.GetBody(msgID); return err }},
		{name: "xover", cmd: "XOVER", conn: func(c *BackendConn) error { _, err := c.XOver(cliHostileGroup, 1, 3, false); return err }},
		{name: "listgroup", cmd: "LISTGROUP", conn: func(c *BackendConn) error { _, err := c.ListGroup(cliHostileGroup, 0, 0); return err }},
		{name: "list active", cmd: "LIST", conn: func(c *BackendConn) error { _, err := c.ListGroups(); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := cliStoreServer(t, cliHostileStore(3, nil), cliFakeStoreOpts{},
				cliFakeFaults{Endless: map[string]bool{tc.cmd: true}})
			c := cliCmdDial(t, s, func(cfg *BackendConfig) {
				// small octet budgets through the exported fields only
				cfg.MaxArticleResponseBytes = cliHostileBudget
				cfg.MaxListResponseBytes = cliHostileBudget
			})
			_, err := cliCmdRun(t, func() error { return tc.conn(c) })
			cliHostileBounded(t, tc.cmd, err)
		})
	}
}

// A reply dripped one byte at a time completes and parses unchanged: the idle
// timeout is per Read, not per reply. The drip pause is far below ReadTimeout
// while the whole reply takes longer than one ReadTimeout to arrive.
func TestCliHostileDrip(t *testing.T) {
	const (
		dripPause   = time.Millisecond
		readTimeout = 2 * time.Second
		bodyLines   = 50
	)
	body := make([]string, bodyLines)
	for i := range body {
		body[i] = strings.Repeat("d", 64)
	}
	dial := func(t *testing.T, cmd string) (*cliFakeServer, *BackendConn) {
		t.Helper()
		s := cliStoreServer(t, cliHostileStore(3, body), cliFakeStoreOpts{},
			cliFakeFaults{Drip: map[string]time.Duration{cmd: dripPause}})
		return s, cliCmdDial(t, s, func(cfg *BackendConfig) { cfg.ReadTimeout = readTimeout })
	}

	t.Run("group", func(t *testing.T) {
		_, c := dial(t, "GROUP")
		var gi *GroupInfo
		if _, err := cliCmdRun(t, func() error {
			var e error
			gi, _, e = c.SelectGroup(cliHostileGroup)
			return e
		}); err != nil {
			t.Fatalf("SelectGroup on a dripped reply: %v", err)
		}
		if gi == nil || gi.Count != 3 || gi.First != 1 || gi.Last != 3 {
			t.Fatalf("GroupInfo = %+v, want count 3 first 1 last 3", gi)
		}
	})

	t.Run("article", func(t *testing.T) {
		_, c := dial(t, "ARTICLE")
		msgID := cliHostileMsgID(2)
		var art = struct {
			id    string
			lines int
		}{}
		if _, err := cliCmdRun(t, func() error {
			a, e := c.GetArticle(&msgID, true)
			if e != nil {
				return e
			}
			art.id, art.lines = a.MessageID, a.Lines
			return nil
		}); err != nil {
			t.Fatalf("GetArticle on a dripped reply: %v", err)
		}
		if art.id != msgID || art.lines != bodyLines {
			t.Fatalf("article %q with %d body lines, want %q with %d", art.id, art.lines, msgID, bodyLines)
		}
	})
}

// A pause between the lines of a reply over ReadTimeout is a timeout error
// and the pooled connection is closed, not returned to the pool.
func TestCliHostileLineDelay(t *testing.T) {
	s := cliStoreServer(t, cliHostileStore(3, nil), cliFakeStoreOpts{},
		cliFakeFaults{LineDelay: map[string]time.Duration{"ARTICLE": time.Second}})
	pool := cliCmdPool(t, s, func(cfg *BackendConfig) { cfg.ReadTimeout = 300 * time.Millisecond })
	msgID := cliHostileMsgID(1)
	_, err := cliCmdRun(t, func() error { _, e := pool.GetArticle(&msgID, true); return e })
	if err == nil {
		t.Fatal("GetArticle succeeded on a reply delayed beyond ReadTimeout")
	}
	if !cliConnIsTimeout(err) {
		t.Fatalf("GetArticle: err = %v, want a timeout (ReadTimeout 300ms)", err)
	}
	s.WaitOpen(t, 0, cliWait)
	if idle := pool.Stats().IdleConnections; idle != 0 {
		t.Fatalf("IdleConnections = %d, want 0", idle)
	}
}
