package main

// Tests of the .tar.gz news spools (plan importer-targz-spool): numeric order per directory
// run, the gmane hierarchy prefix from the file name, textusenet naming, members that are
// passed over, the spill file, truncated and corrupt archives, the stop channel, the
// -from-iafetch suffix rule and the dry run. The archives are built in the tests.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// tgzEntry is one member of a test archive; typ 0 is a regular file.
type tgzEntry struct {
	name, body string
	typ        byte
	link       string
}

// tgzTar builds the uncompressed tar of entries, owner news/news as on arc1-ca.
func tgzTar(t *testing.T, entries []tgzEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Uname: "news", Gname: "news",
			Linkname: e.link, ModTime: time.Unix(1_000_000_000, 0)}
		if e.typ == 0 {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %q: %v", e.name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("tar body %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tgzGzip compresses data with gzip at level (gzip.NoCompression keeps the bytes visible).
func tgzGzip(t *testing.T, data []byte, level int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, level)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gz.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tgzBuild builds a .tar.gz of entries.
func tgzBuild(t *testing.T, entries []tgzEntry) []byte {
	t.Helper()
	return tgzGzip(t, tgzTar(t, entries), gzip.DefaultCompression)
}

// tgzArticle is one article file of a textusenet or gmane spool: full headers, no mbox
// separator line.
func tgzArticle(msgid, newsgroups string) string {
	return strings.SplitN(miArticle(msgid, newsgroups, "subject of "+msgid), "\n", 2)[1]
}

// tgzDB is the 64 KiB binary .db of a group directory.
var tgzDB = strings.Repeat("\x00\x13\xfe\x02", 16384)

// tgzSink records "msgid group" in the order the articles arrive. onHandle runs after an
// article was taken (a test closes the stop channel there).
type tgzSink struct {
	mu       sync.Mutex
	got      []string
	onHandle func()
}

func (s *tgzSink) Handle(msgid string, art func() (*models.Article, error), group string) (bool, bool, error) {
	if _, err := art(); err != nil {
		return false, false, err
	}
	s.mu.Lock()
	s.got = append(s.got, msgid+" "+group)
	s.mu.Unlock()
	if s.onHandle != nil {
		s.onHandle()
	}
	return true, false, nil
}

func (s *tgzSink) beginInput() inputSink { return s }

func (s *tgzSink) endInput(time.Duration) error { return nil }

func (s *tgzSink) list() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.got, "\n")
}

// tgzID is the message-id of article n of a test archive.
func tgzID(dir string, n int) string {
	return "<" + strings.ReplaceAll(dir, "/", "-") + "-" + strconv.Itoa(n) + "@targz.test.invalid>"
}

// tgzGamesEntries is a gmane.games.tar.gz in arc1-ca's layout: directory entries, the
// members of advanced-squad-leader in archive (not numeric) order with the .db among them,
// a symbolic and a hard link (passed over), and a crossposted article in games/other that
// lists another gmane group first.
func tgzGamesEntries() []tgzEntry {
	asl := "games/advanced-squad-leader"
	g := "gmane.games.advanced-squad-leader"
	return []tgzEntry{
		{name: "games/", typ: tar.TypeDir},
		{name: asl + "/", typ: tar.TypeDir},
		{name: asl + "/.art10", body: tgzArticle(tgzID(asl, 10), g)},
		{name: asl + "/.art1", body: tgzArticle(tgzID(asl, 1), g)},
		{name: asl + "/.db", body: tgzDB},
		{name: asl + "/.art9", body: tgzArticle(tgzID(asl, 9), g)},
		{name: asl + "/.art99", typ: tar.TypeSymlink, link: ".art1"},
		{name: asl + "/.art98", typ: tar.TypeLink, link: asl + "/.art1"},
		{name: asl + "/.art2", body: tgzArticle(tgzID(asl, 2), g)},
		{name: "games/other/", typ: tar.TypeDir},
		{name: "./games/other/.art3", body: tgzArticle(tgzID("games/other", 3), "gmane.misc.first,gmane.games.other")},
	}
}

// tgzGamesWant is what reading tgzGamesEntries hands over, in order.
func tgzGamesWant() string {
	asl := "games/advanced-squad-leader"
	g := "gmane.games.advanced-squad-leader"
	return strings.Join([]string{
		tgzID(asl, 1) + " " + g, tgzID(asl, 2) + " " + g, tgzID(asl, 9) + " " + g, tgzID(asl, 10) + " " + g,
		tgzID("games/other", 3) + " gmane.games.other",
	}, "\n")
}

// tgzLimits are the run buffer limits a test reads with: the production ones, and ones so
// small that every member goes through the spill file.
var tgzLimits = []struct {
	name                string
	memLimit, memMember int64
	spills              bool
}{
	{"in memory", tarRunMemory, tarMemberMemory, false},
	{"spill file", 600, 300, true},
}

// tgzRead reads data (a .tar.gz) as the input label with the given run limits. The spill
// file is closed before it returns; run is returned for its counters.
func tgzRead(t *testing.T, label string, data []byte, opts *runOptions, sink inputSink, stop <-chan struct{},
	memLimit, memMember int64) (*counters, *tarRun, error) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	run := newTarRun(label, opts.tmpDir, memLimit, memMember, opts.maxArticleSize)
	c, rerr := readTarRuns(gz, run, label, opts, sink, stop)
	run.close()
	if run.spill != nil {
		t.Error("the spill file is still open after close")
	}
	return c, run, rerr
}

// tgzDirEmpty fails the test when dir holds anything.
func tgzDirEmpty(t *testing.T, what, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%s: left in the temp dir: %v", what, names)
	}
}

// TestMboxTarGzImport is the end-to-end test through the import loop, written against
// what the base had already (importInputs, iafetchSupported, spoolNameLess): a gmane-style
// .tar.gz positional input is read as a spool, numerically ordered per directory, filed
// into the gmane groups and renamed .done. On the base the input was opened as a gzip mbox
// and failed ("no mbox separator").
func TestMboxTarGzImport(t *testing.T) {
	if !spoolNameLess(".art9", ".art10") || spoolNameLess(".art10", ".art9") {
		t.Error("spoolNameLess: .art9 must come before .art10")
	}
	if ok, reason := iafetchSupported("gmane.games.tar.gz.BACKUP.2021-03/gmane.games.tar.gz"); !ok {
		t.Errorf("iafetchSupported(gmane.games.tar.gz) = false (%s), want true", reason)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "gmane.games.tar.gz")
	if err := os.WriteFile(p, tgzBuild(t, tgzGamesEntries()), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	sink := &tgzSink{}
	res := importInputs([]string{p}, &runOptions{markDone: true, tmpDir: tmp}, sink, nil)
	if res.Inputs != 1 || res.InputErrors != 0 || res.Stopped {
		t.Fatalf("inputs=%d input-errors=%d stopped=%v, want 1/0/false", res.Inputs, res.InputErrors, res.Stopped)
	}
	if got, want := sink.list(), tgzGamesWant(); got != want {
		t.Errorf("articles:\n%s\nwant:\n%s", got, want)
	}
	if res.MarkedDone != 1 || !miExists(p+".done") {
		t.Errorf("marked-done=%d renamed=%v, want 1/true", res.MarkedDone, miExists(p+".done"))
	}
	tgzDirEmpty(t, "import", tmp)
}

// TestMboxTarGzOrderAndGroups reads the gmane archive in memory and through the spill file:
// the same order, groups and counters both ways (.db counted as not-article, directories
// and links nowhere).
func TestMboxTarGzOrderAndGroups(t *testing.T) {
	data := tgzBuild(t, tgzGamesEntries())
	for _, l := range tgzLimits {
		tmp := t.TempDir()
		sink := &tgzSink{}
		c, run, err := tgzRead(t, "/mirror/gmane.games.tar.gz.BACKUP.2021-03/gmane.games.tar.gz", data,
			&runOptions{tmpDir: tmp}, sink, nil, l.memLimit, l.memMember)
		if err != nil {
			t.Fatalf("%s: %v", l.name, err)
		}
		if got, want := sink.list(), tgzGamesWant(); got != want {
			t.Errorf("%s: articles:\n%s\nwant:\n%s", l.name, got, want)
		}
		if c.Files != 6 || c.NotArticle != 1 || c.Read != 5 || c.FileErrors != 0 || c.XrefPointers != 0 {
			t.Errorf("%s: files=%d not-article=%d read=%d file-errors=%d xref=%d, want 6/1/5/0/0",
				l.name, c.Files, c.NotArticle, c.Read, c.FileErrors, c.XrefPointers)
		}
		if (run.spilled > 0) != l.spills {
			t.Errorf("%s: spilled=%d members", l.name, run.spilled)
		}
		tgzDirEmpty(t, l.name, tmp)
	}
}

// TestMboxTarGzTextusenet: a textusenet archive (vifa.tar.gz, r50.tar.gz) holds the whole
// group path, so the directory alone gives the group; a crosspost goes to the directory's
// group, an article that does not list it to its first group.
func TestMboxTarGzTextusenet(t *testing.T) {
	data := tgzBuild(t, []tgzEntry{
		{name: "vifa/", typ: tar.TypeDir},
		{name: "vifa/soc/glbf/.art2", body: tgzArticle(tgzID("glbf", 2), "de.test,vifa.soc.glbf")},
		{name: "vifa/soc/glbf/.art1", body: tgzArticle(tgzID("glbf", 1), "vifa.soc.glbf")},
		{name: "vifa/soc/glbf/.db", body: tgzDB},
		{name: "vifa/soc/other/.art1", body: tgzArticle(tgzID("other", 1), "alt.elsewhere,de.test")},
	})
	want := strings.Join([]string{
		tgzID("glbf", 1) + " vifa.soc.glbf", tgzID("glbf", 2) + " vifa.soc.glbf", tgzID("other", 1) + " alt.elsewhere",
	}, "\n")
	for _, label := range []string{"textusenet-backup_v/vifa.tar.gz", "r50.tgz"} {
		sink := &tgzSink{}
		if _, _, err := tgzRead(t, label, data, &runOptions{}, sink, nil, tarRunMemory, tarMemberMemory); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got := sink.list(); got != want {
			t.Errorf("%s: articles:\n%s\nwant:\n%s", label, got, want)
		}
	}
}

// TestMboxTarGzGroupPrefix pins the hierarchy prefix taken from the file name and how the
// walker applies it to a member's directory.
func TestMboxTarGzGroupPrefix(t *testing.T) {
	for _, c := range []struct{ name, prefix, top string }{
		{"gmane.psychology.tar.gz", "gmane", "psychology"},
		{"/m/gmane.comp.tar.gz.BACKUP.2021-03/gmane.comp.tar.gz", "gmane", "comp"},
		{"gmane.comp.lang.tgz", "gmane/comp", "lang"},
		{"gmane.games.tar.gz.done", "gmane", "games"},
		{"GMANE.Comp.TAR.GZ", "GMANE", "Comp"},
		{"rec.tar.gz", "", ""},
		{"r50.tar.gz", "", ""},
		{".tar.gz", "", ""},
		{"gmane..tar.gz", "", ""},
	} {
		p, top := tarGroupPrefix(c.name)
		if p != c.prefix || top != c.top {
			t.Errorf("tarGroupPrefix(%q) = %q, %q; want %q, %q", c.name, p, top, c.prefix, c.top)
		}
	}
	w := &spoolWalker{prefixDir: "gmane", topDir: "psychology"}
	for _, c := range []struct{ dir, want string }{
		{"psychology", "gmane/psychology"},
		{"psychology/behavior-analysis", "gmane/psychology/behavior-analysis"},
		{"Psychology/X", "gmane/Psychology/X"},
		{"psychologyx/y", "psychologyx/y"},
		{"comp/ai", "comp/ai"},
		{".", "."},
	} {
		if got := w.sourceDirOf(c.dir); got != c.want {
			t.Errorf("sourceDirOf(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
	if got := (&spoolWalker{}).sourceDirOf("SPOOL/alt/test"); got != "SPOOL/alt/test" {
		t.Errorf("sourceDirOf without a prefix = %q", got)
	}
	// gmane.psychology.tar.gz: psychology/behavior-analysis -> gmane.psychology.behavior-analysis
	data := tgzBuild(t, []tgzEntry{
		{name: "psychology/behavior-analysis/.art1",
			body: tgzArticle("<ba1@targz.test.invalid>", "gmane.misc,gmane.psychology.behavior-analysis")},
		{name: "psychology/.art1", body: tgzArticle("<p1@targz.test.invalid>", "gmane.misc,gmane.psychology")},
	})
	sink := &tgzSink{}
	if _, _, err := tgzRead(t, "gmane.psychology.tar.gz", data, &runOptions{}, sink, nil, tarRunMemory, tarMemberMemory); err != nil {
		t.Fatal(err)
	}
	want := "<ba1@targz.test.invalid> gmane.psychology.behavior-analysis\n<p1@targz.test.invalid> gmane.psychology"
	if got := sink.list(); got != want {
		t.Errorf("articles:\n%s\nwant:\n%s", got, want)
	}
}

// TestMboxTarGzSplitRuns: a directory that comes back later in the archive is a run of its
// own, each run sorted within itself.
func TestMboxTarGzSplitRuns(t *testing.T) {
	art := func(dir string, n int) tgzEntry {
		return tgzEntry{name: dir + "/.art" + strconv.Itoa(n), body: tgzArticle(tgzID(dir, n), strings.ReplaceAll(dir, "/", "."))}
	}
	data := tgzBuild(t, []tgzEntry{
		art("alt/a", 2), art("alt/a", 1), art("alt/b", 1), art("alt/a", 4), art("alt/a", 3),
	})
	var want []string
	for _, x := range []struct {
		dir string
		n   int
	}{{"alt/a", 1}, {"alt/a", 2}, {"alt/b", 1}, {"alt/a", 3}, {"alt/a", 4}} {
		want = append(want, tgzID(x.dir, x.n)+" "+strings.ReplaceAll(x.dir, "/", "."))
	}
	for _, l := range tgzLimits {
		sink := &tgzSink{}
		if _, _, err := tgzRead(t, "alt.tar.gz", data, &runOptions{tmpDir: t.TempDir()}, sink, nil, l.memLimit, l.memMember); err != nil {
			t.Fatalf("%s: %v", l.name, err)
		}
		if got := sink.list(); got != strings.Join(want, "\n") {
			t.Errorf("%s: articles:\n%s\nwant:\n%s", l.name, got, strings.Join(want, "\n"))
		}
	}
}

// tgzTruncEntries is an archive whose second article is big enough to cut it in the middle.
func tgzTruncEntries() ([]tgzEntry, string) {
	marker := "CUT-THE-ARCHIVE-HERE"
	big := tgzArticle("<big@targz.test.invalid>", "alt.test") +
		strings.Repeat("filler line of the big article\n", 300) + marker + "\n" +
		strings.Repeat("more filler after the cut\n", 300)
	return []tgzEntry{
		{name: "alt/test/.art1", body: tgzArticle("<small@targz.test.invalid>", "alt.test")},
		{name: "alt/test/.art2", body: big},
		{name: "alt/test/.art3", body: tgzArticle("<after@targz.test.invalid>", "alt.test")},
	}, marker
}

// TestMboxTarGzTruncated: a gzip stream cut in the middle of a member is an input error;
// the member it cut short is never handed over, the one read whole before it is; through
// the import loop the input is not marked done and is stopped-early.
func TestMboxTarGzTruncated(t *testing.T) {
	entries, marker := tgzTruncEntries()
	// stored deflate blocks: the member's bytes are visible in the compressed stream
	full := tgzGzip(t, tgzTar(t, entries), gzip.NoCompression)
	cut := bytes.Index(full, []byte(marker))
	if cut < 0 {
		t.Fatal("marker not in the stored gzip stream")
	}
	data := full[:cut]
	for _, l := range tgzLimits {
		tmp := t.TempDir()
		sink := &tgzSink{}
		c, _, err := tgzRead(t, "alt.test.tar.gz", data, &runOptions{tmpDir: tmp}, sink, nil, l.memLimit, l.memMember)
		if err == nil || errors.Is(err, errStopped) || errors.Is(err, errTarSpill) {
			t.Errorf("%s: err=%v, want an archive read error", l.name, err)
		}
		if got := sink.list(); got != "<small@targz.test.invalid> alt.test" {
			t.Errorf("%s: articles:\n%s\nwant only the small one", l.name, got)
		}
		if c.Read != 1 {
			t.Errorf("%s: read=%d, want 1", l.name, c.Read)
		}
		tgzDirEmpty(t, l.name, tmp)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "alt.test.tar.gz")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &tgzSink{}
	res := importInputs([]string{p}, &runOptions{markDone: true, tmpDir: t.TempDir()}, sink, nil)
	if res.InputErrors != 1 || res.MarkedDone != 0 || miExists(p+".done") || res.Stopped {
		t.Errorf("truncated: input-errors=%d marked-done=%d stopped=%v, want 1/0/false",
			res.InputErrors, res.MarkedDone, res.Stopped)
	}
	if len(res.PerInput) != 1 || !res.PerInput[0].StoppedEarly || res.PerInput[0].Stored != 1 {
		t.Errorf("truncated: per-input %+v, want stopped-early and 1 stored", res.PerInput)
	}

	// a gzip checksum error at the very end, after the tar: still an input error
	bad := append([]byte(nil), full...)
	bad[len(bad)-8] ^= 0xff // the CRC-32 of the trailer
	sink = &tgzSink{}
	c, _, err := tgzRead(t, "alt.test.tar.gz", bad, &runOptions{}, sink, nil, tarRunMemory, tarMemberMemory)
	if err == nil || !errors.Is(err, gzip.ErrChecksum) {
		t.Errorf("checksum: err=%v, want gzip.ErrChecksum", err)
	}
	if c.Read != 3 {
		t.Errorf("checksum: read=%d, want 3 (the members are whole)", c.Read)
	}
}

// TestMboxTarGzCorruptHeader: a tar header with a wrong checksum ends the input as an input
// error; the member before it is read.
func TestMboxTarGzCorruptHeader(t *testing.T) {
	entries, _ := tgzTruncEntries()
	raw := tgzTar(t, entries)
	first := len(entries[0].body)
	second := 512 + (first+511)/512*512 // the second header
	raw[second] ^= 0x20                 // a name byte: the header checksum no longer matches
	sink := &tgzSink{}
	c, _, err := tgzRead(t, "alt.test.tar.gz", tgzGzip(t, raw, gzip.DefaultCompression), &runOptions{}, sink, nil,
		tarRunMemory, tarMemberMemory)
	if !errors.Is(err, tar.ErrHeader) {
		t.Errorf("err=%v, want tar.ErrHeader", err)
	}
	if c.Read != 1 || sink.list() != "<small@targz.test.invalid> alt.test" {
		t.Errorf("read=%d articles %q, want only the first", c.Read, sink.list())
	}

	// not gzip at all: an input error before anything is read
	dir := t.TempDir()
	p := filepath.Join(dir, "plain.tar.gz")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	res := importInputs([]string{p}, &runOptions{markDone: true}, &tgzSink{}, nil)
	if res.InputErrors != 1 || miExists(p+".done") {
		t.Errorf("not gzip: input-errors=%d renamed=%v, want 1/false", res.InputErrors, miExists(p+".done"))
	}
}

// TestMboxTarGzStop: the stop channel closed after the first article ends the read as
// stopped, with the spill file closed and nothing left in -tmpdir; through the import loop
// the input is stopped and not renamed.
func TestMboxTarGzStop(t *testing.T) {
	var entries []tgzEntry
	for _, dir := range []string{"alt/a", "alt/b"} {
		for n := 5; n >= 1; n-- {
			entries = append(entries, tgzEntry{name: dir + "/.art" + strconv.Itoa(n),
				body: tgzArticle(tgzID(dir, n), strings.ReplaceAll(dir, "/", "."))})
		}
	}
	data := tgzBuild(t, entries)
	for _, l := range tgzLimits {
		tmp := t.TempDir()
		stop := make(chan struct{})
		var once sync.Once
		sink := &tgzSink{onHandle: func() { once.Do(func() { close(stop) }) }}
		c, run, err := tgzRead(t, "alt.tar.gz", data, &runOptions{tmpDir: tmp}, sink, stop, l.memLimit, l.memMember)
		if !errors.Is(err, errStopped) {
			t.Errorf("%s: err=%v, want errStopped", l.name, err)
		}
		if c.Read != 1 {
			t.Errorf("%s: read=%d, want 1", l.name, c.Read)
		}
		if (run.spilled > 0) != l.spills {
			t.Errorf("%s: spilled=%d", l.name, run.spilled)
		}
		tgzDirEmpty(t, l.name, tmp)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "alt.tar.gz")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	stop := make(chan struct{})
	var once sync.Once
	sink := &tgzSink{onHandle: func() { once.Do(func() { close(stop) }) }}
	res := importInputs([]string{p}, &runOptions{markDone: true, tmpDir: tmp}, sink, stop)
	if !res.Stopped || res.InputErrors != 0 || res.MarkedDone != 0 || miExists(p+".done") {
		t.Errorf("stopped=%v input-errors=%d marked-done=%d, want true/0/0", res.Stopped, res.InputErrors, res.MarkedDone)
	}
	tgzDirEmpty(t, "import", tmp)
}

// TestMboxTarGzMaxArticleSize: with -max-article-size a big one-article member counts as
// too-big whether it is kept in memory or spilled, and an mbox member over the cap is kept
// whole (each of its messages is under the cap, the second one starts just before it: cut
// at the cap it would lose its Message-ID); without the cap the big article is stored.
func TestMboxTarGzMaxArticleSize(t *testing.T) {
	big := tgzArticle("<big@targz.test.invalid>", "alt.test") + strings.Repeat("x line of the big article\n", 800)
	mboxFile := miArticle("<m1@targz.test.invalid>", "alt.test", "one") +
		strings.Repeat("y line of filler text\n", 168) + "\n" + miArticle("<m2@targz.test.invalid>", "alt.test", "two")
	if n := len(mboxFile) - len(miArticle("<m2@targz.test.invalid>", "alt.test", "two")); n > 4096 || n < 3900 {
		t.Fatalf("the first mbox message is %d bytes: the test needs it just under the cap", n)
	}
	data := tgzBuild(t, []tgzEntry{
		{name: "alt/test/.art1", body: big},
		{name: "alt/test/DOUG.PRO", body: mboxFile},
		{name: "alt/test/.art2", body: tgzArticle("<small@targz.test.invalid>", "alt.test")},
	})
	for _, l := range tgzLimits {
		sink := &tgzSink{}
		c, _, err := tgzRead(t, "alt.test.tar.gz", data, &runOptions{maxArticleSize: 4096, tmpDir: t.TempDir()},
			sink, nil, l.memLimit, l.memMember)
		if err != nil {
			t.Fatalf("%s: %v", l.name, err)
		}
		if c.Skipped[skipTooBig] != 1 || c.Read != 4 {
			t.Errorf("%s: too-big=%d read=%d, want 1/4", l.name, c.Skipped[skipTooBig], c.Read)
		}
		want := "<small@targz.test.invalid> alt.test\n<m1@targz.test.invalid> alt.test\n<m2@targz.test.invalid> alt.test"
		if got := sink.list(); got != want {
			t.Errorf("%s: articles:\n%s\nwant:\n%s", l.name, got, want)
		}
	}
	sink := &tgzSink{}
	c, _, err := tgzRead(t, "alt.test.tar.gz", data, &runOptions{}, sink, nil, tarRunMemory, tarMemberMemory)
	if err != nil || c.Skipped[skipTooBig] != 0 || c.Stored != 4 {
		t.Errorf("no cap: err=%v too-big=%d stored=%d, want nil/0/4", err, c.Skipped[skipTooBig], c.Stored)
	}
}

// TestMboxTarGzInputKind pins the suffix rules: .tar.gz and .tgz are news spools by name
// (positional and -from-iafetch), never mboxes; a plain .tar stays unsupported.
func TestMboxTarGzInputKind(t *testing.T) {
	for _, c := range []struct {
		name, kind string
		supported  bool
		reason     string
	}{
		{"gmane.comp.tar.gz", kindTarGz, true, ""},
		{"textusenet-backup_r/rec.tar.gz", kindTarGz, true, ""},
		{"r50.TGZ", kindTarGz, true, ""},
		{"alt.foo.mbox.tar.gz", kindTarGz, true, ""},
		{"rec.tar.gz.done", kindTarGz, false, iaReasonUnsupported}, // positional: imported again; mirror: not an input
		{"spool.tar", "mbox", false, "tar"},
		{"alt.foo.mbox.gz", "gz", true, ""},
	} {
		if got := inputKind(c.name); got != c.kind {
			t.Errorf("inputKind(%q) = %q, want %q", c.name, got, c.kind)
		}
		ok, reason := iafetchSupported(c.name)
		if ok != c.supported || reason != c.reason {
			t.Errorf("iafetchSupported(%q) = %v, %q; want %v, %q", c.name, ok, reason, c.supported, c.reason)
		}
	}
	for _, spool := range []bool{false, true} {
		if got := spoolInput("/m/gmane.comp.tar.gz", &runOptions{spool: spool}); got != spoolTgz {
			t.Errorf("spoolInput(.tar.gz, spool=%v) = %d, want spoolTgz", spool, got)
		}
	}
	p := writeFile(t, "x.tar.gz", "not opened as an mbox")
	if src, err := openSource(p, "7z"); err == nil {
		_ = src.Close()
		t.Error("openSource of a .tar.gz: no error")
	}
}

// TestMboxTarGzDryRun: a dry run reads and counts the archive per target group, renames
// nothing and leaves nothing in -tmpdir; a spool without one article is an input error.
func TestMboxTarGzDryRun(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gmane.games.tar.gz")
	if err := os.WriteFile(p, tgzBuild(t, tgzGamesEntries()), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	sink := newCountingSink()
	res := importInputs([]string{p}, &runOptions{dryRun: true, markDone: true, tmpDir: tmp}, sink, nil)
	if res.Inputs != 1 || res.InputErrors != 0 || res.MarkedDone != 0 || miExists(p+".done") {
		t.Errorf("inputs=%d input-errors=%d marked-done=%d, want 1/0/0", res.Inputs, res.InputErrors, res.MarkedDone)
	}
	if sink.groups["gmane.games.advanced-squad-leader"] != 4 || sink.groups["gmane.games.other"] != 1 || len(sink.groups) != 2 {
		t.Errorf("groups %v, want 4 in gmane.games.advanced-squad-leader and 1 in gmane.games.other", sink.groups)
	}
	if pc := res.PerInput[0]; pc.Files != 6 || pc.NotArticle != 1 {
		t.Errorf("files=%d not-article=%d, want 6/1", pc.Files, pc.NotArticle)
	}
	tgzDirEmpty(t, "dry run", tmp)

	empty := filepath.Join(dir, "empty.tar.gz")
	if err := os.WriteFile(empty, tgzBuild(t, []tgzEntry{{name: "x/.db", body: tgzDB}}), 0o644); err != nil {
		t.Fatal(err)
	}
	res = importInputs([]string{empty}, &runOptions{markDone: true}, newCountingSink(), nil)
	if res.InputErrors != 1 || miExists(empty+".done") {
		t.Errorf("no articles: input-errors=%d renamed=%v, want 1/false", res.InputErrors, miExists(empty+".done"))
	}
}
