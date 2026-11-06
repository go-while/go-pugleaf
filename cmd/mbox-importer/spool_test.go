package main

// Tests of the news spool walk (plan mbox-import-discs): the order a tree is read in, the
// classification of its files by content, the same tree as a zip, and the stop channel.

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// spoolSink keeps every article by message-id and the order they arrived in. onHandle runs
// after an article was taken (a test closes the stop channel there).
type spoolSink struct {
	mu       sync.Mutex
	ids      []string
	arts     map[string]*models.Article
	onHandle func()
}

func newSpoolSink() *spoolSink { return &spoolSink{arts: make(map[string]*models.Article)} }

func (s *spoolSink) Handle(msgid string, art func() (*models.Article, error), _ string) (bool, bool, error) {
	a, err := art()
	if err != nil {
		return false, false, err
	}
	s.mu.Lock()
	s.ids = append(s.ids, msgid)
	s.arts[msgid] = a
	s.mu.Unlock()
	if s.onHandle != nil {
		s.onHandle()
	}
	return true, false, nil
}

func (s *spoolSink) beginInput() inputSink { return s }

func (s *spoolSink) endInput(time.Duration) error { return nil }

// spoolArticleText is one article file of a spool, with full RFC 1036 headers.
func spoolArticleText(msgid, group, subject string) string {
	return "Path: utzoo!decvax!ucbvax!mhtsa!research!alice!ark\n" +
		"From: ark@alice.example.invalid (A U Thor)\n" +
		"Newsgroups: " + group + "\n" +
		"Subject: " + subject + "\n" +
		"Message-ID: " + msgid + "\n" +
		"Date: Mon, 23 Nov 1981 23:44:52 +0000\n" +
		"\n" +
		"body line one\nbody line two\n"
}

// spoolMboxText is an mbox file of two articles (the DOUG.* floppies have no suffix).
func spoolMboxText(first, second string) string {
	return syArticle(first, nil, "floppy body one") + syArticle(second, nil, "floppy body two")
}

// spoolANewsText is the utzoo A-news batch of the plan (news001f1/a13).
const spoolANewsText = "Aalice.154\n" +
	"net.games\n" +
	"utzoo!decvax!ucbvax!mhtsa!research!alice!ark\n" +
	"Mon Nov 23 23:44:52 1981\n" +
	"Re: Quitting\n" +
	"A body line of the A-news article.\n"

// spoolWrite writes one file of a test tree, creating its directories.
func spoolWrite(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", name, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", name, err)
	}
}

// spoolTestTree builds a spool with one article per file: numeric article names, an .XRF
// pointer, an .overview, a binary, a README, a stripped comp.sources posting, an mbox
// file and a CRLF article in nested directories.
func spoolTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	spoolWrite(t, root, "README", "This CD-ROM holds a NetNews spool.\n")
	spoolWrite(t, root, "SPOOL/comp/lang/c/1", spoolArticleText("<c1@example.invalid>", "comp.lang.c", "first"))
	spoolWrite(t, root, "SPOOL/comp/lang/c/2", spoolArticleText("<c2@example.invalid>", "comp.lang.c", "second"))
	spoolWrite(t, root, "SPOOL/comp/lang/c/9", spoolArticleText("<c9@example.invalid>", "comp.lang.c", "ninth"))
	spoolWrite(t, root, "SPOOL/comp/lang/c/10", spoolArticleText("<c10@example.invalid>", "comp.lang.c", "tenth"))
	spoolWrite(t, root, "SPOOL/comp/lang/c/9.XRF", "alt/cad/autocad/1005\n")
	spoolWrite(t, root, "SPOOL/comp/lang/c/.overview", "1\tRe: first\tark\t23 Nov 81\t<c1@example.invalid>\n")
	spoolWrite(t, root, "SPOOL/alt/binaries/pic", "\x00\x01\x02GIF89a\x00\xff\xfe binary rubbish\n")
	spoolWrite(t, root, "SPOOL/DOUG.PRO", spoolMboxText("<doug1@example.invalid>", "<doug2@example.invalid>"))
	spoolWrite(t, root, "SPOOL/crlf/1", strings.ReplaceAll(
		spoolArticleText("<crlf@example.invalid>", "comp.lang.c", "dos copy"), "\n", "\r\n"))
	// a stripped comp.sources posting: no Message-ID at all
	spoolWrite(t, root, "SPOOL/sources/unix/volume1/pkg/part01",
		"Subject: v01i001: pkg - part01 of 01\nNewsgroups: comp.sources.unix\nApproved: rs@example.invalid\n\nshar body\n")
	return root
}

// spoolWantIDs is the order readSpool hands the articles of spoolTestTree over in:
// directories and files in order (DOUG.PRO before alt, comp, crlf, sources), all-digit
// names numerically, so 10 comes after 9.
var spoolWantIDs = []string{
	"<doug1@example.invalid>", "<doug2@example.invalid>",
	"<c1@example.invalid>", "<c2@example.invalid>", "<c9@example.invalid>", "<c10@example.invalid>",
	"<crlf@example.invalid>",
}

// spoolWantCounts checks the counters of a walk over spoolTestTree.
func spoolWantCounts(t *testing.T, what string, c *counters) {
	t.Helper()
	// 11 files: README, 1, 2, 9, 10, 9.XRF, .overview, pic, DOUG.PRO, crlf/1, part01
	if c.Files != 11 {
		t.Errorf("%s: Files=%d, want 11", what, c.Files)
	}
	if c.NotArticle != 3 { // README, .overview, pic
		t.Errorf("%s: NotArticle=%d, want 3", what, c.NotArticle)
	}
	if c.XrefPointers != 1 {
		t.Errorf("%s: XrefPointers=%d, want 1", what, c.XrefPointers)
	}
	if c.ANewsBatches != 0 {
		t.Errorf("%s: ANewsBatches=%d, want 0", what, c.ANewsBatches)
	}
	if c.Read != 8 { // 5 articles, 2 mbox messages, 1 stripped posting
		t.Errorf("%s: Read=%d, want 8", what, c.Read)
	}
	if c.Stored != 7 {
		t.Errorf("%s: Stored=%d, want 7", what, c.Stored)
	}
	if c.Skipped[skipNoMessageID] != 1 || c.skippedTotal() != 1 {
		t.Errorf("%s: skipped %s, want no-msgid=1", what, c.skippedDetail())
	}
	if c.Errors != 0 || c.FileErrors != 0 || c.Rejected != 0 || c.ParserSkipped != 0 || c.StoppedEarly {
		t.Errorf("%s: errors=%d file-errors=%d rejected=%d parser-skipped=%d stopped-early=%t",
			what, c.Errors, c.FileErrors, c.Rejected, c.ParserSkipped, c.StoppedEarly)
	}
}

func TestMboxSpoolWalkTree(t *testing.T) {
	root := spoolTestTree(t)
	sink := newSpoolSink()
	c, err := readSpool(os.DirFS(root), root, &runOptions{}, sink, nil)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	spoolWantCounts(t, "tree", c)
	if strings.Join(sink.ids, " ") != strings.Join(spoolWantIDs, " ") {
		t.Errorf("articles in order\n got %q\nwant %q", sink.ids, spoolWantIDs)
	}
	if c.File != root {
		t.Errorf("counters.File=%q, want %q", c.File, root)
	}
	// the CRLF copy is stored without its CRs, header and body
	art := sink.arts["<crlf@example.invalid>"]
	if art == nil {
		t.Fatalf("the CRLF article was not stored")
	}
	if strings.Contains(art.BodyText, "\r") || strings.Contains(art.HeadersJSON, "\r") {
		t.Errorf("CRLF article kept a CR: headers %q body %q", art.HeadersJSON, art.BodyText)
	}
	if art.Subject != "dos copy" || art.BodyText != "body line one\nbody line two" {
		t.Errorf("CRLF article: subject %q body %q", art.Subject, art.BodyText)
	}
}

// spoolZip packs every regular file of root into a zip and returns it as an fs.FS, the
// way a ".zip" spool (NN_1992_25.zip) is read.
func spoolZip(t *testing.T, root string) fs.FS {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		t.Fatalf("pack zip: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	return zr
}

func TestMboxSpoolWalkZip(t *testing.T) {
	root := spoolTestTree(t)
	sink := newSpoolSink()
	c, err := readSpool(spoolZip(t, root), "tree.zip", &runOptions{}, sink, nil)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	spoolWantCounts(t, "zip", c)
	if strings.Join(sink.ids, " ") != strings.Join(spoolWantIDs, " ") {
		t.Errorf("zip articles in order\n got %q\nwant %q", sink.ids, spoolWantIDs)
	}
}

func TestMboxSpoolStopBeforeWalk(t *testing.T) {
	root := spoolTestTree(t)
	stop := make(chan struct{})
	close(stop)
	sink := newSpoolSink()
	c, err := readSpool(os.DirFS(root), root, &runOptions{}, sink, stop)
	if err != errStopped {
		t.Fatalf("readSpool: %v, want errStopped", err)
	}
	if c.Files != 0 || c.Read != 0 || len(sink.ids) != 0 {
		t.Errorf("files=%d read=%d articles=%d, want nothing read", c.Files, c.Read, len(sink.ids))
	}
	if c.StoppedEarly {
		t.Error("StoppedEarly is for a stuck parser, not for the stop channel")
	}
}

// TestMboxSpoolStopInsideMbox closes the stop channel from the sink, after the first
// article: the walk ends between two articles of the mbox file inside the spool.
func TestMboxSpoolStopInsideMbox(t *testing.T) {
	root := spoolTestTree(t)
	stop := make(chan struct{})
	sink := newSpoolSink()
	var once sync.Once
	sink.onHandle = func() { once.Do(func() { close(stop) }) }
	c, err := readSpool(os.DirFS(root), root, &runOptions{}, sink, stop)
	if err != errStopped {
		t.Fatalf("readSpool: %v, want errStopped", err)
	}
	if len(sink.ids) != 1 || sink.ids[0] != spoolWantIDs[0] {
		t.Errorf("articles %q, want only %q", sink.ids, spoolWantIDs[0])
	}
	if c.Read != 1 || c.Stored != 1 {
		t.Errorf("read=%d stored=%d, want 1 1", c.Read, c.Stored)
	}
	if c.Files != 2 { // README and DOUG.PRO were walked
		t.Errorf("files=%d, want 2", c.Files)
	}
}

func TestMboxSpoolClassify(t *testing.T) {
	cases := []struct {
		name string
		text string
		want spoolClass
	}{
		{"article", spoolArticleText("<a@b.example>", "net.games", "s"), spoolArticle},
		{"article-crlf", strings.ReplaceAll(spoolArticleText("<a@b.example>", "net.games", "s"), "\n", "\r\n"), spoolArticle},
		{"article-xref-first", "Xref: utzoo net.games:154\nPath: utzoo!ark\nSubject: s\n\nbody\n", spoolArticle},
		{"article-id-only", "Relay-Version: B 2.10\nArticle-I.D.: alice.154\nSubject: s\n\nbody\n", spoolArticle},
		{"stripped-posting", "Subject: v01i001: pkg\nNewsgroups: comp.sources.unix\n\nshar\n", spoolArticle},
		{"mbox", spoolMboxText("<m1@b.example>", "<m2@b.example>"), spoolMboxFile},
		{"mbox-rnews", "#! rnews 123\nPath: x\nSubject: s\n\nbody\n", spoolMboxFile},
		{"anews", spoolANewsText, spoolANewsBatch},
		{"anews-bad-date", "Aalice.154\nnet.games\nutzoo!ark\nnot a date\nRe: Quitting\nbody\n", spoolNotArticle},
		{"anews-bad-id", "Balice.154\nnet.games\nutzoo!ark\nMon Nov 23 23:44:52 1981\ns\nbody\n", spoolNotArticle},
		{"readme", "This CD-ROM holds a NetNews spool.\n", spoolNotArticle},
		{"binary", "\x00\x01\x02GIF89a\x00\xff\xfe\n", spoolNotArticle},
		{"overview", "1\tRe: first\tark\t23 Nov 81\t<c1@b.example>\n", spoolNotArticle},
		{"ymtrans", "YMTRANS.TBL: nothing\n", spoolNotArticle}, // a field name with a dot but no known field
		{"continuation-first", " Subject: folded first\n\nbody\n", spoolNotArticle},
		{"no-known-field", "Lines: 3\nContent-Type: text/plain\n\nbody\n", spoolNotArticle},
		{"empty", "", spoolNotArticle},
	}
	for _, tc := range cases {
		// the whole file fits in the peek, so it is not truncated
		if got := classifySpoolFile([]byte(tc.text), false); got != tc.want {
			t.Errorf("%s: class %d, want %d", tc.name, got, tc.want)
		}
	}
	// a header block the peek cut in the middle is an article candidate, with or without
	// one of the known fields in what was read
	cut := []byte("References: " + strings.Repeat("<ref@example.invalid> ", 200))
	if got := classifySpoolFile(cut, true); got != spoolArticle {
		t.Errorf("cut-off header block: class %d, want %d", got, spoolArticle)
	}
	if got := classifySpoolFile(cut, false); got != spoolNotArticle {
		t.Errorf("same bytes as a whole file: class %d, want %d", got, spoolNotArticle)
	}
	for _, name := range []string{"SPOOL/net/games/1005.XRF", "1005.xrf", "1005.XRF;1", "1005.xrf;12"} {
		if !isXrefPointerName(name) {
			t.Errorf("%q was not recognised as an .XRF pointer", name)
		}
	}
	for _, name := range []string{"SPOOL/net/games/1005", "1005;1", "xrf", "notes.XRFX"} {
		if isXrefPointerName(name) {
			t.Errorf("%q was taken for an .XRF pointer", name)
		}
	}
}

func TestMboxSpoolNameOrder(t *testing.T) {
	names := []string{"README", "10", "9", "5.XRF", ".overview", "007", "1992", "alt"}
	sort.SliceStable(names, func(i, j int) bool { return spoolNameLess(names[i], names[j]) })
	want := "007 9 10 1992 .overview 5.XRF README alt"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("order %q, want %q", got, want)
	}
	// the comparison must be a total order: sorting the reverse gives the same list
	rev := []string{"alt", "README", "5.XRF", ".overview", "1992", "10", "9", "007"}
	sort.SliceStable(rev, func(i, j int) bool { return spoolNameLess(rev[i], rev[j]) })
	if got := strings.Join(rev, " "); got != want {
		t.Errorf("reverse input: order %q, want %q", got, want)
	}
}

// TestMboxSpoolANewsBatch reads a tree with one A-news batch. parseANews belongs to
// another slice: while it is a stub that emits nothing the batch counts as not-article,
// and once it parses the batch it counts as an A-news batch with its articles read.
func TestMboxSpoolANewsBatch(t *testing.T) {
	emitted := 0
	perr := parseANews(strings.NewReader(spoolANewsText), func(_ []string, body io.Reader) {
		emitted++
		_, _ = io.Copy(io.Discard, body)
	})
	root := t.TempDir()
	spoolWrite(t, root, "news001f1/a13", spoolANewsText)
	sink := newSpoolSink()
	c, err := readSpool(os.DirFS(root), root, &runOptions{}, sink, nil)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	if c.Files != 1 {
		t.Errorf("Files=%d, want 1", c.Files)
	}
	if emitted == 0 {
		t.Logf("parseANews is still a stub (%v): the batch counts as not-article", perr)
		if c.ANewsBatches != 0 || c.NotArticle != 1 || c.Read != 0 {
			t.Errorf("stub: anews-batches=%d not-article=%d read=%d, want 0 1 0",
				c.ANewsBatches, c.NotArticle, c.Read)
		}
		return
	}
	if c.ANewsBatches != 1 || c.NotArticle != 0 || c.Read != emitted {
		t.Errorf("anews-batches=%d not-article=%d read=%d, want 1 0 %d",
			c.ANewsBatches, c.NotArticle, c.Read, emitted)
	}
}

// TestMboxSpoolStopBetweenFiles closes the stop channel from the sink, after the article
// of the first file: the walk ends between two files.
func TestMboxSpoolStopBetweenFiles(t *testing.T) {
	root := t.TempDir()
	spoolWrite(t, root, "net/games/1", spoolArticleText("<f1@example.invalid>", "net.games", "first"))
	spoolWrite(t, root, "net/games/2", spoolArticleText("<f2@example.invalid>", "net.games", "second"))
	stop := make(chan struct{})
	sink := newSpoolSink()
	var once sync.Once
	sink.onHandle = func() { once.Do(func() { close(stop) }) }
	c, err := readSpool(os.DirFS(root), root, &runOptions{}, sink, stop)
	if err != errStopped {
		t.Fatalf("readSpool: %v, want errStopped", err)
	}
	if c.Files != 1 || c.Read != 1 || c.Stored != 1 {
		t.Errorf("files=%d read=%d stored=%d, want 1 1 1", c.Files, c.Read, c.Stored)
	}
	if len(sink.ids) != 1 || sink.ids[0] != "<f1@example.invalid>" {
		t.Errorf("articles %q, want only the first", sink.ids)
	}
}

// TestMboxSpoolUnreadableFile counts a file that cannot be opened in FileErrors: the walk
// carries on and the input is still imported as far as it could be read.
func TestMboxSpoolUnreadableFile(t *testing.T) {
	root := t.TempDir()
	spoolWrite(t, root, "net/games/1", spoolArticleText("<g1@example.invalid>", "net.games", "first"))
	spoolWrite(t, root, "net/games/2", spoolArticleText("<g2@example.invalid>", "net.games", "second"))
	locked := filepath.Join(root, "net", "games", "2")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if f, err := os.Open(locked); err == nil {
		_ = f.Close()
		t.Skip("this user can read a mode-0 file (running as root?)")
	}
	sink := newSpoolSink()
	c, err := readSpool(os.DirFS(root), root, &runOptions{}, sink, nil)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	if c.Files != 2 || c.FileErrors != 1 || c.Read != 1 || c.Stored != 1 {
		t.Errorf("files=%d file-errors=%d read=%d stored=%d, want 2 1 1 1",
			c.Files, c.FileErrors, c.Read, c.Stored)
	}
	if c.Errors != 0 || c.StoppedEarly {
		t.Errorf("errors=%d stopped-early=%t: an unreadable file is no article error",
			c.Errors, c.StoppedEarly)
	}
	if len(sink.ids) != 1 || sink.ids[0] != "<g1@example.invalid>" {
		t.Errorf("articles %q, want the readable one", sink.ids)
	}
	if !strings.Contains(c.spoolDetail(), "file-errors=1") {
		t.Errorf("the log line does not show the unreadable file: %q", c.spoolDetail())
	}
}

// spoolBadFS serves one file that reads head bytes and then fails: a scratched disc.
type spoolBadFS struct{ name, head string }

func (s spoolBadFS) ReadDir(dir string) ([]fs.DirEntry, error) {
	if dir != "." {
		return nil, fs.ErrNotExist
	}
	return []fs.DirEntry{spoolBadEntry(s.name)}, nil
}

func (s spoolBadFS) Open(name string) (fs.File, error) {
	if name != s.name {
		return nil, fs.ErrNotExist
	}
	return &spoolBadFile{head: s.head}, nil
}

type spoolBadEntry string

func (e spoolBadEntry) Name() string               { return string(e) }
func (e spoolBadEntry) IsDir() bool                { return false }
func (e spoolBadEntry) Type() fs.FileMode          { return 0 }
func (e spoolBadEntry) Info() (fs.FileInfo, error) { return nil, errSpoolTestRead }

type spoolBadFile struct {
	head string
	n    int
}

func (f *spoolBadFile) Stat() (fs.FileInfo, error) { return nil, errSpoolTestRead }
func (f *spoolBadFile) Close() error               { return nil }

func (f *spoolBadFile) Read(p []byte) (int, error) {
	if f.n >= len(f.head) {
		return 0, errSpoolTestRead
	}
	n := copy(p, f.head[f.n:])
	f.n += n
	return n, nil
}

var errSpoolTestRead = errors.New("scratched disc")

// TestMboxSpoolReadErrorMidFile: a one-article file whose read fails after the peek is
// never handed over half-read, and counts in FileErrors.
func TestMboxSpoolReadErrorMidFile(t *testing.T) {
	head := spoolArticleText("<bad@example.invalid>", "net.games", "cut short") +
		strings.Repeat("a body line of the article\n", 200) // past the 4 KiB peek
	if len(head) <= spoolPeekSize {
		t.Fatalf("the test file is only %d bytes: it must be longer than the peek", len(head))
	}
	sink := newSpoolSink()
	c, err := readSpool(spoolBadFS{name: "1", head: head}, "scratched.iso", &runOptions{}, sink, nil)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	if c.Files != 1 || c.FileErrors != 1 || c.Read != 0 || c.Errors != 0 || c.NotArticle != 0 {
		t.Errorf("files=%d file-errors=%d read=%d errors=%d not-article=%d, want 1 1 0 0 0",
			c.Files, c.FileErrors, c.Read, c.Errors, c.NotArticle)
	}
	if len(sink.ids) != 0 {
		t.Errorf("stored %q, want nothing: the article was cut short", sink.ids)
	}
}

// TestMboxSpoolReadErrorMbox: a read error inside an mbox file of the spool is recorded by
// the reader wrapped around it and counted in FileErrors once the file is done.
func TestMboxSpoolReadErrorMbox(t *testing.T) {
	head := syArticle("<mb1@example.invalid>", nil, strings.Repeat("a body line\n", 500))
	if len(head) <= spoolPeekSize {
		t.Fatalf("the test file is only %d bytes: it must be longer than the peek", len(head))
	}
	sink := newSpoolSink()
	c, err := readSpool(spoolBadFS{name: "DOUG.PRO", head: head}, "floppy.img", &runOptions{}, sink, nil)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	if c.Files != 1 || c.FileErrors != 1 || c.Errors != 0 {
		t.Errorf("files=%d file-errors=%d errors=%d, want 1 1 0", c.Files, c.FileErrors, c.Errors)
	}
	// the mbox parser turns the read error into io.EOF, so the message it was in the
	// middle of is handed over as if the file ended there; FileErrors is what says the
	// file was damaged (an mbox file is streamed, not read whole like a one-article file)
	if c.Read != 1 {
		t.Errorf("read=%d, want the message the error cut short", c.Read)
	}
}

// TestMboxSpoolTooBigArticle: with -max-article-size the file is read spoolSizeSlack bytes
// past the cap and counted as too-big, not stored.
func TestMboxSpoolTooBigArticle(t *testing.T) {
	root := t.TempDir()
	spoolWrite(t, root, "net/games/1", spoolArticleText("<small@example.invalid>", "net.games", "small"))
	spoolWrite(t, root, "net/games/2", spoolArticleText("<big@example.invalid>", "net.games", "big")+
		strings.Repeat("filler line of the body\n", 500))
	sink := newSpoolSink()
	c, err := readSpool(os.DirFS(root), root, &runOptions{maxArticleSize: 4096}, sink, nil)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	if c.Read != 2 || c.Stored != 1 || c.Skipped[skipTooBig] != 1 || c.FileErrors != 0 {
		t.Errorf("read=%d stored=%d too-big=%d file-errors=%d, want 2 1 1 0",
			c.Read, c.Stored, c.Skipped[skipTooBig], c.FileErrors)
	}
	if len(sink.ids) != 1 || sink.ids[0] != "<small@example.invalid>" {
		t.Errorf("stored %q, want only the small article", sink.ids)
	}
}

// TestMboxSpoolSymlinks walks past symlinks without following them: a crosspost link
// would import the same article twice.
func TestMboxSpoolSymlinks(t *testing.T) {
	root := t.TempDir()
	spoolWrite(t, root, "net/games/1", spoolArticleText("<s1@example.invalid>", "net.games", "first"))
	if err := os.Symlink(filepath.Join(root, "net", "games", "1"), filepath.Join(root, "net", "games", "2")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "net", "games"), filepath.Join(root, "games")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	sink := newSpoolSink()
	c, err := readSpool(os.DirFS(root), root, &runOptions{}, sink, nil)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	if c.Files != 1 || c.Read != 1 || c.NotArticle != 0 || c.Errors != 0 {
		t.Errorf("files=%d read=%d not-article=%d errors=%d, want 1 1 0 0",
			c.Files, c.Read, c.NotArticle, c.Errors)
	}
	if len(sink.ids) != 1 {
		t.Errorf("articles %q, want one", sink.ids)
	}
}

// TestMboxSpoolOversizeHeader: a file whose first line looks like a header field but whose
// "header block" never ends is no article.
func TestMboxSpoolOversizeHeader(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("Subject: a text file that starts like an article\n")
	b.WriteString("Newsgroups: net.games\n")
	for b.Len() <= spoolMaxHeaderBytes {
		b.WriteString("X-Filler: no empty line in this file at all\n")
	}
	spoolWrite(t, root, "doc/manual", b.String())
	sink := newSpoolSink()
	c, err := readSpool(os.DirFS(root), root, &runOptions{}, sink, nil)
	if err != nil {
		t.Fatalf("readSpool: %v", err)
	}
	if c.Files != 1 || c.NotArticle != 1 || c.Read != 0 || c.Errors != 0 {
		t.Errorf("files=%d not-article=%d read=%d errors=%d, want 1 1 0 0",
			c.Files, c.NotArticle, c.Read, c.Errors)
	}
}

// TestMboxSpoolSplitArticle splits a single-article file: the CR before the LF is dropped,
// the empty line ends the header block, and a block over maxHeaderLines lines is handed
// over long enough for rewriteHead to skip it.
func TestMboxSpoolSplitArticle(t *testing.T) {
	head, oversize, body := spoolSplitArticle([]byte("Subject: s\r\nFrom: a\r\n\r\nbody one\r\nbody two\r\n"))
	if oversize || strings.Join(head, "|") != "Subject: s|From: a" {
		t.Errorf("CRLF block: %q oversize=%t", head, oversize)
	}
	if string(body) != "body one\r\nbody two\r\n" { // readBody drops the CRs
		t.Errorf("CRLF body %q", body)
	}
	// a file with a header block and no body (no empty line at all)
	head, oversize, body = spoolSplitArticle([]byte("Subject: s\nFrom: a"))
	if oversize || strings.Join(head, "|") != "Subject: s|From: a" || len(body) != 0 {
		t.Errorf("no body: %q oversize=%t body %q", head, oversize, body)
	}
	var b strings.Builder
	for i := 0; i < maxHeaderLines+10; i++ {
		b.WriteString("X-Filler: line\n")
	}
	b.WriteString("\nbody\n")
	head, oversize, body = spoolSplitArticle([]byte(b.String()))
	if oversize || len(head) != maxHeaderLines+1 || string(body) != "body\n" {
		t.Errorf("long block: %d lines oversize=%t body %q", len(head), oversize, body)
	}
	if _, reason := rewriteHead("<a@b.example>", head); reason != skipTooManyHeaders {
		t.Errorf("long block: rewriteHead reason %v, want too-many-headers", reason)
	}
	// no empty line within spoolMaxHeaderBytes: no article
	long := make([]byte, 0, spoolMaxHeaderBytes+64)
	for len(long) <= spoolMaxHeaderBytes {
		long = append(long, "X-Filler: no empty line at all\n"...)
	}
	if head, oversize, _ = spoolSplitArticle(long); !oversize || head != nil {
		t.Errorf("oversize block: oversize=%t %d lines", oversize, len(head))
	}
}
