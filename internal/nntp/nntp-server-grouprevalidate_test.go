package nntp

// SEC-2 residual: a session re-validates a group in the main database before it opens the
// group's database, both for the group it selected earlier and for a group name a message-id
// lookup produced (openSelectableGroupDB / openCurrentGroupDB / currentGroupDBForMsgID).
//
// An admin may deactivate or delete a group after a session selected it (or after the
// history index noted an article in it). The session must not keep serving that group, and
// for a group that is gone from the main database it must not re-create its group database
// file (database.GetGroupDB creates one for any name).
//
// Identifiers here start with Grv/grv.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/nntp-testkit/client"
)

// grvExpect runs one command and fails unless it answers want.
func grvExpect(t *testing.T, c *client.Conn, want int, format string, args ...interface{}) string {
	t.Helper()
	code, line := c.Cmd(format, args...)
	if code != want {
		t.Fatalf("%s: got %q, want %d", fmt.Sprintf(format, args...), line, want)
	}
	return line
}

// grvDeleteGroup removes the newsgroups row of group (its group database file stays on
// disk, as after a web admin delete that only drops the row).
func grvDeleteGroup(t *testing.T, group string) {
	t.Helper()
	if _, err := hTestDB(t).GetMainDB().Exec("DELETE FROM newsgroups WHERE name = ?", group); err != nil {
		t.Fatalf("delete newsgroup %s: %v", group, err)
	}
}

// grvNewEntries returns the entries under <DataDir>/db that are in after but not in before
// and whose name holds the sanitized group name, plus every new directory (a group database
// lives in its own hierarchy directory).
func grvNewEntries(t *testing.T, before, after map[string]bool, group string) []string {
	t.Helper()
	sanitized := database.SanitizeGroupName(group)
	var created []string
	for p := range after {
		if before[p] {
			continue
		}
		fi, err := os.Stat(p)
		if strings.Contains(filepath.Base(p), sanitized) || (err == nil && fi.IsDir()) {
			created = append(created, p)
		}
	}
	return created
}

// TestGrvMsgIDCurrentGroupDeleted: the message-id forms search the current group first.
// When its newsgroups row is gone the group must not be read any more: the lookup skips it
// (430 without another source) and the stale selection is dropped (the next group-relative
// command answers 412).
func TestGrvMsgIDCurrentGroupDeleted(t *testing.T) {
	hs := newTestServer(t, hServerOpts{}) // no processor: only the current group is searched
	g := hGroup(t, "grv.msgiddel", 2, nil)
	msgid := hMessageID(g, 2)
	c := hs.dial(t)

	grvExpect(t, c, 211, "GROUP %s", g)
	if line := grvExpect(t, c, 223, "STAT %s", msgid); line != "223 2 "+msgid+" Article exists" {
		t.Fatalf("STAT %s in the selected group: %q", msgid, line)
	}

	grvDeleteGroup(t, g)
	grvExpect(t, c, 430, "STAT %s", msgid)
	grvExpect(t, c, 430, "HEAD %s", msgid)
	grvExpect(t, c, 430, "OVER %s", msgid)
	grvExpect(t, c, 430, "HDR Subject %s", msgid)
	grvExpect(t, c, 430, "XHDR Subject %s", msgid)
	// the selection is gone, as if no group had been selected
	grvExpect(t, c, 412, "STAT 2")
	grvExpect(t, c, 411, "GROUP %s", g)
}

// TestGrvMsgIDCurrentGroupDeactivated: the same for a group an admin deactivated; after it
// is active again a fresh GROUP serves it as before.
func TestGrvMsgIDCurrentGroupDeactivated(t *testing.T) {
	db := hTestDB(t)
	hs := newTestServer(t, hServerOpts{})
	g := hGroup(t, "grv.msgiddeact", 2, nil)
	msgid := hMessageID(g, 1)
	c := hs.dial(t)

	grvExpect(t, c, 211, "GROUP %s", g)
	grvExpect(t, c, 223, "STAT %s", msgid)

	if err := db.UpdateNewsgroupActive(g, false); err != nil {
		t.Fatalf("UpdateNewsgroupActive(%s, false): %v", g, err)
	}
	grvExpect(t, c, 430, "STAT %s", msgid)
	grvExpect(t, c, 430, "XHDR Subject %s", msgid)
	grvExpect(t, c, 412, "XOVER 1-2")

	if err := db.UpdateNewsgroupActive(g, true); err != nil {
		t.Fatalf("UpdateNewsgroupActive(%s, true): %v", g, err)
	}
	grvExpect(t, c, 211, "GROUP %s", g)
	grvExpect(t, c, 223, "STAT %s", msgid)
}

// TestGrvHistoryGroupGoneNoFile: a history entry naming a group the server does not carry is
// a miss (430) and must not create that group's database file or a newsgroups row (SEC-2,
// as TestGrpListGroupUnknownNoFile for LISTGROUP).
func TestGrvHistoryGroupGoneNoFile(t *testing.T) {
	db := hTestDB(t)
	gone := hGroupName("grv.historygone") // never inserted into the main database
	msgid := hMessageID(gone, 1)
	proc := &hFakeProcessor{findFn: func(messageID, _ string) (*models.Article, error) {
		if messageID != msgid {
			return nil, fmt.Errorf("miss: %w", ErrArticleNotFound)
		}
		g := gone
		return &models.Article{MessageID: messageID, Subject: "from the history", NewsgroupsPtr: []*string{&g}}, nil
	}}
	hs := newTestServer(t, hServerOpts{Proc: proc})
	c := hs.dial(t)

	before := grvDBEntriesOf(t)
	for _, cmd := range []string{"STAT %s", "HEAD %s", "ARTICLE %s", "BODY %s", "OVER %s", "HDR Subject %s", "XHDR Subject %s"} {
		grvExpect(t, c, 430, cmd, msgid)
	}
	after := grvDBEntriesOf(t)

	if created := grvNewEntries(t, before, after, gone); len(created) > 0 {
		t.Errorf("the message-id lookups created %v for the unknown group %s", created, gone)
	}
	if _, err := db.MainDBGetNewsgroup(gone); err == nil {
		t.Errorf("the message-id lookups created a newsgroups row for %s", gone)
	}
	if n := proc.findCount(); n == 0 {
		t.Errorf("the history index was not consulted (%d FindArticleByMessageID calls)", n)
	}
}

// grvDBEntriesOf snapshots <DataDir>/db (same shape as grpDBEntries, kept separate so this
// file does not depend on the group test file).
func grvDBEntriesOf(t *testing.T) map[string]bool {
	t.Helper()
	root := filepath.Join(hDataDir(t), "db")
	out := make(map[string]bool)
	for _, pat := range []string{filepath.Join(root, "*"), filepath.Join(root, "*", "*")} {
		m, err := filepath.Glob(pat)
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		for _, p := range m {
			out[p] = true
		}
	}
	return out
}

// TestGrvHistoryGroupDeactivatedNotServed: a history entry naming a group that exists but is
// deactivated must not be read either, although its group database still holds the article:
// the message-id forms answer 430.
func TestGrvHistoryGroupDeactivatedNotServed(t *testing.T) {
	db := hTestDB(t)
	g := hGroup(t, "grv.historydeact", 1, nil)
	msgid := hMessageID(g, 1)
	proc := &hFakeProcessor{findFn: func(messageID, _ string) (*models.Article, error) {
		if messageID != msgid {
			return nil, fmt.Errorf("miss: %w", ErrArticleNotFound)
		}
		name := g
		return &models.Article{MessageID: messageID, NewsgroupsPtr: []*string{&name}}, nil
	}}
	hs := newTestServer(t, hServerOpts{Proc: proc})
	c := hs.dial(t)

	// no group selected: the article is served through the history index
	grvExpect(t, c, 223, "STAT %s", msgid)

	if err := db.UpdateNewsgroupActive(g, false); err != nil {
		t.Fatalf("UpdateNewsgroupActive(%s, false): %v", g, err)
	}
	grvExpect(t, c, 430, "STAT %s", msgid)
	grvExpect(t, c, 430, "HEAD %s", msgid)
	grvExpect(t, c, 430, "XHDR Subject %s", msgid)

	// deleting the row behaves the same
	if err := db.UpdateNewsgroupActive(g, true); err != nil {
		t.Fatalf("UpdateNewsgroupActive(%s, true): %v", g, err)
	}
	grvExpect(t, c, 223, "STAT %s", msgid)
	grvDeleteGroup(t, g)
	grvExpect(t, c, 430, "STAT %s", msgid)
}

// TestGrvGroupRelativeDeletedGroup: every group-relative command answers 412 once the
// selected group is gone from the main database, and none of them touches <DataDir>/db.
func TestGrvGroupRelativeDeletedGroup(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	g := hGroup(t, "grv.grouprel", 3, nil)
	c := hs.dial(t)

	grvExpect(t, c, 211, "GROUP %s", g)
	grvExpect(t, c, 223, "STAT 2")
	grvDeleteGroup(t, g)

	before := grvDBEntriesOf(t)
	for _, cmd := range []string{
		"ARTICLE 1", "HEAD 1", "BODY 1", "STAT 1", "ARTICLE", "STAT",
		"NEXT", "LAST", "XOVER 1-3", "OVER 1-3", "XHDR Subject 1-3", "HDR Subject 1-3", "LISTGROUP",
	} {
		grvExpect(t, c, 412, "%s", cmd)
	}
	after := grvDBEntriesOf(t)

	// the group's own database file was created when it was seeded; nothing new may appear
	if created := grvNewEntries(t, before, after, g); len(created) > 0 {
		t.Errorf("the group-relative commands created %v after %s was deleted", created, g)
	}
	grvExpect(t, c, 411, "GROUP %s", g)
}
