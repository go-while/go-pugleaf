package nntp

// Bogus newsgroup names (plan e2e-gap-tests, slice intake-wire, G12; source: INN
// tests/data/overview/bogus). Names like "..foo..", "../.." and "/" are not newsgroup
// names (RFC 5536 §3.1.4): the server must answer 411 for them, must never create a group
// database for such a name (SEC-2: database.GetGroupDB would create a file for any name,
// and SanitizeGroupName maps "../.." and "/" to the same "junk"), and must refuse an
// article whose Newsgroups header carries one.
//
// Identifiers use the gn / TestGn prefix (contract K2).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/nntp-testkit/client"
)

// gnBogusNames are the names under test: path traversal, a bare separator and empty
// components. None of them is a valid newsgroup name.
func gnBogusNames() []string {
	return []string{"..foo..", "../..", "/", ".", ".."}
}

// gnSnapshot lists what the data directory holds now: every entry under <DataDir>/db
// (grpDBEntries) plus the database files directly in <DataDir>, so a group database
// created outside db/ is seen as well.
func gnSnapshot(t *testing.T) map[string]bool {
	t.Helper()
	out := grpDBEntries(t)
	m, err := filepath.Glob(filepath.Join(hDataDir(t), "*.sq3*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, p := range m {
		out[p] = true
	}
	return out
}

// gnNoNewFiles fails when anything was created under <DataDir>/db or <DataDir> since
// before, and when any of names got a newsgroups row.
func gnNoNewFiles(t *testing.T, before map[string]bool, what string, names []string) {
	t.Helper()
	db := hTestDB(t)
	var errs []error
	for p := range gnSnapshot(t) {
		if !before[p] {
			errs = append(errs, fmt.Errorf("%s created %s", what, p))
		}
	}
	for _, name := range names {
		if _, err := db.MainDBGetNewsgroup(name); err == nil {
			errs = append(errs, fmt.Errorf("%s created a newsgroups row for %q", what, name))
		}
	}
	if err := errors.Join(errs...); err != nil {
		t.Error(err)
	}
}

// TestGnSelectBogusGroup: GROUP and LISTGROUP of a bogus name answer 411 (RFC 3977
// §6.1.1.2, §6.1.2.2), create no group database and leave the session without a selection,
// so a group-relative command such as XHDR answers 412 and not data of another group.
func TestGnSelectBogusGroup(t *testing.T) {
	names := gnBogusNames()
	before := gnSnapshot(t)
	hs := newTestServer(t, hServerOpts{})

	for _, name := range names {
		t.Run("name="+name, func(t *testing.T) {
			c := hs.dial(t)
			code, line := c.Cmd("GROUP %s", name)
			if err := client.ExpectCode(code, line, 411); err != nil {
				t.Errorf("GROUP %q: %v", name, err)
			}
			code, line = c.Cmd("LISTGROUP %s", name)
			if code == 211 {
				c.ReadDotLines()
			}
			if err := client.ExpectCode(code, line, 411); err != nil {
				t.Errorf("LISTGROUP %q: %v", name, err)
			}
			// No group was selected by either (RFC 3977 §6.1.1.2: the current group
			// stays unchanged), so the group-relative commands say 412.
			code, line = c.Cmd("XHDR Subject 1-5")
			if err := client.ExpectCode(code, line, 412); err != nil {
				t.Errorf("XHDR after GROUP %q: %v", name, err)
			}
			code, line = c.Cmd("LISTGROUP")
			if err := client.ExpectCode(code, line, 412); err != nil {
				t.Errorf("bare LISTGROUP after GROUP %q: %v", name, err)
			}
		})
	}
	gnNoNewFiles(t, before, "GROUP/LISTGROUP of a bogus name", names)
}

// TestGnBogusNewsgroupsHeader: an article whose Newsgroups header holds a bogus name is
// refused (437 for IHAVE, 439 for TAKETHIS, 441 for POST: common.ParseNewsgroups refuses
// the header, so the article is invalid), nothing is stored and no group database appears.
// Pinned: one bogus name in a crosspost refuses the whole article, it is not dropped from
// the list.
func TestGnBogusNewsgroupsHeader(t *testing.T) {
	good := hGroup(t, "t11.bogushdr", 0, nil)
	names := gnBogusNames()
	before := gnSnapshot(t)

	t.Run("feed", func(t *testing.T) {
		proc := iwPass()
		hs := iwPeerServer(t, proc, 0, 0)
		c := hs.dial(t)
		for i, name := range names {
			for _, groups := range []string{name, good + "," + name} {
				id := hMessageID("t11.bogushdr", int64(i+1))
				head := iwHead(id, groups, "bogus newsgroups")
				code, line, _ := c.Ihave(id, iwWire(head, []string{"body"}))
				if err := client.ExpectCode(code, line, 437); err != nil {
					t.Errorf("IHAVE with Newsgroups: %q: %v", groups, err)
				}
				id = hMessageID("t11.bogushdr", int64(i+1))
				head = iwHead(id, groups, "bogus newsgroups")
				code, line, _ = c.Takethis(id, iwWire(head, []string{"body"}))
				if err := client.ExpectCode(code, line, 439); err != nil {
					t.Errorf("TAKETHIS with Newsgroups: %q: %v", groups, err)
				}
			}
		}
		iwWantProcessed(t, proc)
	})

	t.Run("post", func(t *testing.T) {
		proc := iwPass()
		hs := newTestServer(t, hServerOpts{Proc: proc, Hostname: peerFeedHost})
		user, pass := hUser(t, true, true)
		c := hs.dial(t)
		authMustLogin(t, c, user, pass)
		for i, name := range names {
			for _, groups := range []string{name, good + "," + name} {
				id := hMessageID("t11.bogushdr-post", int64(i+1))
				head := iwHead(id, groups, "bogus newsgroups")
				code, line, sent := c.Post(iwWire(head, []string{"body"}))
				if err := client.ExpectCode(code, line, 441); err != nil || !sent {
					t.Errorf("POST with Newsgroups: %q: %v (sent=%v, %q)", groups, err, sent, line)
				}
			}
		}
		iwWantProcessed(t, proc)
	})

	gnNoNewFiles(t, before, "an article with a bogus Newsgroups header", names)
}

// TestGnSanitizeCollision: the names "../.." and "/" both sanitize to "junk", so a server
// that opened a group database for an unknown name would make them share one file. The
// group database directory must hold no "junk" entry after both were tried.
func TestGnSanitizeCollision(t *testing.T) {
	for _, name := range []string{"../..", "/"} {
		if got := database.SanitizeGroupName(name); got != "junk" {
			t.Fatalf("SanitizeGroupName(%q) = %q, want junk (the test's premise)", name, got)
		}
	}
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	for _, name := range []string{"../..", "/"} {
		code, line := c.Cmd("GROUP %s", name)
		if err := client.ExpectCode(code, line, 411); err != nil {
			t.Errorf("GROUP %q: %v", name, err)
		}
	}
	root := filepath.Join(hDataDir(t), "db")
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", root, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "junk") {
			t.Errorf("GROUP of a bogus name created %s", filepath.Join(root, e.Name()))
		}
	}
}
