package main

// News spool inputs (plan mbox-import-discs): a directory tree, a zip file or an unpacked
// disc image with one article per file (NetNews/CD, NetNews Offline, utzoo, InfoMagic CDs).

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"log"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	mbox "github.com/go-while/go-lib-mbox2nntp"
)

// spoolPeekSize is how much of a file is read to classify it (the first lines are enough:
// a separator, an A-news header or the start of a header block).
const spoolPeekSize = 4096

// spoolMaxHeaderBytes bounds the header block of a one-article file: a file classified as
// an article whose header block does not end within this many bytes is no article after
// all (a text file whose first line happens to look like a header field), and is counted
// as not-article instead of being held in memory whole.
const spoolMaxHeaderBytes = 1 << 20

// spoolReadBufferSize is the read buffer over a single-article file.
const spoolReadBufferSize = 64 * 1024

// spoolANewsID matches the first line of an A-news batch, "A<site>.<n>" (utzoo 1981-83).
var spoolANewsID = regexp.MustCompile(`^A[A-Za-z0-9_.-]+\.[0-9]+$`)

// spoolCtimeLayouts are the date forms line 4 of an A-news header may have:
// "Mon Nov 23 23:44:52 1981", with or without a zone name.
var spoolCtimeLayouts = []string{
	"Mon Jan _2 15:04:05 2006",
	"Mon Jan _2 15:04:05 2006 MST",
	"Mon Jan _2 15:04:05 MST 2006",
}

// spoolClass is what a file of a spool holds (decided by its content, not by its name,
// apart from the ".XRF" pointers).
type spoolClass int

const (
	spoolNotArticle  spoolClass = iota // a binary, an archive, .overview, a README, ...
	spoolArticle                       // one article with an RFC 1036/5322 header block
	spoolMboxFile                      // an mbox file (the DOUG.* floppies)
	spoolANewsBatch                    // an A-news batch: several articles, no header names
	spoolXrefPointer                   // a NetNews/CD "<n>.XRF" crosspost pointer
)

// spoolWalker is the state of one spool walk: what it reads, where it reports and the
// peek buffer, which is reused for every file (a disc holds millions of them).
type spoolWalker struct {
	fsys  fs.FS
	label string // the input as the log names it (the image or directory)
	opts  *runOptions
	sink  inputSink
	stop  <-chan struct{}
	c     *counters
	buf   []byte // spoolPeekSize bytes, reused per file
}

// readSpool walks fsys (os.DirFS of a directory or an unpacked image, or a *zip.Reader) and
// feeds every article it finds to sink: one-article files through handleMessage, mbox files
// through the mbox reader, A-news batches through parseANews. label names the input in the
// log. It returns errStopped (with the counters so far) when stop was closed between two
// articles or files.
//
// Directories are walked in order, all-digit file names (article numbers) in numeric order,
// so a group is read oldest first. Symlinks are not followed and count nowhere. Every
// regular file walked counts in Files; a file that is not an article, an mbox or an A-news
// batch counts in NotArticle, a "<n>.XRF" pointer in XrefPointers, and a batch that yielded
// at least one article in ANewsBatches.
//
// A file (or directory) that cannot be opened or read is counted as an article error
// (Errors) and logged: the walk goes on, but the input is not marked done and the run exits
// non-zero, so a damaged image is never written off as imported. StoppedEarly is not set
// for it; only a stuck mbox parser inside the spool sets that, as it does for an mbox input.
func readSpool(fsys fs.FS, label string, opts *runOptions, sink inputSink, stop <-chan struct{}) (*counters, error) {
	w := &spoolWalker{
		fsys:  fsys,
		label: label,
		opts:  opts,
		sink:  sink,
		stop:  stop,
		c:     newCounters(label),
		buf:   make([]byte, spoolPeekSize),
	}
	if w.stopped() {
		return w.c, errStopped
	}
	return w.c, w.walkDir(".")
}

// stopped reports whether the shutdown signal has arrived.
func (w *spoolWalker) stopped() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

// walkDir reads one directory of the spool and its subdirectories. A directory that cannot
// be read is counted and logged, and the walk carries on with the rest; only errStopped
// ends the walk.
func (w *spoolWalker) walkDir(dir string) error {
	entries, err := fs.ReadDir(w.fsys, dir)
	if err != nil {
		w.c.Errors++
		log.Printf("[MBOX-IMPORT] spool file=%q: directory %q: %v", w.label, dir, err)
		return nil
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return spoolNameLess(entries[i].Name(), entries[j].Name())
	})
	for _, e := range entries {
		if w.stopped() {
			return errStopped
		}
		name := path.Join(dir, e.Name())
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			continue // symlinks are not followed (a crosspost link would import twice)
		case e.IsDir():
			if err := w.walkDir(name); err != nil {
				return err
			}
		case e.Type().IsRegular():
			if err := w.readFile(name); err != nil {
				return err
			}
		default:
			continue // a device, socket or fifo is no article
		}
	}
	return nil
}

// readFile classifies one regular file and reads it. The file is closed on every path.
func (w *spoolWalker) readFile(name string) error {
	w.c.Files++
	if isXrefPointerName(name) {
		w.c.XrefPointers++
		return nil
	}
	f, err := w.fsys.Open(name)
	if err != nil {
		w.c.Errors++
		log.Printf("[MBOX-IMPORT] spool file=%q: open %q: %v", w.label, name, err)
		return nil
	}
	defer func() { _ = f.Close() }()

	n, err := io.ReadFull(f, w.buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		w.c.Errors++
		log.Printf("[MBOX-IMPORT] spool file=%q: read %q: %v", w.label, name, err)
		return nil
	}
	if n == 0 {
		w.c.NotArticle++ // an empty file
		return nil
	}
	peek := w.buf[:n]
	r := io.MultiReader(bytes.NewReader(peek), f)
	switch classifySpoolFile(peek) {
	case spoolMboxFile:
		return w.readMboxFile(name, r)
	case spoolANewsBatch:
		return w.readANewsFile(name, r)
	case spoolArticle:
		w.readArticleFile(name, r)
		return nil
	default:
		w.c.NotArticle++
		return nil
	}
}

// readArticleFile reads a file holding one article: the header block up to the first empty
// line, then the body (handleMessage drains it).
func (w *spoolWalker) readArticleFile(name string, r io.Reader) {
	br := bufio.NewReaderSize(r, spoolReadBufferSize)
	head, oversize, err := spoolHeaderBlock(br)
	switch {
	case err != nil:
		w.c.Errors++
		log.Printf("[MBOX-IMPORT] spool file=%q: read %q: %v", w.label, name, err)
		return
	case oversize:
		// no empty line in the first spoolMaxHeaderBytes bytes: not an article after all
		w.c.NotArticle++
		return
	}
	w.c.Read++
	handleMessage(w.c, head, br, w.opts, w.sink)
	w.progress()
}

// readMboxFile reads a whole mbox file inside the spool through the mbox parser, the same
// per-message loop readMbox runs over an mbox input.
func (w *spoolWalker) readMboxFile(name string, r io.Reader) error {
	stream, err := mbox.CreateMboxStream(bufio.NewReaderSize(r, spoolReadBufferSize))
	if err != nil {
		// the first line was a separator, so this is a truncated file or a read error
		if errors.Is(err, io.EOF) {
			w.c.NotArticle++
			return nil
		}
		w.c.Errors++
		log.Printf("[MBOX-IMPORT] spool file=%q: mbox %q: %v", w.label, name, err)
		return nil
	}
	lastErr := ""
	for {
		if w.stopped() {
			return errStopped
		}
		msg, err := stream.ReadMessage()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if err.Error() == lastErr {
				log.Printf("[MBOX-IMPORT] spool file=%q: mbox %q: parser stuck (same error twice): %v",
					w.label, name, err)
				w.c.StoppedEarly = true
				break
			}
			lastErr = err.Error()
			w.c.ParserSkipped++
			log.Printf("[MBOX-IMPORT] spool file=%q: mbox %q: parser skipped a message: %v", w.label, name, err)
			continue
		}
		lastErr = ""
		w.c.Read++
		handleMessage(w.c, msg.Headers(), msg.BodyReader(), w.opts, w.sink)
		w.progress()
	}
	if misses, _ := stream.NearMisses(); misses > 0 {
		w.c.NearMisses += misses
	}
	return nil
}

// readANewsFile reads an A-news batch: parseANews emits one header block and body per
// article. A parse error is logged (the file is an input-level problem, not an article
// error) and the walk carries on; a batch that yielded nothing counts as not-article, one
// that yielded at least one article as an A-news batch.
func (w *spoolWalker) readANewsFile(name string, r io.Reader) error {
	emitted, stopped := 0, false
	err := parseANews(r, func(head []string, body io.Reader) {
		if !stopped && w.stopped() {
			stopped = true // between two articles of the batch
		}
		if stopped {
			_, _ = io.Copy(io.Discard, body)
			return
		}
		emitted++
		w.c.Read++
		handleMessage(w.c, head, body, w.opts, w.sink)
		w.progress()
	})
	if err != nil {
		log.Printf("[MBOX-IMPORT] spool file=%q: A-news batch %q: %v", w.label, name, err)
	}
	if emitted > 0 {
		w.c.ANewsBatches++
	} else {
		w.c.NotArticle++
	}
	if stopped {
		return errStopped
	}
	return nil
}

// progress logs a progress line every progressEvery articles, with the files walked.
func (w *spoolWalker) progress() {
	if w.c.Read%progressEvery != 0 {
		return
	}
	log.Printf("[MBOX-IMPORT] progress file=%q files=%d read=%d stored=%d present=%d skipped=%d rejected=%d errors=%d",
		w.label, w.c.Files, w.c.Read, w.c.Stored, w.c.Present, w.c.skippedTotal(), w.c.Rejected, w.c.Errors)
}

// spoolHeaderBlock reads the header lines of a one-article file: every line up to the first
// empty one, without its line ending (a CR before the LF is dropped: the DOS-side copies of
// the CDs have CRLF). At most maxHeaderLines+1 lines are kept, so rewriteHead skips an
// article with a header block over the limit instead of this holding it whole. oversize
// reports that no empty line came within spoolMaxHeaderBytes bytes: the file is no article.
func spoolHeaderBlock(br *bufio.Reader) (head []string, oversize bool, err error) {
	bytesRead := 0
	for {
		line, rerr := br.ReadString('\n')
		bytesRead += len(line)
		line = strings.TrimRight(line, "\r\n")
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return nil, false, rerr
			}
			if line != "" {
				head = append(head, line)
			}
			return head, false, nil // a header block without a body
		}
		if line == "" {
			return head, false, nil
		}
		if len(head) < maxHeaderLines+1 {
			head = append(head, line)
		}
		if bytesRead > spoolMaxHeaderBytes {
			return nil, true, nil
		}
	}
}

// isXrefPointerName reports whether the file is a NetNews/CD crosspost pointer, "<n>.XRF"
// (its content is the group and number of the article it points at, not an article).
func isXrefPointerName(name string) bool {
	return strings.HasSuffix(name, ".XRF") || strings.HasSuffix(name, ".xrf")
}

// spoolNameLess orders the entries of one spool directory: all-digit names (article
// numbers and year directories) come first in numeric order, so 10 comes after 9, and
// every other name after them in lexical order. Both halves are ordered among themselves,
// which keeps the comparison a total order even for names such as "5.XRF".
func spoolNameLess(a, b string) bool {
	an, aok := spoolArticleNumber(a)
	bn, bok := spoolArticleNumber(b)
	switch {
	case aok != bok:
		return aok
	case aok && an != bn:
		return an < bn
	}
	return a < b
}

// spoolArticleNumber returns the value of an all-digit name; ok is false for every other
// name (and for one with more digits than fit, which is ordered lexically).
func spoolArticleNumber(name string) (n uint64, ok bool) {
	if name == "" {
		return 0, false
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(name, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// classifySpoolFile decides what a file holds from its first bytes (spoolPeekSize):
// an mbox file, an A-news batch, one article, or none of them.
func classifySpoolFile(peek []byte) spoolClass {
	if len(peek) == 0 {
		return spoolNotArticle
	}
	if spoolIsMboxStart(peek) {
		return spoolMboxFile
	}
	lines := spoolPeekLines(peek, maxHeaderLines+1)
	if spoolIsANewsStart(lines) {
		return spoolANewsBatch
	}
	if spoolIsArticleStart(lines) {
		return spoolArticle
	}
	return spoolNotArticle
}

// spoolPeekLines splits peek into at most maxLines lines without their line endings (a CR
// before the LF is dropped). A last line that the peek cut in the middle is kept: it is
// only looked at for its field name or its first bytes.
func spoolPeekLines(peek []byte, maxLines int) []string {
	lines := make([]string, 0, 16)
	for len(peek) > 0 && len(lines) < maxLines {
		line := peek
		if i := bytes.IndexByte(peek, '\n'); i >= 0 {
			line, peek = peek[:i], peek[i+1:]
		} else {
			peek = nil
		}
		lines = append(lines, string(bytes.TrimSuffix(line, []byte("\r"))))
	}
	return lines
}

// spoolIsMboxStart reports whether the first line of peek is a message separator as the
// mbox parser sees it. The parser's own verdict is taken, by handing it that one line:
// CreateMboxStream returns io.EOF for a first line that is not a separator. Every shape it
// knows starts with "From " or "#! rnews ", which is checked first so that the regex and
// the parser never run for the files of a spool (millions of them) that cannot match.
func spoolIsMboxStart(peek []byte) bool {
	line := peek
	if i := bytes.IndexByte(peek, '\n'); i >= 0 {
		line = peek[:i+1]
	}
	if !bytes.HasPrefix(line, []byte("From ")) && !bytes.HasPrefix(line, []byte("#! rnews ")) {
		return false
	}
	_, err := mbox.CreateMboxStream(bytes.NewReader(line))
	return err == nil
}

// spoolIsANewsStart reports whether the lines start an A-news batch: "A<site>.<n>",
// newsgroups, path, and a ctime date as the fourth line.
func spoolIsANewsStart(lines []string) bool {
	if len(lines) < 4 || !spoolANewsID.MatchString(lines[0]) {
		return false
	}
	return spoolIsCtime(lines[3])
}

// spoolIsCtime reports whether line is a ctime date, "Mon Nov 23 23:44:52 1981".
func spoolIsCtime(line string) bool {
	line = strings.TrimSpace(line)
	for _, layout := range spoolCtimeLayouts {
		if _, err := time.Parse(layout, line); err == nil {
			return true
		}
	}
	return false
}

// spoolHeadFields are the header fields one of which a one-article file must have: an
// article of the discs always carries at least one of them (Article-I.D. is the B-news
// field the oldest utzoo articles have instead of a Message-ID).
var spoolHeadFields = map[string]bool{
	"newsgroups": true, "message-id": true, "article-i.d.": true,
	"path": true, "from": true, "subject": true,
}

// spoolIsArticleStart reports whether the lines start one article: the first line is a
// header field (an RFC 5322 field name and a colon) and the header block, up to the first
// empty line and at most maxHeaderLines lines, holds one of spoolHeadFields.
func spoolIsArticleStart(lines []string) bool {
	if len(lines) == 0 || !spoolFieldName(lines[0], nil) {
		return false
	}
	for i, line := range lines {
		if line == "" {
			return false // the header block ended without one of the fields
		}
		if i >= maxHeaderLines {
			return false // no end of the header block in sight: no article
		}
		if isspace(line[0]) {
			continue // a continuation line
		}
		if spoolFieldName(line, spoolHeadFields) {
			return true
		}
	}
	return false // the peek ended inside the header block without one of the fields
}

// spoolFieldName reports whether line is a header field line: a field name of RFC 5322
// ftext (as the processor requires), a colon, and, when want is not nil, a name want holds.
// A name with spaces before the colon is accepted, as headerField reads it; a line that
// starts with whitespace is a continuation, not a field line.
func spoolFieldName(line string, want map[string]bool) bool {
	if line == "" || isspace(line[0]) {
		return false
	}
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return false
	}
	name := strings.TrimSpace(line[:colon])
	if !validFieldName(name) {
		return false
	}
	return want == nil || want[strings.ToLower(name)]
}
