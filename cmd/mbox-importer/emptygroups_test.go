package main

// Plan importer-empty-groups: an article the processor's bulk checks refuse creates no group
// and no group database, and -drop-empty-groups deletes the empty groups earlier runs left.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// TestMboxCmdRejectedCreatesNoGroup: every article the processor refuses (control message,
// header block with a glued second header, unreadable date, NUL in the body, too many
// groups, empty From) is counted as rejected and leaves neither a newsgroups row nor a group
// database; the good articles still create their group (the first listed one only).
func TestMboxCmdRejectedCreatesNoGroup(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	const (
		good    = "t.empty.good"
		good2   = "t.empty.good2"
		other   = "t.empty.other"
		ctl     = "t.empty.ctl"
		glued   = "t.empty.glued"
		badDate = "t.empty.date"
		nul     = "t.empty.nul"
		many    = "t.empty.many"
		noFrom  = "t.empty.nofrom"
		idGood  = "<good@empty.test.invalid>"
		idGood2 = "<good2@empty.test.invalid>"
		idCtl   = "<ctl@empty.test.invalid>"
		idGlued = "<glued@empty.test.invalid>"
		idDate  = "<date@empty.test.invalid>"
		idNul   = "<nul@empty.test.invalid>"
		idMany  = "<many@empty.test.invalid>"
		idFrom  = "<nofrom@empty.test.invalid>"
	)
	const date = "Date: Tue, 24 Jun 2003 19:30:12 +0200"
	manyGroups := []string{many}
	for i := 0; i < 9; i++ {
		manyGroups = append(manyGroups, fmt.Sprintf("t.empty.many%d", i))
	}
	mbox := miArticle(idGood, good, "good") +
		miArticle(idGood2, good2+","+other, "good crosspost") +
		miArticle(idCtl, ctl, "cmsg cancel <x@y.invalid>", "Control: cancel <x@y.invalid>") +
		// a truncated article with the next message's header glued on: a second Subject and Date
		miArticle(idGlued, glued, "truncated", "Subject: next message", "Date: Wed, 25 Jun 2003 10:00:00 +0200") +
		strings.Replace(miArticle(idDate, badDate, "bad date"), date, "Date: garbage", 1) +
		strings.Replace(miArticle(idNul, nul, "nul"), "body of "+idNul, "body\x00of", 1) +
		miArticle(idMany, strings.Join(manyGroups, ","), "too many groups") +
		strings.Replace(miArticle(idFrom, noFrom, "empty from"), "From: Tester <tester@example.invalid>", "From:", 1)
	in := miWrite(t, t.TempDir(), "g.mbox", mbox)

	code, sum := miRun(t, "-data", data, in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 8, 2, 0, 6, 0, "-")
	absent := append([]string{other, ctl, glued, badDate, nul, noFrom}, manyGroups...)
	ids := []string{idGood, idGood2, idCtl, idGlued, idDate, idNul, idMany, idFrom}
	miCheckGroups(t, data, map[string][]string{good: {idGood}, good2: {idGood2}}, ids, absent...)
	for _, g := range absent {
		if dir, _ := groupDBFiles(data, g); miExists(dir) {
			t.Errorf("rejected article created the group database directory %s of %s", dir, g)
		}
	}
	// the refusals are still remembered (a peer offering the id later gets "not wanted")
	miCheckHistory(t, data, idGood, idGood2, idCtl, idGlued, idDate, idNul, idMany, idFrom)
}

// deMakeGroup inserts an active group with no articles; withFile also creates its (empty)
// group database.
func deMakeGroup(t *testing.T, db *database.Database, name string, withFile bool) {
	t.Helper()
	deMakeGroupActive(t, db, name, withFile, true)
}

// deMakeGroupActive is deMakeGroup with the given active value.
func deMakeGroupActive(t *testing.T, db *database.Database, name string, withFile, active bool) {
	t.Helper()
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: name, Description: "drop-empty-groups", Active: active, Status: "y", LowWater: 1}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	if withFile {
		g, err := db.GetGroupDB(name)
		if err != nil {
			t.Fatalf("GetGroupDB %s: %v", name, err)
		}
		g.Return()
	}
}

// TestMboxCmdDropEmptyGroups: -drop-empty-groups with -dry-run changes nothing; without it
// it deletes exactly the listed groups without articles (row, section membership, group
// database files and directory) and skips a group with articles, a name without a row and
// every group not in the list.
func TestMboxCmdDropEmptyGroups(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	const (
		full    = "t.drop.full"    // imported article: never deleted
		empty   = "t.drop.empty"   // row and empty group database: deleted
		noFile  = "t.drop.nofile"  // row, no group database: deleted
		keep    = "t.drop.keep"    // empty, not in the list: kept
		missing = "t.drop.missing" // in the list, no row
		idFull  = "<full@drop.test.invalid>"
	)
	in := miWrite(t, t.TempDir(), "g.mbox", miArticle(idFull, full, "full"))
	if code, _ := miRun(t, "-data", data, in); code != 0 {
		t.Fatalf("import exit %d", code)
	}
	var sectionID int
	miWithDB(t, data, func(db *database.Database) {
		deMakeGroup(t, db, empty, true)
		deMakeGroup(t, db, noFile, false)
		deMakeGroup(t, db, keep, true)
		section := &models.Section{Name: "dropsec", DisplayName: "drop", CreatedAt: time.Now()}
		if err := db.CreateSection(section); err != nil {
			t.Fatalf("CreateSection: %v", err)
		}
		sectionID = section.ID
		if err := db.CreateSectionGroup(&models.SectionGroup{SectionID: sectionID, NewsgroupName: empty, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("CreateSectionGroup: %v", err)
		}
	})
	emptyDir, emptyFile := groupDBFiles(data, empty)
	if !miExists(emptyFile) {
		t.Fatalf("setup: no group database %s", emptyFile)
	}
	list := miWrite(t, t.TempDir(), "drop.txt", strings.Join([]string{
		"# importer leftovers", empty, "", noFile, full, missing, empty,
	}, "\n")+"\n")

	// dry run: lists, changes nothing
	code, _ := miRun(t, "-data", data, "-dry-run", "-drop-empty-groups", list)
	if code != 0 {
		t.Fatalf("dry run exit %d", code)
	}
	res := lastDrop
	if res == nil || res.listed != 4 || strings.Join(res.deletable, ",") != empty+","+noFile || len(res.deleted) != 0 || len(res.skipped) != 2 {
		t.Fatalf("dry run result %+v", res)
	}
	// rows only: a GetGroupDB here would create the group database of noFile
	miWithDB(t, data, func(db *database.Database) {
		for _, g := range []string{full, empty, noFile, keep} {
			if _, err := db.MainDBGetNewsgroup(g); err != nil {
				t.Errorf("dry run: group %s: %v", g, err)
			}
		}
	})
	if !miExists(emptyFile) {
		t.Fatalf("dry run removed %s", emptyFile)
	}
	if _, f := groupDBFiles(data, noFile); miExists(f) {
		t.Fatalf("setup: %s has a group database", noFile)
	}

	// for real
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	code, _ = miRun(t, "-data", data, "-drop-empty-groups", list)
	log.SetOutput(os.Stderr)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, logs.String())
	}
	res = lastDrop
	if res == nil || strings.Join(res.deleted, ",") != empty+","+noFile || res.fileErrors != 0 {
		t.Fatalf("result %+v", res)
	}
	skipped := map[string]string{}
	for _, s := range res.skipped {
		skipped[s.group] = s.reason
	}
	if len(skipped) != 2 || skipped[missing] != "no newsgroups row" || skipped[full] == "" {
		t.Errorf("skipped %v, want %s (no row) and %s (articles)", skipped, missing, full)
	}
	for _, want := range []string{
		fmt.Sprintf("[MBOX-IMPORT] drop-empty-groups: deleted group=%q", empty),
		fmt.Sprintf("[MBOX-IMPORT] drop-empty-groups: skipped group=%q: no newsgroups row", missing),
		"[MBOX-IMPORT] drop-empty-groups done: listed=4 empty=2 deleted=2 file-only=0 files-removed=0 skipped=2 check-errors=0 file-errors=0 dry-run=false",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q\n%s", want, logs.String())
		}
	}
	miCheckGroups(t, data, map[string][]string{full: {idFull}, keep: nil}, []string{idFull}, empty, noFile)
	miWithDB(t, data, func(db *database.Database) {
		if in, err := db.SectionGroupExists(sectionID, empty); err != nil || in {
			t.Errorf("section membership of %s: %t, %v; want gone", empty, in, err)
		}
	})
	for _, p := range []string{emptyFile, emptyFile + "-wal", emptyFile + "-shm", emptyDir} {
		if miExists(p) {
			t.Errorf("%s still there", p)
		}
	}
	if _, keepFile := groupDBFiles(data, keep); !miExists(keepFile) {
		t.Errorf("group database of %s (not listed) removed", keep)
	}
}

// TestMboxCmdDropEmptyGroupsRefused: bad command lines exit 2 (also without an explicit
// -data); a -data without a main database exits 1 and creates nothing; an unfinished run of
// the tracking database refuses the cleanup: one alive on this host always, one of another
// host unless -force-stale-runs; a dry run only logs it.
func TestMboxCmdDropEmptyGroupsRefused(t *testing.T) {
	list := miWrite(t, t.TempDir(), "drop.txt", "t.refused.a\n")
	for _, args := range [][]string{
		{"-drop-empty-groups", list, "x.mbox"},
		{"-drop-empty-groups", list, "-status"},
		{"-drop-empty-groups", list, "-from-iafetch", "ia.sqlite"},
		{"-drop-empty-groups", list},             // no explicit -data
		{"-drop-empty-groups", list, "-dry-run"}, // no explicit -data
		{"-force-stale-runs", "x.mbox"},          // only with -drop-empty-groups
	} {
		if code, _ := miRun(t, args...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}

	nodata := filepath.Join(t.TempDir(), "nodata")
	if code, _ := miRun(t, "-data", nodata, "-drop-empty-groups", list); code != 1 {
		t.Errorf("no main database: exit %d, want 1", code)
	}
	if miExists(nodata) {
		t.Errorf("-drop-empty-groups created %s", nodata)
	}

	miNeedDB(t)
	miSetup(t)
	data := filepath.Join(t.TempDir(), "data")
	in := miWrite(t, t.TempDir(), "g.mbox", miArticle(miID1, miAlpha, "a"))
	if code, _ := miRun(t, "-data", data, in); code != 0 {
		t.Fatalf("import exit %d", code)
	}
	miWithDB(t, data, func(db *database.Database) { deMakeGroup(t, db, "t.refused.a", false) })
	raw := mtRaw(t, filepath.Join(data, "cfg"))
	insertRun := func(host string, pid int, bootID string, pstart int64) int64 {
		t.Helper()
		res, err := database.RetryableExec(raw, `INSERT INTO runs (started, host, pid, boot_id, pstart) VALUES (?, ?, ?, ?, ?)`,
			trackNow(), host, pid, bootID, pstart)
		if err != nil {
			t.Fatalf("insert run: %v", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("insert run: %v", err)
		}
		return id
	}

	// a run row of this very process, not finished: a live import, refused even with -force-stale-runs
	self := trackSelf()
	liveID := insertRun(self.host, self.pid, self.bootID, self.pstart)
	for _, args := range [][]string{
		{"-data", data, "-drop-empty-groups", list},
		{"-data", data, "-force-stale-runs", "-drop-empty-groups", list},
	} {
		if code, _ := miRun(t, args...); code != 1 {
			t.Errorf("live import run %q: exit %d, want 1", args, code)
		}
	}
	// a dry run goes on (it changes nothing) and lists
	if code, _ := miRun(t, "-data", data, "-dry-run", "-drop-empty-groups", list); code != 0 || lastDrop == nil || len(lastDrop.deletable) != 1 {
		t.Errorf("dry run next to a live run: exit %d, result %+v; want 0 and the group listed", code, lastDrop)
	}
	miCheckGroups(t, data, map[string][]string{"t.refused.a": nil}, nil)

	// the live run ends; an unfinished run of another host is refused unless -force-stale-runs
	if _, err := database.RetryableExec(raw, `UPDATE runs SET finished = ? WHERE id = ?`, trackNow(), liveID); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	insertRun("other-host.invalid", 4242, "", 0)
	if code, _ := miRun(t, "-data", data, "-drop-empty-groups", list); code != 1 {
		t.Errorf("stale run of another host: exit %d, want 1", code)
	}
	miCheckGroups(t, data, map[string][]string{"t.refused.a": nil}, nil)
	if code, _ := miRun(t, "-data", data, "-force-stale-runs", "-drop-empty-groups", list); code != 0 {
		t.Errorf("-force-stale-runs: exit %d, want 0", code)
	}
	miCheckGroups(t, data, nil, nil, "t.refused.a")
}

// TestMboxCmdDropEmptyGroupsChunkFailure: with several chunks, a chunk whose delete fails (a
// test trigger refuses the delete of one of its rows) keeps its groups with their earlier
// active values and their files, while the chunks before and after it are deleted with their
// files, and the summary says so. A re-run removes the files of a listed name whose row is
// gone (file-only cleanup) but keeps a group database that holds articles.
func TestMboxCmdDropEmptyGroupsChunkFailure(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	oldChunk := dropChunk
	dropChunk = 2
	t.Cleanup(func() { dropChunk = oldChunk })
	data := filepath.Join(t.TempDir(), "data")
	const (
		c1a, c1b    = "t.chunk.c1a", "t.chunk.c1b"
		c2a, c2b    = "t.chunk.c2a", "t.chunk.c2b" // c2a: delete refused; c2b: inactive before
		c3a         = "t.chunk.c3a"
		orphan      = "t.chunk.orphan"     // row deleted, empty group database left
		orphanFull  = "t.chunk.orphanfull" // row deleted, group database with an article left
		idOrphanArt = "<orphanfull@chunk.test.invalid>"
	)
	in := miWrite(t, t.TempDir(), "g.mbox", miArticle(idOrphanArt, orphanFull, "full"))
	if code, _ := miRun(t, "-data", data, in); code != 0 {
		t.Fatalf("import exit %d", code)
	}
	miWithDB(t, data, func(db *database.Database) {
		for _, g := range []string{c1a, c1b, c2a, c3a, orphan} {
			deMakeGroup(t, db, g, true)
		}
		deMakeGroupActive(t, db, c2b, true, false)
		// the crash between row delete and file removal: rows gone, files left
		for _, g := range []string{orphan, orphanFull} {
			if err := db.UpdateNewsgroupActive(g, false); err != nil {
				t.Fatalf("deactivate %s: %v", g, err)
			}
			if ok, err := db.DeleteNewsgroup(g); err != nil || !ok {
				t.Fatalf("DeleteNewsgroup %s: %t, %v", g, ok, err)
			}
		}
		if _, err := db.GetMainDB().Exec(`CREATE TRIGGER drop_test_block BEFORE DELETE ON newsgroups
			WHEN old.name = '` + c2a + `' BEGIN SELECT RAISE(ABORT, 'drop test: delete refused'); END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
	})
	file := func(g string) string { _, f := groupDBFiles(data, g); return f }
	list := miWrite(t, t.TempDir(), "drop.txt", strings.Join([]string{c1a, c1b, c2a, c2b, c3a}, "\n")+"\n")

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	code, _ := miRun(t, "-data", data, "-drop-empty-groups", list)
	log.SetOutput(os.Stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (a chunk failed)\n%s", code, logs.String())
	}
	res := lastDrop
	if res == nil || strings.Join(res.deleted, ",") != strings.Join([]string{c1a, c1b, c3a}, ",") || res.fileErrors != 0 {
		t.Fatalf("result %+v, want %s,%s,%s deleted", res, c1a, c1b, c3a)
	}
	if want := "deleted=3 file-only=0 files-removed=0 skipped=2"; !strings.Contains(logs.String(), want) {
		t.Errorf("summary lacks %q\n%s", want, logs.String())
	}
	for _, g := range []string{c1a, c1b, c3a} {
		if miExists(file(g)) {
			t.Errorf("files of deleted %s still there", g)
		}
	}
	for _, g := range []string{c2a, c2b} {
		if !miExists(file(g)) {
			t.Errorf("files of %s (failed chunk) removed", g)
		}
	}
	miWithDB(t, data, func(db *database.Database) {
		for g, want := range map[string]bool{c2a: true, c2b: false} {
			ng, err := db.MainDBGetNewsgroup(g)
			if err != nil {
				t.Errorf("group %s of the failed chunk: %v", g, err)
				continue
			}
			if ng.Active != want {
				t.Errorf("group %s active=%t, want %t (its value before the cleanup)", g, ng.Active, want)
			}
		}
		for _, g := range []string{c1a, c1b, c3a} {
			if _, err := db.MainDBGetNewsgroup(g); err == nil {
				t.Errorf("group %s still there", g)
			}
		}
	})

	// re-run on the leftovers of an interrupted cleanup
	list2 := miWrite(t, t.TempDir(), "drop2.txt", orphan+"\n"+orphanFull+"\n"+c1a+"\n")
	logs.Reset()
	log.SetOutput(&logs)
	code, _ = miRun(t, "-data", data, "-drop-empty-groups", list2)
	log.SetOutput(os.Stderr)
	if code != 0 {
		t.Fatalf("re-run exit %d\n%s", code, logs.String())
	}
	res = lastDrop
	if res == nil || strings.Join(res.filesGone, ",") != orphan || len(res.deleted) != 0 {
		t.Fatalf("re-run result %+v, want file-only cleanup of %s", res, orphan)
	}
	skipped := map[string]string{}
	for _, sk := range res.skipped {
		skipped[sk.group] = sk.reason
	}
	if skipped[orphanFull] != "no newsgroups row, group database has articles (kept)" || skipped[c1a] != "no newsgroups row" {
		t.Errorf("re-run skipped %v", skipped)
	}
	if want := fmt.Sprintf("[MBOX-IMPORT] drop-empty-groups: removed the files of group=%q (no newsgroups row, file-only cleanup)", orphan); !strings.Contains(logs.String(), want) {
		t.Errorf("log lacks %q\n%s", want, logs.String())
	}
	if dir, f := groupDBFiles(data, orphan); miExists(f) || miExists(dir) {
		t.Errorf("files of %s still there", orphan)
	}
	if !miExists(file(orphanFull)) {
		t.Errorf("group database of %s (with an article) removed", orphanFull)
	}
}
