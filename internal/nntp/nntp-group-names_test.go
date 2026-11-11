package nntp

// Bogus newsgroup names (plan e2e-gap-tests, slice intake-wire, G12; source: INN
// tests/data/overview/bogus). Names like "..foo..", "../.." and "/" are not newsgroup
// names (RFC 5536 §3.1.4): the server must answer 411 for them, must never create a group
// database for such a name (SEC-2: database.GetGroupDB creates one for any name), and must
// refuse an article whose Newsgroups header carries one.
//
// A group database lives at <DataDir>/db/<MD5Hash(name)>/<SanitizeGroupName(name)>.db
// (db_groupdbs.go): the directory comes from the raw name, the file name from the
// sanitized one, and SanitizeGroupName maps "../..", "/", "." and ".." all to "junk".
// The checks below therefore look at the directory GetGroupDB would create for the bogus
// name and at that sanitized basename under any directory, instead of diffing the whole
// shared data directory (which other tests write to, and which -shuffle reorders).
//
// Identifiers use the gn / TestGn prefix (contract K2).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/nntp-testkit/client"
)

// gnBogusNames are the names under test: path traversal, a bare separator and empty
// components. None of them is a valid newsgroup name.
func gnBogusNames() []string {
	return []string{"..foo..", "../..", "/", ".", ".."}
}

// gnGroupDBDir is the directory GetGroupDB would create for name:
// <DataDir>/db/<MD5Hash(name)>, hashed from the raw name
// (database.GroupHashMap.GroupToHash is MD5Hash).
func gnGroupDBDir(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(hDataDir(t), "db", database.MD5Hash(name))
}

// gnLayoutPremise checks what the negative tests below rely on: for a group that really
// has a database, gnGroupDBDir names the directory it lives in and the file inside is
// <SanitizeGroupName(name)>.db. Without this a wrong path would make every "no group
// database was created" check pass for the wrong reason.
func gnLayoutPremise(t *testing.T) {
	t.Helper()
	g := hGroup(t, "t11.layout", 1, nil) // one article, so the group DB exists
	dir := gnGroupDBDir(t, g)
	want := filepath.Join(dir, database.SanitizeGroupName(g)+".db")
	if _, err := os.Stat(want); err != nil {
		entries, _ := os.ReadDir(dir)
		held := make([]string, 0, len(entries))
		for _, e := range entries {
			held = append(held, e.Name())
		}
		t.Fatalf("group %s has no database at %s (%v): the layout of gnGroupDBDir is wrong, so the checks below prove nothing", g, want, held)
	}
	if m, err := filepath.Glob(filepath.Join(hDataDir(t), "db", "*", database.SanitizeGroupName(g)+".db*")); err != nil || len(m) == 0 {
		t.Fatalf("glob for %s found nothing (%v): the basename glob of the checks below never matches", g, err)
	}
}

// gnNoGroupDB fails when a group database exists for any of names: neither the directory
// GetGroupDB would create for it, nor a file named after its sanitized form under any
// directory of <DataDir>/db (the sanitized names of the bogus names are "foo" and "junk",
// which no group of the harness, always "<prefix>.<seq>", can produce). It also fails when
// the name got a newsgroups row. what names the command under test.
func gnNoGroupDB(t *testing.T, what string, names []string) {
	t.Helper()
	db := hTestDB(t)
	var errs []error
	for _, name := range names {
		dir := gnGroupDBDir(t, name)
		switch entries, err := os.ReadDir(dir); {
		case err == nil:
			held := make([]string, 0, len(entries))
			for _, e := range entries {
				held = append(held, e.Name())
			}
			errs = append(errs, fmt.Errorf("%s created %s (holding %v) for name %q", what, dir, held, name))
		case !os.IsNotExist(err):
			t.Fatalf("read %s: %v", dir, err)
		}
		// The same file under another hash directory (the SanitizeGroupName collision),
		// including its -wal and -shm companions.
		pat := filepath.Join(hDataDir(t), "db", "*", database.SanitizeGroupName(name)+".db*")
		m, err := filepath.Glob(pat)
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		for _, p := range m {
			errs = append(errs, fmt.Errorf("%s created %s for name %q", what, p, name))
		}
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
	gnLayoutPremise(t)
	names := gnBogusNames()
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
	gnNoGroupDB(t, "GROUP/LISTGROUP of a bogus name", names)
}

// TestGnBogusNewsgroupsHeader: an article whose Newsgroups header holds a bogus name is
// refused (437 for IHAVE, 439 for TAKETHIS, 441 for POST: common.ParseNewsgroups refuses
// the header, so the article is invalid), nothing is stored and no group database appears.
// Pinned: one bogus name in a crosspost refuses the whole article, it is not dropped from
// the list. Every probe uses a fresh message-id, so a refusal one probe remembers can never
// decide the answer to a later one.
func TestGnBogusNewsgroupsHeader(t *testing.T) {
	good := hGroup(t, "t11.bogushdr", 0, nil)
	names := gnBogusNames()

	t.Run("feed", func(t *testing.T) {
		proc := iwPass()
		hs := iwPeerServer(t, proc, 0, 0)
		c := hs.dial(t)
		for _, name := range names {
			for _, groups := range []string{name, good + "," + name} {
				id := iwID("t11.bogushdr")
				code, line, _ := c.Ihave(id, iwWire(iwHead(id, groups, "bogus newsgroups"), []string{"body"}))
				if err := client.ExpectCode(code, line, 437); err != nil {
					t.Errorf("IHAVE %s with Newsgroups: %q: %v", id, groups, err)
				}
				id = iwID("t11.bogushdr")
				code, line, _ = c.Takethis(id, iwWire(iwHead(id, groups, "bogus newsgroups"), []string{"body"}))
				if err := client.ExpectCode(code, line, 439); err != nil {
					t.Errorf("TAKETHIS %s with Newsgroups: %q: %v", id, groups, err)
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
		for _, name := range names {
			for _, groups := range []string{name, good + "," + name} {
				id := iwID("t11.bogushdr-post")
				code, line, sent := c.Post(iwWire(iwHead(id, groups, "bogus newsgroups"), []string{"body"}))
				if err := client.ExpectCode(code, line, 441); err != nil || !sent {
					t.Errorf("POST %s with Newsgroups: %q: %v (sent=%v, %q)", id, groups, err, sent, line)
				}
			}
		}
		iwWantProcessed(t, proc)
	})

	gnNoGroupDB(t, "an article with a bogus Newsgroups header", names)
}

// TestGnSanitizeCollision: "../..", "/", "." and ".." all sanitize to "junk", so a server
// that opened a group database for an unknown name would make them share one file (their
// hash directories differ, the file name does not). After all of them were tried there
// must be no group database for any of them at all: no "junk" file anywhere under
// <DataDir>/db and none of the hash directories GetGroupDB would have created.
func TestGnSanitizeCollision(t *testing.T) {
	names := []string{"../..", "/", ".", ".."}
	for _, name := range names {
		if got := database.SanitizeGroupName(name); got != "junk" {
			t.Fatalf("SanitizeGroupName(%q) = %q, want junk (the test's premise)", name, got)
		}
	}
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	for _, name := range names {
		code, line := c.Cmd("GROUP %s", name)
		if err := client.ExpectCode(code, line, 411); err != nil {
			t.Errorf("GROUP %q: %v", name, err)
		}
	}
	// Explicit here (gnNoGroupDB checks the same glob per name): no junk* under any
	// directory of <DataDir>/db.
	pat := filepath.Join(hDataDir(t), "db", "*", "junk*")
	m, err := filepath.Glob(pat)
	if err != nil {
		t.Fatalf("glob %s: %v", pat, err)
	}
	for _, p := range m {
		t.Errorf("GROUP of a bogus name created %s", p)
	}
	gnNoGroupDB(t, "GROUP of a name that sanitizes to junk", names)
}
