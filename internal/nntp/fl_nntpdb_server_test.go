package nntp

// Server tests of plan followup-leftovers, slice fl-nntp-db: a session drops its current group
// when an admin deactivates it (the next group-relative command answers 412, as on a fresh
// connection), STAT by message-id reads only the article number, and the local430 TTL is
// configurable (cmd/nntp-server -local430ttl).

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/nntp-testkit/client"
)

// flNntpdbExpect runs one command and fails unless it answers want.
func flNntpdbExpect(t *testing.T, c *client.Conn, want int, format string, args ...interface{}) string {
	t.Helper()
	code, line := c.Cmd(format, args...)
	if code != want {
		t.Fatalf("%s: got %q, want %d", fmt.Sprintf(format, args...), line, want)
	}
	return line
}

// flNntpdbSetActive changes the active flag of a group (what the web admin page does).
func flNntpdbSetActive(t *testing.T, group string, active bool) {
	t.Helper()
	if err := hTestDB(t).UpdateNewsgroupActive(group, active); err != nil {
		t.Fatalf("UpdateNewsgroupActive(%s, %v): %v", group, active, err)
	}
}

// TestFlNntpdbDeactivatedGroupDropsSelection: after GROUP, an admin deactivates the group: the
// next group-relative command answers 412 and the selection is gone (no stale current
// article), GROUP answers 411, and after reactivation GROUP selects it again.
func TestFlNntpdbDeactivatedGroupDropsSelection(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	g := hGroup(t, "flnntpdb.deact", 3, nil)
	c := hs.dial(t)

	flNntpdbExpect(t, c, 211, "GROUP %s", g)
	flNntpdbExpect(t, c, 223, "STAT 2")
	flNntpdbExpect(t, c, 224, "XOVER 1-3")
	if lines := c.ReadDotLines(); len(lines) != 3 {
		t.Fatalf("XOVER before the deactivation: %d lines, want 3", len(lines))
	}

	flNntpdbSetActive(t, g, false)
	flNntpdbExpect(t, c, 412, "STAT 1") // number form
	// the selection is gone for good, not only refused once
	for _, cmd := range []string{"STAT", "HEAD", "NEXT", "LAST", "XOVER 1-3", "OVER 1-3", "XHDR Subject 1-3", "HDR Subject 1-3", "LISTGROUP"} {
		flNntpdbExpect(t, c, 412, "%s", cmd)
	}
	flNntpdbExpect(t, c, 411, "GROUP %s", g)

	flNntpdbSetActive(t, g, true)
	flNntpdbExpect(t, c, 211, "GROUP %s", g)
	flNntpdbExpect(t, c, 223, "STAT 3")

	// LISTGROUP without an argument on a group deactivated since it was selected: 412 too
	flNntpdbSetActive(t, g, false)
	flNntpdbExpect(t, c, 412, "LISTGROUP")
	flNntpdbExpect(t, c, 412, "STAT 3")
}

// TestFlNntpdbDeletedGroupDropsSelection: a group deleted from the main database behaves like
// a deactivated one.
func TestFlNntpdbDeletedGroupDropsSelection(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	g := hGroup(t, "flnntpdb.del", 2, nil)
	c := hs.dial(t)
	flNntpdbExpect(t, c, 211, "GROUP %s", g)
	if _, err := hTestDB(t).GetMainDB().Exec("DELETE FROM newsgroups WHERE name = ?", g); err != nil {
		t.Fatalf("delete newsgroup %s: %v", g, err)
	}
	flNntpdbExpect(t, c, 412, "ARTICLE 1")
	flNntpdbExpect(t, c, 412, "NEXT")
}

// flNntpdbBreakFullRow makes the full article row of num unreadable (bytes holds text, so the
// scan of NNTPArticleByNum fails) while the index columns stay readable.
func flNntpdbBreakFullRow(t *testing.T, group string, num int64) {
	t.Helper()
	gdb, err := hTestDB(t).GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer gdb.Return()
	if _, err := gdb.DB.Exec("UPDATE articles SET bytes = 'not-a-number' WHERE article_num = ?", num); err != nil {
		t.Fatalf("break row %d of %s: %v", num, group, err)
	}
}

// TestFlNntpdbStatByMessageIDLean: STAT <message-id> reads only the article number, in the
// current group and in the group the history lookup names: with the full row unreadable,
// ARTICLE fails with 403 while STAT still answers 223.
func TestFlNntpdbStatByMessageIDLean(t *testing.T) {
	g := hGroup(t, "flnntpdb.stat", 2, nil)
	msgid := hMessageID(g, 2)
	flNntpdbBreakFullRow(t, g, 2)
	groupName := g
	proc := &hFakeProcessor{findFn: func(messageID, _ string) (*models.Article, error) {
		if messageID != msgid {
			return nil, fmt.Errorf("miss: %w", ErrArticleNotFound)
		}
		return &models.Article{MessageID: messageID, NewsgroupsPtr: []*string{&groupName}}, nil
	}}
	hs := newTestServer(t, hServerOpts{Proc: proc})
	c := hs.dial(t)

	// (b) no group selected: found through the processor, re-read in its group
	if line := flNntpdbExpect(t, c, 223, "STAT %s", msgid); line != "223 0 "+msgid+" Article exists" {
		t.Errorf("STAT outside the group: %q", line)
	}
	flNntpdbExpect(t, c, 403, "ARTICLE %s", msgid)

	// (a) the current group
	flNntpdbExpect(t, c, 211, "GROUP %s", g)
	if line := flNntpdbExpect(t, c, 223, "STAT %s", msgid); line != "223 2 "+msgid+" Article exists" {
		t.Errorf("STAT in the current group: %q", line)
	}
	flNntpdbExpect(t, c, 403, "HEAD %s", msgid)
	// STAT does not change the current article (RFC 3977 6.2.4.2 for the message-id form)
	if line := flNntpdbExpect(t, c, 223, "STAT"); line != "223 1 "+hMessageID(g, 1)+" Article exists" {
		t.Errorf("current article after STAT <message-id>: %q, want article 1", line)
	}
}

// TestFlNntpdbLocal430TTL: SetLocal430TTL changes how long a 430 is answered from the cache;
// <= 0 restores the default.
func TestFlNntpdbLocal430TTL(t *testing.T) {
	s, err := NewNNTPServer(&database.Database{}, &config.ServerConfig{}, new(sync.WaitGroup), nil)
	if err != nil {
		t.Fatalf("NewNNTPServer: %v", err)
	}
	if got := s.Local430TTL(); got != DefaultLocal430TTL {
		t.Fatalf("default TTL %v, want %v", got, DefaultLocal430TTL)
	}
	const id = "<fl-nntpdb-430@test.invalid>"
	s.SetLocal430TTL(time.Hour)
	if !s.local430.Add(id) || !s.local430.Check(id) {
		t.Fatal("a fresh 430 is not answered from the cache with a 1h TTL")
	}
	s.SetLocal430TTL(time.Nanosecond)
	if got := s.Local430TTL(); got != time.Nanosecond {
		t.Fatalf("TTL %v after SetLocal430TTL(1ns)", got)
	}
	time.Sleep(time.Millisecond) // older than the 1ns TTL
	if s.local430.Check(id) {
		t.Error("an entry older than the TTL is still answered from the cache")
	}
	s.local430.Add(id)
	time.Sleep(time.Millisecond)
	s.local430.Cleanup()
	s.local430.mu.RLock()
	n := len(s.local430.cache)
	s.local430.mu.RUnlock()
	if n != 0 {
		t.Errorf("Cleanup kept %d entries older than the TTL", n)
	}
	s.SetLocal430TTL(0)
	if got := s.Local430TTL(); got != DefaultLocal430TTL {
		t.Errorf("TTL after SetLocal430TTL(0) = %v, want the default %v", got, DefaultLocal430TTL)
	}
}

// ageForTest moves the arrival time of messageID back by d (as if it arrived d earlier) and
// reports whether the entry exists (plan post-430-window tests).
func (ra *RecentArrivals) ageForTest(messageID string, d time.Duration) bool {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	e, ok := ra.cache[messageID]
	if ok {
		e.at = e.at.Add(-d)
		ra.cache[messageID] = e
	}
	return ok
}

// recentArrivalsLen returns the number of entries.
func (ra *RecentArrivals) recentArrivalsLen() int {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	return len(ra.cache)
}

// TestFlNntpdbRecentArrivals: the recentArrivals table of plan post-430-window keeps the
// bounds of local430 (250-octet ids, max entries), forgets entries after ArrivalGrace (on
// lookup and in Cleanup), copies the groups and is safe on a nil table.
func TestFlNntpdbRecentArrivals(t *testing.T) {
	s, err := NewNNTPServer(&database.Database{}, &config.ServerConfig{}, new(sync.WaitGroup), nil)
	if err != nil {
		t.Fatalf("NewNNTPServer: %v", err)
	}
	ra := s.recentArrivals
	if ra == nil || s.local430.arrivals != ra {
		t.Fatal("NewNNTPServer: recentArrivals missing or not cleaned by local430.run")
	}

	t.Run("bounds", func(t *testing.T) {
		long := "<" + strings.Repeat("a", 251-len("<@x>")) + "@x>" // 251 octets
		if len(long) != 251 {
			t.Fatalf("test id has %d octets", len(long))
		}
		if ra.NoteArrival(long, []string{"g"}) {
			t.Error("a 251-octet id was stored")
		}
		if _, ok := ra.RecentArrival(long); ok {
			t.Error("a 251-octet id is a recent arrival")
		}
		maxID := long[:249] + ">" // 250 octets
		groups := []string{"fl.a", "fl.b"}
		if !ra.NoteArrival(maxID, groups) {
			t.Fatal("a 250-octet id was not stored")
		}
		groups[0] = "changed" // the table keeps its own copy
		if got, ok := ra.RecentArrival(maxID); !ok || len(got) != 2 || got[0] != "fl.a" || got[1] != "fl.b" {
			t.Errorf("RecentArrival(250-octet id) = %q, %v; want [fl.a fl.b]", got, ok)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		const id = "<fl-nntpdb-arrival-expiry@test.invalid>"
		ra.NoteArrival(id, nil)
		if _, ok := ra.RecentArrival(id); !ok {
			t.Fatal("a fresh arrival without groups is not a recent arrival")
		}
		ra.ageForTest(id, ArrivalGrace)
		if _, ok := ra.RecentArrival(id); ok {
			t.Error("an arrival older than ArrivalGrace is still recent")
		}
		if ra.ageForTest(id, 0) {
			t.Error("the expired entry was not removed by the lookup")
		}
	})

	t.Run("cleanup", func(t *testing.T) {
		c := newRecentArrivals()
		const oldID, youngID = "<fl-nntpdb-arrival-old@test.invalid>", "<fl-nntpdb-arrival-young@test.invalid>"
		c.NoteArrival(oldID, []string{"g"})
		c.NoteArrival(youngID, []string{"g"})
		c.ageForTest(oldID, ArrivalGrace+time.Second)
		c.Cleanup()
		if c.ageForTest(oldID, 0) {
			t.Error("Cleanup kept an entry older than ArrivalGrace")
		}
		if _, ok := c.RecentArrival(youngID); !ok {
			t.Error("Cleanup removed a young entry")
		}
	})

	t.Run("cap", func(t *testing.T) {
		c := newRecentArrivals()
		for i := 0; i < recentArrivalsMaxEntries; i++ {
			if !c.NoteArrival(fmt.Sprintf("<fl-cap-%d@test.invalid>", i), nil) {
				t.Fatalf("entry %d refused below the cap", i)
			}
		}
		const extra = "<fl-cap-extra@test.invalid>"
		if c.NoteArrival(extra, nil) {
			t.Error("a new id was stored in a full table of young entries")
		}
		if !c.NoteArrival("<fl-cap-0@test.invalid>", []string{"g"}) {
			t.Error("an id already in the full table could not be refreshed")
		}
		// expired entries make room
		c.ageForTest("<fl-cap-1@test.invalid>", ArrivalGrace)
		if !c.NoteArrival(extra, nil) {
			t.Error("a new id was refused although an entry had expired")
		}
		if n := c.recentArrivalsLen(); n != recentArrivalsMaxEntries {
			t.Errorf("table holds %d entries, want the cap %d", n, recentArrivalsMaxEntries)
		}
	})

	t.Run("nil", func(t *testing.T) {
		var n *RecentArrivals
		if n.NoteArrival("<fl-nil@test.invalid>", nil) {
			t.Error("a nil table stored an id")
		}
		if _, ok := n.RecentArrival("<fl-nil@test.invalid>"); ok {
			t.Error("a nil table found an id")
		}
		n.Cleanup()
	})
}
