package main

// Tests of -foreign (plan importer-skip-foreign): an article in an input named after a group
// (or a hierarchy) that lists neither that group nor a group below it is skipped as foreign
// by default, instead of being filed into the first group of its Newsgroups header (one
// article batch, one commit, in a group the input is not about: the Giganews dump
// alt.global-warming.20141010.mbox.gz holds de.rec.fotografie articles). These tests use no
// identifier the base (df3263b) lacks, so the file runs there as well and fails.

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fgCross   = "t.mbox.cross"   // the other group of the crosspost, listed first
	fgForeign = "t.mbox.foreign" // the only group of the foreign articles
)

var (
	fgOwn  = []string{"<own1@foreign.test.invalid>", "<own2@foreign.test.invalid>", "<own3@foreign.test.invalid>"}
	fgXp   = "<cross@foreign.test.invalid>"
	fgFor  = []string{"<for1@foreign.test.invalid>", "<for2@foreign.test.invalid>"}
	fgIDs  = append(append(append([]string{}, fgOwn...), fgXp), fgFor...)
	fgMine = append(append([]string{}, fgOwn...), fgXp)
)

// fgMbox is 3 own articles of miAlpha, 1 crosspost "t.mbox.cross,t.mbox.alpha" and 2
// foreign articles of t.mbox.foreign only.
func fgMbox() string {
	var b strings.Builder
	b.WriteString(miArticle(fgOwn[0], miAlpha, "own one"))
	b.WriteString(miArticle(fgFor[0], fgForeign, "foreign one"))
	b.WriteString(miArticle(fgOwn[1], miAlpha, "own two"))
	b.WriteString(miArticle(fgXp, fgCross+","+miAlpha, "crosspost"))
	b.WriteString(miArticle(fgFor[1], fgForeign, "foreign two"))
	b.WriteString(miArticle(fgOwn[2], miAlpha, "own three"))
	return b.String()
}

// fgWriteGz writes content gzipped to dir/name and returns the path.
func fgWriteGz(t *testing.T, dir, name, content string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return miWrite(t, dir, name, buf.String())
}

// fgArchiveName is the name archive.org's Giganews dumps use: group, dump date, .mbox.gz.
var fgArchiveName = miAlpha + ".20141010.mbox.gz"

// fgCapture runs the tool with args and returns the exit code, the summary and the log.
func fgCapture(t *testing.T, args ...string) (int, *runSummary, string) {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	code, sum := miRun(t, args...)
	log.SetOutput(os.Stderr) // the buffer is read only after the logger stopped writing to it
	return code, sum, buf.String()
}

// TestForeignMboxSkip: the default skips the two foreign articles (reason foreign, in the
// counters, the per-input and the totals line); the own articles and the crosspost that
// lists the input's group go into it; neither the foreign group nor the crosspost's first
// group is created. On the base the foreign articles made the group t.mbox.foreign.
func TestForeignMboxSkip(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	in := fgWriteGz(t, t.TempDir(), fgArchiveName, fgMbox())
	data := filepath.Join(t.TempDir(), "data")

	code, sum, out := fgCapture(t, "-data", data, in)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	miCheck(t, sum, 6, 4, 0, 0, 0, "foreign=2")
	miCheckGroups(t, data, map[string][]string{miAlpha: fgMine}, fgIDs, fgForeign, fgCross)
	for _, want := range []string{
		fmt.Sprintf("[MBOX-IMPORT] file=%q read=6 stored=4 present=0 skipped=2 (foreign=2) ", in),
		"[MBOX-IMPORT] done inputs=1 marked-done=1 input-errors=0 read=6 stored=4 present=0 skipped=2 (foreign=2) ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "skipped as foreign: none lists") {
		t.Errorf("warning for an input with stored articles\n%s", out)
	}
	// a mixed input (own and foreign articles) is done as before
	if !miExists(in + ".done") {
		t.Errorf("mixed input not renamed .done")
	}
	if status, reason := mtStatusOf(t, mtRaw(t, filepath.Join(data, "cfg")), in); status != "done" {
		t.Errorf("tracking row %q / %q, want done", status, reason)
	}
}

// TestForeignMboxFile: -foreign file restores the old rule: the foreign articles go to their
// first group, nothing is skipped.
func TestForeignMboxFile(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	in := fgWriteGz(t, t.TempDir(), fgArchiveName, fgMbox())
	data := filepath.Join(t.TempDir(), "data")

	code, sum := miRun(t, "-data", data, "-foreign", "file", in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 6, 6, 0, 0, 0, "-")
	miCheckGroups(t, data, map[string][]string{miAlpha: fgMine, fgForeign: fgFor}, fgIDs, fgCross)
}

// TestForeignMboxHierarchy: a file named after a hierarchy files an article into the first
// listed group below it (also when another group comes first) and skips one that lists none.
func TestForeignMboxHierarchy(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	const hier, other = "t.hier", "t.other"
	const ga, gb = hier + ".a", hier + ".b"
	idA, idB, idF := "<a@hier.test.invalid>", "<b@hier.test.invalid>", "<f@hier.test.invalid>"
	in := miWrite(t, t.TempDir(), hier+".mbox",
		miArticle(idA, ga, "a")+miArticle(idB, other+","+gb, "b")+miArticle(idF, other, "foreign"))
	data := filepath.Join(t.TempDir(), "data")

	code, sum := miRun(t, "-data", data, in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 3, 2, 0, 0, 0, "foreign=1")
	miCheckGroups(t, data, map[string][]string{ga: {idA}, gb: {idB}}, []string{idA, idB, idF}, other, hier)
}

// TestForeignMboxForcedGroup: -group files every article into its group, foreign or not.
func TestForeignMboxForcedGroup(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	const forced = "t.mbox.forced"
	in := fgWriteGz(t, t.TempDir(), fgArchiveName, fgMbox())
	data := filepath.Join(t.TempDir(), "data")

	code, sum := miRun(t, "-data", data, "-group", forced, in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 6, 6, 0, 0, 0, "-")
	miCheckGroups(t, data, map[string][]string{forced: fgIDs}, fgIDs, miAlpha, fgForeign, fgCross)
}

// TestForeignMboxNoGroupName: an input whose name gives no group (one word: export.mbox) is
// unchanged: every article goes to the first group of its Newsgroups header.
func TestForeignMboxNoGroupName(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	in := miWrite(t, t.TempDir(), "export.mbox", fgMbox())
	data := filepath.Join(t.TempDir(), "data")

	code, sum := miRun(t, "-data", data, in)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	miCheck(t, sum, 6, 6, 0, 0, 0, "-")
	miCheckGroups(t, data, map[string][]string{miAlpha: fgOwn, fgCross: {fgXp}, fgForeign: fgFor}, fgIDs)
}

// TestForeignDryRun: the dry run counts foreign skips the same way and lists no target group
// for them; an input whose articles are all foreign gets a warning and is an input error
// (exit 1), as in a real run. No database is opened.
func TestForeignDryRun(t *testing.T) {
	dir := t.TempDir()
	in := fgWriteGz(t, dir, fgArchiveName, fgMbox())
	allForeign := miWrite(t, dir, miBeta+".mbox", miArticle(fgFor[0], fgForeign, "f1")+miArticle(fgFor[1], fgForeign, "f2"))
	data := filepath.Join(t.TempDir(), "data")

	code, sum, out := fgCapture(t, "-data", data, "-dry-run", in, allForeign)
	if code != 1 || sum == nil || sum.InputErrors != 1 {
		t.Fatalf("exit %d summary %+v, want exit 1 with 1 input error\n%s", code, sum, out)
	}
	miCheck(t, sum, 8, 4, 0, 0, 0, "foreign=4")
	if miExists(data) {
		t.Errorf("dry run created %s", data)
	}
	for _, want := range []string{
		"[MBOX-IMPORT] dry-run: 1 target groups",
		fmt.Sprintf("[MBOX-IMPORT] dry-run group=%q articles=4", miAlpha),
		fmt.Sprintf("[MBOX-IMPORT] file=%q read=6 stored=4 present=0 skipped=2 (foreign=2) ", in),
		fmt.Sprintf("[MBOX-IMPORT] file=%q read=2 stored=0 present=0 skipped=2 (foreign=2) ", allForeign),
		fmt.Sprintf("[MBOX-IMPORT] WARNING file=%q: all 2 importable articles skipped as foreign: none lists %q", allForeign, miBeta),
		"[MBOX-IMPORT] done inputs=2 marked-done=0 input-errors=1 read=8 stored=4 present=0 skipped=4 (foreign=4) ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n%s", want, out)
		}
	}
	if n := strings.Count(out, "skipped as foreign: none lists"); n != 1 {
		t.Errorf("%d warnings, want 1 (only the all-foreign input)\n%s", n, out)
	}
}

// TestForeignFlagValue: -foreign takes skip or file only.
func TestForeignFlagValue(t *testing.T) {
	in := miWrite(t, t.TempDir(), "x.mbox", miArticle(miID1, miAlpha, "a"))
	if code, _ := miRun(t, "-dry-run", "-foreign", "drop", in); code != 2 {
		t.Errorf("-foreign drop: exit %d, want 2", code)
	}
	for _, v := range []string{"skip", "file"} {
		if code, _ := miRun(t, "-dry-run", "-foreign", v, in); code != 0 {
			t.Errorf("-foreign %s: exit %d, want 0", v, code)
		}
	}
}

// TestForeignUAT: a UAT package's group counts like the file name's: a message that does
// not list it is skipped as foreign.
func TestForeignUAT(t *testing.T) {
	in := uaWrite(t, t.TempDir(), uaGroup, []string{
		uaMessage(uaIDPolish, uaGroup, uaPolishSub, "own\n"),
		uaMessage(uaIDCross, uaOther, "elsewhere", "foreign\n"),
	})
	res := importInputs([]string{in}, &runOptions{zstd: "zstd"}, newCountingSink(), make(chan struct{}))
	if res.Total.Read != 2 || res.Total.Stored != 1 || res.Total.skippedDetail() != "foreign=1" {
		t.Errorf("read=%d stored=%d skipped=%s; want 2, 1, foreign=1", res.Total.Read, res.Total.Stored, res.Total.skippedDetail())
	}
}

// TestForeignAllForeignFails: a real run of an input whose name is dotted but no group of its
// articles (an archive suffix the name rule does not know) skips every article as foreign;
// such an input is an input error (exit 1), is not renamed .done, and its tracking row is
// failed with a reason that names the group, so the next run takes it again (with -foreign
// file). An input next to it with stored articles is done as usual. On 17a9eb0 the input was
// marked done with only a warning.
func TestForeignAllForeignFails(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	dir := t.TempDir()
	bad := miWrite(t, dir, miAlpha+".2014.mbox", miArticle(fgOwn[0], miAlpha, "own 1")+miArticle(fgOwn[1], miAlpha, "own 2"))
	good := fgWriteGz(t, dir, fgArchiveName, fgMbox())
	data := filepath.Join(t.TempDir(), "data")

	code, sum, out := fgCapture(t, "-data", data, bad, good)
	if code != 1 || sum == nil || sum.InputErrors != 1 || sum.MarkedDone != 1 {
		t.Fatalf("exit %d summary %+v, want exit 1, 1 input error, 1 marked done\n%s", code, sum, out)
	}
	if !miExists(bad) || miExists(bad+".done") {
		t.Errorf("all-foreign input renamed: %t %t", miExists(bad), miExists(bad+".done"))
	}
	if !miExists(good + ".done") {
		t.Errorf("mixed input not renamed .done")
	}
	if want := fmt.Sprintf("[MBOX-IMPORT] WARNING file=%q: all 2 importable articles skipped as foreign: none lists %q",
		bad, miAlpha+".2014"); !strings.Contains(out, want) {
		t.Errorf("log lacks %q\n%s", want, out)
	}
	track := mtRaw(t, filepath.Join(data, "cfg"))
	status, reason := mtStatusOf(t, track, bad)
	if status != "failed" || !strings.Contains(reason, "all articles foreign") || !strings.Contains(reason, miAlpha+".2014") {
		t.Errorf("tracking row of the all-foreign input %q / %q, want failed / all articles foreign ... %s", status, reason, miAlpha+".2014")
	}
	if status, reason := mtStatusOf(t, track, good); status != "done" {
		t.Errorf("tracking row of the mixed input %q / %q, want done", status, reason)
	}

	// the failed input is taken again on the next run; -foreign file imports it
	code, sum = miRun(t, "-data", data, "-foreign", "file", bad)
	if code != 0 || sum == nil || sum.MarkedDone != 1 {
		t.Fatalf("rerun with -foreign file: exit %d summary %+v", code, sum)
	}
	miCheck(t, sum, 2, 0, 2, 0, 0, "-") // its articles came with the mixed input already
	if status, reason := mtStatusOf(t, track, bad); status != "done" {
		t.Errorf("tracking row after the rerun %q / %q, want done", status, reason)
	}
}

// TestForeignSpoolInnerMbox: an mbox file inside a -spool directory, named after a group none
// of its articles lists, is all foreign: the walker warns with the spool and the file, the
// other articles of the spool are stored, and the spool input fails (input error, tracking
// row failed) so it is redone with -foreign file.
func TestForeignSpoolInnerMbox(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	tree := t.TempDir()
	const inner = "misc/t.mbox.backup.mbox"
	for name, content := range map[string]string{
		"t/mbox/alpha/1": miArticle(fgOwn[0], miAlpha, "own"),
		inner:            miArticle(fgFor[0], fgForeign, "f1") + miArticle(fgFor[1], fgForeign, "f2"),
	} {
		p := filepath.Join(tree, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data := filepath.Join(t.TempDir(), "data")

	code, sum, out := fgCapture(t, "-data", data, "-spool", tree)
	if code != 1 || sum == nil || sum.InputErrors != 1 {
		t.Fatalf("exit %d summary %+v, want exit 1 with 1 input error\n%s", code, sum, out)
	}
	miCheck(t, sum, 3, 1, 0, 0, 0, "foreign=2")
	if want := fmt.Sprintf("[MBOX-IMPORT] WARNING spool file=%q mbox=%q: all 2 importable articles skipped as foreign: none lists %q",
		tree, inner, "t.mbox.backup"); !strings.Contains(out, want) {
		t.Errorf("log lacks %q\n%s", want, out)
	}
	miCheckGroups(t, data, map[string][]string{miAlpha: {fgOwn[0]}}, []string{fgOwn[0], fgFor[0], fgFor[1]}, fgForeign)
	if status, reason := mtStatusOf(t, mtRaw(t, filepath.Join(data, "cfg")), tree); status != "failed" || !strings.Contains(reason, "all articles foreign") {
		t.Errorf("tracking row %q / %q, want failed / all articles foreign", status, reason)
	}
}

// TestForeignOtherOutcomesNotAllForeign: only articles that reached the group choice count
// for the all-foreign failure. A group file whose own articles are all yEnc (skipped as
// binary before the group choice) plus one stray foreign article, and one whose own article
// is rejected (a control message) plus a foreign one, are no input errors: both are done.
// On 99d99ef both failed as "all foreign".
func TestForeignOtherOutcomesNotAllForeign(t *testing.T) {
	miNeedDB(t)
	miSetup(t)
	dir := t.TempDir()
	yenc := func(id string) string {
		return strings.Replace(miArticle(id, miAlpha, "binary"), "\nbody of "+id+"\n",
			"\n=ybegin line=128 size=3 name=a.bin\nxyz\n=yend size=3\n", 1)
	}
	binIn := miWrite(t, dir, miAlpha+".mbox", yenc(fgOwn[0])+yenc(fgOwn[1])+miArticle(fgFor[0], fgForeign, "stray"))
	rejIn := miWrite(t, dir, miBeta+".mbox",
		miArticle(fgOwn[2], miBeta, "control", "Control: cancel <x@foreign.test.invalid>")+miArticle(fgFor[1], fgForeign, "stray"))
	data := filepath.Join(t.TempDir(), "data")

	code, sum, out := fgCapture(t, "-data", data, binIn, rejIn)
	if code != 0 || sum == nil || sum.InputErrors != 0 || sum.MarkedDone != 2 {
		t.Fatalf("exit %d summary %+v, want exit 0, no input error, 2 marked done\n%s", code, sum, out)
	}
	miCheck(t, sum, 5, 0, 0, 1, 0, "binary=2,foreign=2")
	if strings.Contains(out, "skipped as foreign: none lists") {
		t.Errorf("all-foreign warning\n%s", out)
	}
	track := mtRaw(t, filepath.Join(data, "cfg"))
	for _, in := range []string{binIn, rejIn} {
		if status, reason := mtStatusOf(t, track, in); status != "done" {
			t.Errorf("tracking row of %s %q / %q, want done", filepath.Base(in), status, reason)
		}
	}
}

// TestForeignSpoolNamedNonMbox: in a spool only a file read as an mbox gets a group from its
// name. A one-article file named misc/news.backup.mbox and an A-news batch named
// misc/old.news.bag are filed by their Newsgroups (rule 4), not skipped as foreign. On
// 99d99ef both were skipped as foreign.
func TestForeignSpoolNamedNonMbox(t *testing.T) {
	tree := t.TempDir()
	spoolWrite(t, tree, "misc/news.backup.mbox", spoolArticleText("<single@foreign.test.invalid>", miAlpha, "single"))
	spoolWrite(t, tree, "misc/old.news.bag", spoolANewsText)

	code, sum, out := fgCapture(t, "-dry-run", "-spool", tree)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	miCheck(t, sum, 2, 2, 0, 0, 0, "-")
	for _, want := range []string{
		fmt.Sprintf("[MBOX-IMPORT] dry-run group=%q articles=1", miAlpha),
		`[MBOX-IMPORT] dry-run group="net.games" articles=1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n%s", want, out)
		}
	}
}

// TestForeignSkipsNames pins which file names make foreign skips (foreignSkips on the group
// groupFromFileName gives): every dotted name counts, also one that is no group (an archive
// suffix the name rule does not know); such an input then fails (TestForeignAllForeignFails).
func TestForeignSkipsNames(t *testing.T) {
	for _, c := range []struct {
		name, group string
		skips       bool
	}{
		{"alt.test.mbox.gz", "alt.test", true},
		{"alt.test.20141010.mbox.gz", "alt.test", true},
		{"alt.test.(9).mbox.7z", "alt.test", true},
		{"free.pt.mbox.7z", "free.pt", true},
		{"alt.test.2014.mbox.gz", "alt.test.2014", true},
		{"alt.test.part2.mbox", "alt.test.part2", true},
		{"news.backup.mbox", "news.backup", true},
		{"export.mbox", "export", false},
		{"g.mbox", "g", false},
	} {
		g, err := groupFromFileName(c.name)
		if err != nil || g != c.group {
			t.Errorf("groupFromFileName(%q) = %q, %v; want %q", c.name, g, err, c.group)
			continue
		}
		if got := foreignSkips(&runOptions{sourceGroup: g}); got != c.skips {
			t.Errorf("foreignSkips(%q) = %t, want %t", g, got, c.skips)
		}
		if foreignSkips(&runOptions{sourceGroup: g, foreignFile: true}) {
			t.Errorf("foreignSkips(%q) with -foreign file = true", g)
		}
	}
}
