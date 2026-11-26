package main

// Tests of the .tar.gz news spools (plan importer-targz-spool): members streamed in archive
// order, the gmane hierarchy prefix from the file name, textusenet naming, members that are
// passed over, truncated and corrupt archives, data after the end of the tar, GNU sparse
// members, the stop channel, -max-article-size, the -from-iafetch suffix rule and the dry
// run. The archives are built in the tests.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
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
// members of advanced-squad-leader in archive (not numeric) order with the .db and an empty
// member among them, a symbolic and a hard link (passed over), and a crossposted article in
// games/other that lists another gmane group first.
func tgzGamesEntries() []tgzEntry {
	asl := "games/advanced-squad-leader"
	g := "gmane.games.advanced-squad-leader"
	return []tgzEntry{
		{name: "games/", typ: tar.TypeDir},
		{name: asl + "/", typ: tar.TypeDir},
		{name: asl + "/.art10", body: tgzArticle(tgzID(asl, 10), g)},
		{name: asl + "/.art1", body: tgzArticle(tgzID(asl, 1), g)},
		{name: asl + "/.db", body: tgzDB},
		{name: asl + "/.art0"}, // empty
		{name: asl + "/.art9", body: tgzArticle(tgzID(asl, 9), g)},
		{name: asl + "/.art99", typ: tar.TypeSymlink, link: ".art1"},
		{name: asl + "/.art98", typ: tar.TypeLink, link: asl + "/.art1"},
		{name: asl + "/.art2", body: tgzArticle(tgzID(asl, 2), g)},
		{name: "games/other/", typ: tar.TypeDir},
		{name: "./games/other/.art3", body: tgzArticle(tgzID("games/other", 3), "gmane.misc.first,gmane.games.other")},
	}
}

// tgzGamesWant is what reading tgzGamesEntries hands over: the archive order.
func tgzGamesWant() string {
	asl := "games/advanced-squad-leader"
	g := "gmane.games.advanced-squad-leader"
	return strings.Join([]string{
		tgzID(asl, 10) + " " + g, tgzID(asl, 1) + " " + g, tgzID(asl, 9) + " " + g, tgzID(asl, 2) + " " + g,
		tgzID("games/other", 3) + " gmane.games.other",
	}, "\n")
}

// tgzRead reads data (a .tar.gz) as the input label.
func tgzRead(t *testing.T, label string, data []byte, opts *runOptions, sink inputSink, stop <-chan struct{}) (*counters, error) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	return readTarSpool(gz, label, opts, sink, stop)
}

// TestMboxTarGzImport is the end-to-end test through the import loop: a gmane-style
// .tar.gz positional input is read as a spool in archive order, filed into the gmane groups
// and renamed .done. On the base the input was opened as a gzip mbox and failed ("no mbox
// separator").
func TestMboxTarGzImport(t *testing.T) {
	if ok, reason := iafetchSupported("gmane.games.tar.gz.BACKUP.2021-03/gmane.games.tar.gz"); !ok {
		t.Errorf("iafetchSupported(gmane.games.tar.gz) = false (%s), want true", reason)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "gmane.games.tar.gz")
	if err := os.WriteFile(p, tgzBuild(t, tgzGamesEntries()), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &tgzSink{}
	res := importInputs([]string{p}, &runOptions{markDone: true}, sink, nil)
	if res.Inputs != 1 || res.InputErrors != 0 || res.Stopped {
		t.Fatalf("inputs=%d input-errors=%d stopped=%v, want 1/0/false", res.Inputs, res.InputErrors, res.Stopped)
	}
	if got, want := sink.list(), tgzGamesWant(); got != want {
		t.Errorf("articles:\n%s\nwant:\n%s", got, want)
	}
	if res.MarkedDone != 1 || !miExists(p+".done") {
		t.Errorf("marked-done=%d renamed=%v, want 1/true", res.MarkedDone, miExists(p+".done"))
	}
}

// TestMboxTarGzArchiveOrderAndGroups: the members are handed over in archive order, filed by
// their directory with the gmane prefix; the .db and the empty member count as not-article,
// directories and links nowhere.
func TestMboxTarGzArchiveOrderAndGroups(t *testing.T) {
	sink := &tgzSink{}
	c, err := tgzRead(t, "/mirror/gmane.games.tar.gz.BACKUP.2021-03/gmane.games.tar.gz", tgzBuild(t, tgzGamesEntries()),
		&runOptions{}, sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sink.list(), tgzGamesWant(); got != want {
		t.Errorf("articles:\n%s\nwant:\n%s", got, want)
	}
	if c.Files != 7 || c.NotArticle != 2 || c.Read != 5 || c.FileErrors != 0 || c.XrefPointers != 0 {
		t.Errorf("files=%d not-article=%d read=%d file-errors=%d xref=%d, want 7/2/5/0/0",
			c.Files, c.NotArticle, c.Read, c.FileErrors, c.XrefPointers)
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
		tgzID("glbf", 2) + " vifa.soc.glbf", tgzID("glbf", 1) + " vifa.soc.glbf", tgzID("other", 1) + " alt.elsewhere",
	}, "\n")
	for _, label := range []string{"textusenet-backup_v/vifa.tar.gz", "r50.tgz"} {
		sink := &tgzSink{}
		if _, err := tgzRead(t, label, data, &runOptions{}, sink, nil); err != nil {
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
	if _, err := tgzRead(t, "gmane.psychology.tar.gz", data, &runOptions{}, sink, nil); err != nil {
		t.Fatal(err)
	}
	want := "<ba1@targz.test.invalid> gmane.psychology.behavior-analysis\n<p1@targz.test.invalid> gmane.psychology"
	if got := sink.list(); got != want {
		t.Errorf("articles:\n%s\nwant:\n%s", got, want)
	}
}

// TestMboxTarGzArtNumberOrder: .artN sorts as article number N in a spool directory (an
// unpacked .tar.gz walked with -spool); all-digit names keep their order.
func TestMboxTarGzArtNumberOrder(t *testing.T) {
	if !spoolNameLess(".art9", ".art10") || spoolNameLess(".art10", ".art9") {
		t.Error("spoolNameLess: .art9 must come before .art10")
	}
	if !spoolNameLess("9", "10") || !spoolNameLess(".art10", ".db") {
		t.Error("spoolNameLess: numbers first, numerically")
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
// the member it cut short is never handed over, the one before it is; through the import
// loop the input is not marked done and is stopped-early. With -max-article-size the cut
// can fall in the part read past: still an input error, nothing of that member stored.
func TestMboxTarGzTruncated(t *testing.T) {
	entries, marker := tgzTruncEntries()
	// stored deflate blocks: the member's bytes are visible in the compressed stream
	full := tgzGzip(t, tgzTar(t, entries), gzip.NoCompression)
	cut := bytes.Index(full, []byte(marker))
	if cut < 0 {
		t.Fatal("marker not in the stored gzip stream")
	}
	data := full[:cut]
	for _, maxSize := range []int{0, 1024} {
		sink := &tgzSink{}
		c, err := tgzRead(t, "alt.test.tar.gz", data, &runOptions{maxArticleSize: maxSize}, sink, nil)
		if err == nil || errors.Is(err, errStopped) {
			t.Errorf("max %d: err=%v, want an archive read error", maxSize, err)
		}
		if got := sink.list(); got != "<small@targz.test.invalid> alt.test" {
			t.Errorf("max %d: articles:\n%s\nwant only the small one", maxSize, got)
		}
		if c.Read != 1 {
			t.Errorf("max %d: read=%d, want 1", maxSize, c.Read)
		}
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "alt.test.tar.gz")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	res := importInputs([]string{p}, &runOptions{markDone: true}, &tgzSink{}, nil)
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
	c, err := tgzRead(t, "alt.test.tar.gz", bad, &runOptions{}, &tgzSink{}, nil)
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
	c, err := tgzRead(t, "alt.test.tar.gz", tgzGzip(t, raw, gzip.DefaultCompression), &runOptions{}, sink, nil)
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
// stopped before the next member; through the import loop the input is stopped and not
// renamed.
func TestMboxTarGzStop(t *testing.T) {
	var entries []tgzEntry
	for _, dir := range []string{"alt/a", "alt/b"} {
		for n := 5; n >= 1; n-- {
			entries = append(entries, tgzEntry{name: dir + "/.art" + strconv.Itoa(n),
				body: tgzArticle(tgzID(dir, n), strings.ReplaceAll(dir, "/", "."))})
		}
	}
	data := tgzBuild(t, entries)
	stop := make(chan struct{})
	var once sync.Once
	sink := &tgzSink{onHandle: func() { once.Do(func() { close(stop) }) }}
	c, err := tgzRead(t, "alt.tar.gz", data, &runOptions{}, sink, stop)
	if !errors.Is(err, errStopped) || c.Read != 1 {
		t.Errorf("err=%v read=%d, want errStopped and 1", err, c.Read)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "alt.tar.gz")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	stop = make(chan struct{})
	var once2 sync.Once
	sink = &tgzSink{onHandle: func() { once2.Do(func() { close(stop) }) }}
	res := importInputs([]string{p}, &runOptions{markDone: true}, sink, stop)
	if !res.Stopped || res.InputErrors != 0 || res.MarkedDone != 0 || miExists(p+".done") {
		t.Errorf("stopped=%v input-errors=%d marked-done=%d, want true/0/0", res.Stopped, res.InputErrors, res.MarkedDone)
	}
}

// TestMboxTarGzMaxArticleSize: with -max-article-size a big one-article member counts as
// too-big, and an mbox member over the cap is read whole (each of its messages is under the
// cap); without the cap the big article is stored.
func TestMboxTarGzMaxArticleSize(t *testing.T) {
	big := tgzArticle("<big@targz.test.invalid>", "alt.test") + strings.Repeat("x line of the big article\n", 800)
	mboxFile := miArticle("<m1@targz.test.invalid>", "alt.test", "one") +
		strings.Repeat("y line of filler text\n", 168) + "\n" + miArticle("<m2@targz.test.invalid>", "alt.test", "two")
	data := tgzBuild(t, []tgzEntry{
		{name: "alt/test/.art1", body: big},
		{name: "alt/test/DOUG.PRO", body: mboxFile},
		{name: "alt/test/.art2", body: tgzArticle("<small@targz.test.invalid>", "alt.test")},
	})
	sink := &tgzSink{}
	c, err := tgzRead(t, "alt.test.tar.gz", data, &runOptions{maxArticleSize: 4096}, sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Skipped[skipTooBig] != 1 || c.Read != 4 {
		t.Errorf("too-big=%d read=%d, want 1/4", c.Skipped[skipTooBig], c.Read)
	}
	want := "<m1@targz.test.invalid> alt.test\n<m2@targz.test.invalid> alt.test\n<small@targz.test.invalid> alt.test"
	if got := sink.list(); got != want {
		t.Errorf("articles:\n%s\nwant:\n%s", got, want)
	}
	c, err = tgzRead(t, "alt.test.tar.gz", data, &runOptions{}, &tgzSink{}, nil)
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

// TestMboxTarGzDryRun: a dry run reads and counts the archive per target group and renames
// nothing; a spool without one article is an input error.
func TestMboxTarGzDryRun(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gmane.games.tar.gz")
	if err := os.WriteFile(p, tgzBuild(t, tgzGamesEntries()), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := newCountingSink()
	res := importInputs([]string{p}, &runOptions{dryRun: true, markDone: true}, sink, nil)
	if res.Inputs != 1 || res.InputErrors != 0 || res.MarkedDone != 0 || miExists(p+".done") {
		t.Errorf("inputs=%d input-errors=%d marked-done=%d, want 1/0/0", res.Inputs, res.InputErrors, res.MarkedDone)
	}
	if sink.groups["gmane.games.advanced-squad-leader"] != 4 || sink.groups["gmane.games.other"] != 1 || len(sink.groups) != 2 {
		t.Errorf("groups %v, want 4 in gmane.games.advanced-squad-leader and 1 in gmane.games.other", sink.groups)
	}
	if pc := res.PerInput[0]; pc.Files != 7 || pc.NotArticle != 2 {
		t.Errorf("files=%d not-article=%d, want 7/2", pc.Files, pc.NotArticle)
	}

	empty := filepath.Join(dir, "empty.tar.gz")
	if err := os.WriteFile(empty, tgzBuild(t, []tgzEntry{{name: "x/.db", body: tgzDB}}), 0o644); err != nil {
		t.Fatal(err)
	}
	res = importInputs([]string{empty}, &runOptions{markDone: true}, newCountingSink(), nil)
	if res.InputErrors != 1 || miExists(empty+".done") {
		t.Errorf("no articles: input-errors=%d renamed=%v, want 1/false", res.InputErrors, miExists(empty+".done"))
	}
}

// TestMboxTarGzTrailingData: zero padding after the tar's end-of-archive blocks is fine;
// a second tar in the same gzip stream, or two .tar.gz files joined with cat, is an input
// error (the second archive is never dropped as if the input were done).
func TestMboxTarGzTrailingData(t *testing.T) {
	a := tgzTar(t, []tgzEntry{{name: "alt/a/.art1", body: tgzArticle("<a1@targz.test.invalid>", "alt.a")}})
	b := tgzTar(t, []tgzEntry{{name: "alt/b/.art1", body: tgzArticle("<b1@targz.test.invalid>", "alt.b")}})

	// padded to a 10240-byte record, as GNU tar writes it
	padded := append(append([]byte(nil), a...), make([]byte, 10240-len(a)%10240)...)
	c, err := tgzRead(t, "alt.tar.gz", tgzGzip(t, padded, gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
	if err != nil || c.Read != 1 {
		t.Errorf("zero padding: err=%v read=%d, want nil/1", err, c.Read)
	}

	// two tars in one gzip stream
	two := tgzGzip(t, append(append([]byte(nil), a...), b...), gzip.DefaultCompression)
	sink := &tgzSink{}
	_, err = tgzRead(t, "alt.tar.gz", two, &runOptions{}, sink, nil)
	if !errors.Is(err, errTarTrailing) {
		t.Errorf("two tars in one stream: err=%v, want errTarTrailing", err)
	}
	if got := sink.list(); got != "<a1@targz.test.invalid> alt.a" {
		t.Errorf("two tars in one stream: articles %q, want the first tar's", got)
	}

	// cat a.tar.gz b.tar.gz: the gzip reader reads the second member as more of the stream
	dir := t.TempDir()
	p := filepath.Join(dir, "alt.tar.gz")
	cat := append(tgzGzip(t, a, gzip.DefaultCompression), tgzGzip(t, b, gzip.DefaultCompression)...)
	if err := os.WriteFile(p, cat, 0o644); err != nil {
		t.Fatal(err)
	}
	res := importInputs([]string{p}, &runOptions{markDone: true}, &tgzSink{}, nil)
	if res.InputErrors != 1 || res.MarkedDone != 0 || miExists(p+".done") {
		t.Errorf("cat of two .tar.gz: input-errors=%d marked-done=%d renamed=%v, want 1/0/false",
			res.InputErrors, res.MarkedDone, miExists(p+".done"))
	}
	if len(res.PerInput) != 1 || !res.PerInput[0].StoppedEarly {
		t.Errorf("cat of two .tar.gz: per-input %+v, want stopped-early", res.PerInput)
	}
}

// TestMboxTarGzEndBlocks: a tar that ends at a member boundary without its end-of-archive
// blocks (a partial tar, compressed whole: the gzip stream is intact) is an input error and
// not marked done, also when the last member's data ends in zero blocks; one zero block and
// then the end of the stream is accepted, as Go's reader accepts it.
func TestMboxTarGzEndBlocks(t *testing.T) {
	full := tgzTar(t, []tgzEntry{
		{name: "alt/a/.art1", body: tgzArticle("<e1@targz.test.invalid>", "alt.a")},
		{name: "alt/a/.art2", body: tgzArticle("<e2@targz.test.invalid>", "alt.a")},
	})
	zeros := tgzTar(t, []tgzEntry{
		{name: "alt/a/.art1", body: tgzArticle("<e1@targz.test.invalid>", "alt.a")},
		{name: "alt/a/.db", body: strings.Repeat("\x00", 2048)},
	})
	for _, c := range []struct {
		name string
		tar  []byte
		ok   bool
	}{
		{"both end blocks", full, true},
		{"one end block", full[:len(full)-512], true},
		{"no end blocks", full[:len(full)-1024], false},
		{"no end blocks, last member all zeros", zeros[:len(zeros)-1024], false},
		{"last member all zeros, end blocks", zeros, true},
	} {
		c1, err := tgzRead(t, "alt.tar.gz", tgzGzip(t, c.tar, gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
		switch {
		case c.ok && err != nil:
			t.Errorf("%s: err=%v, want nil", c.name, err)
		case !c.ok && (err == nil || !strings.Contains(err.Error(), "end-of-archive")):
			t.Errorf("%s: err=%v, want tar ended without its end-of-archive blocks", c.name, err)
		}
		if c1.Read < 1 { // the members before the cut are read either way
			t.Errorf("%s: read=%d, want at least 1", c.name, c1.Read)
		}
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "alt.tar.gz")
	if err := os.WriteFile(p, tgzGzip(t, full[:len(full)-1024], gzip.DefaultCompression), 0o644); err != nil {
		t.Fatal(err)
	}
	res := importInputs([]string{p}, &runOptions{markDone: true}, &tgzSink{}, nil)
	if res.InputErrors != 1 || res.MarkedDone != 0 || miExists(p+".done") {
		t.Errorf("no end blocks: input-errors=%d marked-done=%d renamed=%v, want 1/0/false",
			res.InputErrors, res.MarkedDone, miExists(p+".done"))
	}
}

// tgzOctal writes v as a NUL-terminated octal field of a tar header.
func tgzOctal(dst []byte, v int64) {
	copy(dst, fmt.Sprintf("%0*o", len(dst)-1, v))
	dst[len(dst)-1] = 0
}

// tgzSparseTar is a tar holding one old-GNU sparse member ('S', the format GNU tar writes
// with --sparse) without holes: one sparse entry over the whole body. archive/tar's writer
// cannot write sparse files, so the header block is built here.
func tgzSparseTar(name, body string) []byte { return tgzSparseHoleTar(name, body, int64(len(body))) }

// tgzSparseHoleTar is tgzSparseTar with the real size given: past the body, up to realSize,
// the member is one hole.
func tgzSparseHoleTar(name, body string, realSize int64) []byte {
	return tgzSparseSegsTar(name, []tgzSeg{{0, body}}, realSize)
}

// tgzSeg is one stored segment of a sparse member: its offset in the expanded file and its
// bytes; between segments the file is a hole.
type tgzSeg struct {
	off  int64
	data string
}

// tgzChecksum fills in the checksum field of a tar header block.
func tgzChecksum(blk []byte) {
	copy(blk[148:156], "        ")
	var sum int64
	for _, c := range blk {
		sum += int64(c)
	}
	copy(blk[148:156], fmt.Sprintf("%06o\x00 ", sum))
}

// tgzPad pads b with zeros to the 512-byte block size.
func tgzPad(b []byte) []byte { return append(b, make([]byte, (512-len(b)%512)%512)...) }

// tgzSparseSegsTar is a tar holding one old-GNU sparse member with up to 4 stored segments
// (all in the header's own sparse map), stored one after the other, then the end blocks.
func tgzSparseSegsTar(name string, segs []tgzSeg, realSize int64) []byte {
	var stored []byte
	blk := make([]byte, 512)
	copy(blk[0:100], name)
	tgzOctal(blk[100:108], 0o644)
	tgzOctal(blk[108:116], 0)
	tgzOctal(blk[116:124], 0)
	for i, s := range segs {
		tgzOctal(blk[386+24*i:398+24*i], s.off)              // sparse entry i: offset
		tgzOctal(blk[398+24*i:410+24*i], int64(len(s.data))) // and length
		stored = append(stored, s.data...)
	}
	tgzOctal(blk[124:136], int64(len(stored))) // the bytes stored in the archive
	tgzOctal(blk[136:148], 1_000_000_000)
	blk[156] = tar.TypeGNUSparse
	copy(blk[257:265], "ustar  \x00")
	tgzOctal(blk[483:495], realSize) // the real size
	tgzChecksum(blk)
	out := append(blk, tgzPad(stored)...)
	return append(out, make([]byte, 1024)...) // the end-of-archive blocks
}

// tgzPAXRecord is one PAX record, "<length> <key>=<value>\n", the length counting itself.
func tgzPAXRecord(k, v string) string {
	rest := " " + k + "=" + v + "\n"
	n := len(rest) + 1
	for len(strconv.Itoa(n))+len(rest) != n {
		n = len(strconv.Itoa(n)) + len(rest)
	}
	return strconv.Itoa(n) + rest
}

// tgzUstarBlock is a ustar header block.
func tgzUstarBlock(name string, typ byte, size int64) []byte {
	blk := make([]byte, 512)
	copy(blk[0:100], name)
	tgzOctal(blk[100:108], 0o644)
	tgzOctal(blk[108:116], 0)
	tgzOctal(blk[116:124], 0)
	tgzOctal(blk[124:136], size)
	tgzOctal(blk[136:148], 1_000_000_000)
	blk[156] = typ
	copy(blk[257:263], "ustar\x00")
	copy(blk[263:265], "00")
	tgzChecksum(blk)
	return blk
}

// tgzSparsePAX10Tar is a tar holding one PAX 1.0 sparse member (what GNU tar writes with
// --sparse --format=posix): an 'x' header with GNU.sparse.major=1, minor=0, name and
// realsize, then a regular header whose data starts with the sparse map as text (padded to
// a block) followed by the stored segments, then the end blocks.
func tgzSparsePAX10Tar(name string, segs []tgzSeg, realSize int64) []byte {
	recs := tgzPAXRecord("GNU.sparse.major", "1") + tgzPAXRecord("GNU.sparse.minor", "0") +
		tgzPAXRecord("GNU.sparse.name", name) + tgzPAXRecord("GNU.sparse.realsize", strconv.FormatInt(realSize, 10))
	m := strconv.Itoa(len(segs)) + "\n"
	var stored []byte
	for _, s := range segs {
		m += strconv.FormatInt(s.off, 10) + "\n" + strconv.Itoa(len(s.data)) + "\n"
		stored = append(stored, s.data...)
	}
	data := append(tgzPad([]byte(m)), stored...)
	out := tgzUstarBlock("PaxHeaders.0/"+path.Base(name), tar.TypeXHeader, int64(len(recs)))
	out = append(out, tgzPad([]byte(recs))...)
	out = append(out, tgzUstarBlock("GNUSparseFile.0/"+path.Base(name), tar.TypeReg, int64(len(data)))...)
	out = append(out, tgzPad(data)...)
	return append(out, make([]byte, 1024)...) // the end-of-archive blocks
}

// TestMboxTarGzSparseMember: a GNU sparse member is read like a regular file (the reader
// returns its expanded data), not passed over.
func TestMboxTarGzSparseMember(t *testing.T) {
	regular := tgzTar(t, []tgzEntry{{name: "alt/test/.art1", body: tgzArticle("<r1@targz.test.invalid>", "alt.test")}})
	sparse := tgzSparseTar("alt/test/.art2", tgzArticle("<s2@targz.test.invalid>", "alt.test"))
	// the regular tar without its end-of-archive blocks, then the sparse one
	data := tgzGzip(t, append(append([]byte(nil), regular[:len(regular)-1024]...), sparse...), gzip.DefaultCompression)
	sink := &tgzSink{}
	c, err := tgzRead(t, "alt.test.tar.gz", data, &runOptions{}, sink, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	want := "<r1@targz.test.invalid> alt.test\n<s2@targz.test.invalid> alt.test"
	if got := sink.list(); got != want || c.Files != 2 {
		t.Errorf("files=%d articles:\n%s\nwant 2 files and:\n%s", c.Files, got, want)
	}
}

// TestMboxTarGzSparseEnd: a sparse last member whose stored data ends in zero blocks, cut
// without the end-of-archive blocks, is refused (its stored size is taken from reading it,
// not guessed from trailing zeros); with the end blocks it is accepted. A sparse member
// with a hole over the limit is an input error, not read.
func TestMboxTarGzSparseEnd(t *testing.T) {
	sparse := tgzSparseTar("alt/test/.db", strings.Repeat("\x00", 1536))
	for _, c := range []struct {
		name string
		tar  []byte
		ok   bool
	}{
		{"sparse last, end blocks", sparse, true},
		{"sparse last, no end blocks", sparse[:len(sparse)-1024], false},
	} {
		_, err := tgzRead(t, "alt.test.tar.gz", tgzGzip(t, c.tar, gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
		switch {
		case c.ok && err != nil:
			t.Errorf("%s: err=%v, want nil", c.name, err)
		case !c.ok && (err == nil || !strings.Contains(err.Error(), "end-of-archive")):
			t.Errorf("%s: err=%v, want tar ended without its end-of-archive blocks", c.name, err)
		}
	}

	huge := tgzSparseHoleTar("alt/test/.art1", tgzArticle("<h1@targz.test.invalid>", "alt.test"), 2<<30)
	sink := &tgzSink{}
	_, err := tgzRead(t, "alt.test.tar.gz", tgzGzip(t, huge, gzip.DefaultCompression), &runOptions{}, sink, nil)
	if err == nil || !strings.Contains(err.Error(), "sparse member") || sink.list() != "" {
		t.Errorf("sparse member with a 2 GiB hole: err=%v articles %q, want an input error and nothing read", err, sink.list())
	}
}

// TestMboxTarGzPAXHeaders: a tar cut right after a PAX extended header (no member follows)
// is refused; an archive of only a global PAX header and the end blocks is a complete tar
// (it has no articles, which importSpool reports later).
func TestMboxTarGzPAXHeaders(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := tgzArticle("<x1@targz.test.invalid>", "alt.test")
	if err := tw.WriteHeader(&tar.Header{Name: "alt/test/.art1", Typeflag: tar.TypeReg, Mode: 0o644,
		Size: int64(len(body)), Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "pax"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	// the 'x' header and its records (under 512 bytes) come first, the member's header at 1024
	if raw[156] != tar.TypeXHeader || raw[1024+156] != tar.TypeReg {
		t.Fatalf("unexpected layout: types %q and %q", raw[156], raw[1024+156])
	}
	_, err := tgzRead(t, "alt.test.tar.gz", tgzGzip(t, raw[:1024], gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
	if err == nil {
		t.Error("cut after the PAX header: no error, want an input error")
	}
	c, err := tgzRead(t, "alt.test.tar.gz", tgzGzip(t, raw, gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
	if err != nil || c.Read != 1 {
		t.Errorf("whole PAX tar: err=%v read=%d, want nil/1", err, c.Read)
	}

	buf.Reset()
	tw = tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
		PAXRecords: map[string]string{"comment": "only a global header"}}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = tgzRead(t, "alt.test.tar.gz", tgzGzip(t, buf.Bytes(), gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
	if err != nil || c.Files != 0 {
		t.Errorf("global header only: err=%v files=%d, want nil/0", err, c.Files)
	}
	g := buf.Bytes()
	_, err = tgzRead(t, "alt.test.tar.gz", tgzGzip(t, g[:len(g)-1024], gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
	if err == nil || !strings.Contains(err.Error(), "end-of-archive") {
		t.Errorf("global header, no end blocks: err=%v, want tar ended without its end-of-archive blocks", err)
	}

	// gzip of nothing: no tar at all
	_, err = tgzRead(t, "alt.test.tar.gz", tgzGzip(t, nil, gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
	if err == nil {
		t.Error("empty gzip stream: no error")
	}
}

// tgzBinSegs are three stored segments of a binary (non-article) sparse file with holes
// between them, about 21 KiB stored in all, so most of it lies past the walker's 4096-byte
// classification read and is left for the drain; the last segment ends in zero blocks.
func tgzBinSegs() ([]tgzSeg, int64) {
	bin := strings.Repeat("\x01\xfe\x7f\x80", 1750) // 7000 bytes
	return []tgzSeg{
		{0, bin},
		{16384, bin},
		{32768, strings.Repeat("\xff", 3000) + strings.Repeat("\x00", 4096)},
	}, 40000
}

// TestMboxTarGzSparseDrain: a sparse non-article member whose stored data is much larger
// than the walker's classification read, last in the archive, in the old-GNU and in the
// PAX 1.0 format: with the end blocks it is accepted and counted as not-article; cut
// without them it is refused (the drain reads the stored data the walker left, and the data
// end comes from that, not from trailing zeros).
func TestMboxTarGzSparseDrain(t *testing.T) {
	segs, realSize := tgzBinSegs()
	for _, f := range []struct {
		name string
		tar  []byte
	}{
		{"old GNU", tgzSparseSegsTar("alt/test/bin", segs, realSize)},
		{"PAX 1.0", tgzSparsePAX10Tar("alt/test/bin", segs, realSize)},
	} {
		// the fixture as archive/tar sees it: one sparse member, expanded to its real size
		tr := tar.NewReader(bytes.NewReader(f.tar))
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("%s: fixture: %v", f.name, err)
		}
		n, err := io.Copy(io.Discard, tr)
		if err != nil || hdr.Name != "alt/test/bin" || hdr.Size != realSize || n != realSize {
			t.Fatalf("%s: fixture: name=%q size=%d expanded=%d err=%v, want a sparse alt/test/bin of %d bytes",
				f.name, hdr.Name, hdr.Size, n, err, realSize)
		}

		c, err := tgzRead(t, "alt.test.tar.gz", tgzGzip(t, f.tar, gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
		if err != nil || c.Files != 1 || c.NotArticle != 1 || c.FileErrors != 0 {
			t.Errorf("%s, end blocks: err=%v files=%d not-article=%d file-errors=%d, want nil/1/1/0",
				f.name, err, c.Files, c.NotArticle, c.FileErrors)
		}
		cut := f.tar[:len(f.tar)-1024]
		_, err = tgzRead(t, "alt.test.tar.gz", tgzGzip(t, cut, gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
		if err == nil || !strings.Contains(err.Error(), "end-of-archive") {
			t.Errorf("%s, no end blocks: err=%v, want tar ended without its end-of-archive blocks", f.name, err)
		}
	}
}

// TestMboxTarGzGlobalHeaderNoPadding: a global PAX header whose data is exactly one block
// (no padding), last in the archive: accepted with the end blocks, refused when the stream
// ends right after its data.
func TestMboxTarGzGlobalHeaderNoPadding(t *testing.T) {
	value := strings.Repeat("v", 499) // "512 comment=" + 499 + "\n" = 512 bytes
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
		PAXRecords: map[string]string{"comment": value}}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	g := buf.Bytes()
	if len(g) != 512+512+1024 || g[156] != tar.TypeXGlobalHeader {
		t.Fatalf("unexpected layout: %d bytes, type %q", len(g), g[156])
	}
	c, err := tgzRead(t, "alt.tar.gz", tgzGzip(t, g, gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
	if err != nil || c.Files != 0 {
		t.Errorf("end blocks: err=%v files=%d, want nil/0", err, c.Files)
	}
	_, err = tgzRead(t, "alt.tar.gz", tgzGzip(t, g[:1024], gzip.DefaultCompression), &runOptions{}, &tgzSink{}, nil)
	if err == nil || !strings.Contains(err.Error(), "end-of-archive") {
		t.Errorf("cut after its data: err=%v, want tar ended without its end-of-archive blocks", err)
	}
}

// TestMboxTarGzMemberTypes pins which member types are read as files.
func TestMboxTarGzMemberTypes(t *testing.T) {
	for _, c := range []struct {
		typ  byte
		file bool
	}{
		{tar.TypeReg, true}, {'\x00', true}, {tar.TypeGNUSparse, true},
		{tar.TypeDir, false}, {tar.TypeSymlink, false}, {tar.TypeLink, false}, {tar.TypeChar, false},
		{tar.TypeBlock, false}, {tar.TypeFifo, false}, {tar.TypeXHeader, false}, {tar.TypeGNULongName, false},
	} {
		if got := tarIsFile(c.typ); got != c.file {
			t.Errorf("tarIsFile(%q) = %v, want %v", c.typ, got, c.file)
		}
	}
}
