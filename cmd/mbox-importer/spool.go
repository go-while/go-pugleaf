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
// as not-article instead of being handed over.
const spoolMaxHeaderBytes = 1 << 20

// spoolReadBufferSize is the read buffer over an mbox file inside a spool.
const spoolReadBufferSize = 64 * 1024

// spoolSizeSlack is how much of a one-article file is read past -max-article-size, so that
// handleMessage sees the article is over the cap and counts it as too-big: one byte for the
// empty line between the header block and the body, which its accounting of the cap leaves
// out, and one more byte to be over it.
const spoolSizeSlack = 2

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
	spoolNotArticle spoolClass = iota // a binary, an archive, .overview, a README, ...
	spoolArticle                      // one article with an RFC 1036/5322 header block
	spoolMboxFile                     // an mbox file (the DOUG.* floppies)
	spoolANewsBatch                   // an A-news batch: several articles, no header names
)

// spoolErrReader records the first read error (other than io.EOF) of what it reads, so a
// file the mbox parser or parseANews gave up on can be told apart from a damaged one.
type spoolErrReader struct {
	r   io.Reader
	err error
}

func (e *spoolErrReader) reset(r io.Reader) { e.r, e.err = r, nil }

func (e *spoolErrReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && e.err == nil {
		e.err = err
	}
	return n, err
}

// spoolWalker is the state of one spool walk: what it reads, where it reports and the
// buffers, which are reused for every file (a disc holds millions of them).
type spoolWalker struct {
	fsys  fs.FS
	label string // the input as the log names it (the image or directory)
	opts  *runOptions
	sink  inputSink
	stop  <-chan struct{}
	c     *counters

	buf  []byte         // spoolPeekSize bytes: the peek that classifies a file
	art  bytes.Buffer   // the one-article file being read
	body bytes.Reader   // the body of that article, handed to handleMessage
	br   *bufio.Reader  // the read buffer of an mbox file inside the spool
	rest spoolErrReader // the read errors of an mbox or A-news file
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
// A file or directory that cannot be opened or read counts in FileErrors and is logged with
// its name: the walk carries on, the input is still marked done and the exit code is
// unchanged (a re-walk of a read-only image would only read the same bad blocks again).
// A one-article file is read whole before it is handed over, so a read error never stores a
// truncated article; an mbox file and an A-news batch are streamed, so a read error in one
// of them is recorded in FileErrors, but the message it cut short is handed over as if the
// file had ended there. StoppedEarly is only set by a stuck mbox parser inside the spool, as
// it is for an mbox input.
func readSpool(fsys fs.FS, label string, opts *runOptions, sink inputSink, stop <-chan struct{}) (*counters, error) {
	w := &spoolWalker{
		fsys:  fsys,
		label: label,
		opts:  opts,
		sink:  sink,
		stop:  stop,
		c:     newCounters(label),
		buf:   make([]byte, spoolPeekSize),
		br:    bufio.NewReaderSize(bytes.NewReader(nil), spoolReadBufferSize),
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

// fileError counts and logs a file or directory of the spool that could not be read.
func (w *spoolWalker) fileError(name, what string, err error) {
	w.c.FileErrors++
	log.Printf("[MBOX-IMPORT] spool file=%q: %s %q: %v", w.label, what, name, err)
}

// walkDir reads one directory of the spool and its subdirectories. A directory that cannot
// be read is counted and logged, and the walk carries on with the rest; only errStopped
// ends the walk.
func (w *spoolWalker) walkDir(dir string) error {
	entries, err := fs.ReadDir(w.fsys, dir)
	if err != nil {
		w.fileError(dir, "read directory", err)
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
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
		w.fileError(name, "open", err)
		return nil
	}
	defer func() { _ = f.Close() }()

	n, err := io.ReadFull(f, w.buf)
	truncated := err == nil // the peek filled the buffer: the file may hold more
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		w.fileError(name, "read", err)
		return nil
	}
	if n == 0 {
		w.c.NotArticle++ // an empty file
		return nil
	}
	peek := w.buf[:n]
	switch classifySpoolFile(peek, truncated) {
	case spoolMboxFile:
		return w.readMboxFile(name, peek, f)
	case spoolANewsBatch:
		return w.readANewsFile(name, peek, f)
	case spoolArticle:
		w.readArticleFile(name, peek, f)
		return nil
	default:
		w.c.NotArticle++
		return nil
	}
}

// readArticleFile reads a file holding one article whole, then hands its header block and
// body over: an article is never stored from a file a read error cut short. With
// -max-article-size the file is read spoolSizeSlack bytes past the cap and the rest is
// drained, so handleMessage counts the article as too-big; without the flag the file is
// read whole (the articles of the discs are small).
func (w *spoolWalker) readArticleFile(name string, peek []byte, f io.Reader) {
	w.art.Reset()
	_, _ = w.art.Write(peek) // bytes.Buffer.Write never fails
	if limit := w.opts.maxArticleSize; limit > 0 {
		if left := limit + spoolSizeSlack - len(peek); left > 0 {
			if _, err := w.art.ReadFrom(io.LimitReader(f, int64(left))); err != nil {
				w.fileError(name, "read", err)
				return
			}
		}
		if _, err := io.Copy(io.Discard, f); err != nil { // the rest, for its read errors
			w.fileError(name, "read", err)
			return
		}
	} else if _, err := w.art.ReadFrom(f); err != nil {
		w.fileError(name, "read", err)
		return
	}
	head, oversize, body := spoolSplitArticle(w.art.Bytes())
	if oversize {
		// no empty line in the first spoolMaxHeaderBytes bytes: no article after all
		w.c.NotArticle++
		return
	}
	w.body.Reset(body)
	w.c.Read++
	handleMessage(w.c, head, &w.body, w.opts, w.sink)
	w.progress()
}

// readMboxFile reads a whole mbox file inside the spool through the mbox parser, the same
// per-message loop readMbox runs over an mbox input.
func (w *spoolWalker) readMboxFile(name string, peek []byte, f io.Reader) error {
	w.rest.reset(io.MultiReader(bytes.NewReader(peek), f))
	defer w.countReadError(name)
	w.br.Reset(&w.rest)
	stream, err := mbox.CreateMboxStream(w.br)
	if err != nil {
		if w.rest.err == nil {
			// the first line was a separator, so the file is truncated or holds a line the
			// parser refuses (over mbox.MaxLineLength): no mbox file after all
			if !errors.Is(err, io.EOF) {
				log.Printf("[MBOX-IMPORT] spool file=%q: mbox %q: %v", w.label, name, err)
			}
			w.c.NotArticle++
		}
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
	if misses, samples := stream.NearMisses(); misses > 0 {
		w.c.NearMisses += misses
		for i, nm := range samples {
			if i == maxNearMissSamples {
				break
			}
			log.Printf("[MBOX-IMPORT]   near-miss file=%q mbox=%q line=%d %q", w.label, name, nm.Line, nm.Text)
		}
	}
	return nil
}

// readANewsFile reads an A-news batch: parseANews emits one header block and body per
// article. A parse error is logged and, when part of the batch was imported, counted as a
// file error; a batch that yielded nothing counts as not-article, one that yielded at least
// one article as an A-news batch.
func (w *spoolWalker) readANewsFile(name string, peek []byte, f io.Reader) error {
	w.rest.reset(io.MultiReader(bytes.NewReader(peek), f))
	defer w.countReadError(name)
	emitted, stopped := 0, false
	err := parseANews(&w.rest, func(head []string, body io.Reader) {
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
		if emitted > 0 && w.rest.err == nil {
			// part of the batch was imported, the rest was not: a file-level problem
			// (a read error is counted by countReadError instead)
			w.c.FileErrors++
		}
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

// countReadError counts the read error of an mbox or A-news file once the file is done.
func (w *spoolWalker) countReadError(name string) {
	if w.rest.err != nil {
		w.fileError(name, "read", w.rest.err)
	}
}

// progress logs a progress line every progressEvery articles, with the files walked.
func (w *spoolWalker) progress() {
	if w.c.Read%progressEvery != 0 {
		return
	}
	log.Printf("[MBOX-IMPORT] progress file=%q files=%d read=%d stored=%d present=%d skipped=%d rejected=%d errors=%d",
		w.label, w.c.Files, w.c.Read, w.c.Stored, w.c.Present, w.c.skippedTotal(), w.c.Rejected, w.c.Errors)
}

// spoolSplitArticle splits the bytes of a one-article file into its header lines (without
// their line endings; a CR before the LF is dropped, the DOS-side copies of the CDs have
// CRLF) and the body that follows the first empty line. At most maxHeaderLines+1 lines are
// kept, so rewriteHead skips an article with a header block over the limit instead of this
// building it whole. oversize reports that no empty line came within spoolMaxHeaderBytes
// bytes, which means the file is no article.
func spoolSplitArticle(data []byte) (head []string, oversize bool, body []byte) {
	rest := data
	for len(rest) > 0 {
		var line []byte
		line, rest = spoolLine(rest)
		if len(line) == 0 {
			return head, false, rest // the empty line ends the header block
		}
		if len(head) < maxHeaderLines+1 {
			head = append(head, string(line))
		}
		if len(data)-len(rest) > spoolMaxHeaderBytes {
			return nil, true, nil
		}
	}
	return head, false, nil // a header block without a body
}

// spoolLine cuts the first line off p: the line without its ending (a CR before the LF
// dropped) and what follows it.
func spoolLine(p []byte) (line, rest []byte) {
	if i := bytes.IndexByte(p, '\n'); i >= 0 {
		return bytes.TrimSuffix(p[:i], []byte("\r")), p[i+1:]
	}
	return bytes.TrimSuffix(p, []byte("\r")), nil
}

// isXrefPointerName reports whether the file is a NetNews/CD crosspost pointer, "<n>.XRF"
// (its content is the group and number of the article it points at, not an article). An
// ISO 9660 version suffix, "1005.XRF;1", is cut off first.
func isXrefPointerName(name string) bool {
	if i := strings.LastIndexByte(name, ';'); i > 0 {
		if _, err := strconv.ParseUint(name[i+1:], 10, 16); err == nil {
			name = name[:i]
		}
	}
	return len(name) >= len(".XRF") && strings.EqualFold(name[len(name)-len(".XRF"):], ".XRF")
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

// classifySpoolFile decides what a file holds from its first bytes: an mbox file, an A-news
// batch, one article, or none of them. truncated says the peek ended because it was full,
// not because the file did, so the header block of an article may go on past it.
func classifySpoolFile(peek []byte, truncated bool) spoolClass {
	if len(peek) == 0 {
		return spoolNotArticle
	}
	if spoolIsMboxStart(peek) {
		return spoolMboxFile
	}
	if spoolIsANewsStart(peek) {
		return spoolANewsBatch
	}
	if spoolIsArticleStart(peek, truncated) {
		return spoolArticle
	}
	return spoolNotArticle
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

// spoolIsANewsStart reports whether peek starts an A-news batch: "A<site>.<n>",
// newsgroups, path, and a ctime date as the fourth line.
func spoolIsANewsStart(peek []byte) bool {
	line, rest := spoolLine(peek)
	if !spoolANewsID.Match(line) {
		return false
	}
	for i := 0; i < 2; i++ { // the newsgroups and the path line
		if len(rest) == 0 {
			return false
		}
		_, rest = spoolLine(rest)
	}
	if len(rest) == 0 {
		return false
	}
	line, _ = spoolLine(rest)
	return spoolIsCtime(string(line))
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
var spoolHeadFields = [][]byte{
	[]byte("newsgroups"), []byte("message-id"), []byte("article-i.d."),
	[]byte("path"), []byte("from"), []byte("subject"),
}

// spoolIsArticleStart reports whether peek starts one article: its first line is a header
// field (an RFC 5322 field name and a colon) and the header block, up to the first empty
// line and at most maxHeaderLines lines, holds one of spoolHeadFields. A header block the
// peek cut in the middle (truncated) is an article candidate without one of them: a long
// Path or References line can fill the peek on its own, and messageIDFromHeaders and
// rewriteHead decide over the whole block.
func spoolIsArticleStart(peek []byte, truncated bool) bool {
	line, rest := spoolLine(peek)
	if !spoolFieldName(line, nil) {
		return false
	}
	for i := 0; ; i++ {
		switch {
		case len(line) == 0:
			return false // the header block ended without one of the fields
		case i >= maxHeaderLines:
			return false // no end of the header block in sight: no article
		case !isspace(line[0]) && spoolFieldName(line, spoolHeadFields):
			return true
		case len(rest) == 0:
			// the peek ended inside the header block: only a cut-off block goes on
			return truncated
		}
		line, rest = spoolLine(rest)
	}
}

// spoolFieldName reports whether line is a header field line: a field name of RFC 5322
// ftext (as the processor requires, see validFieldName), a colon, and, when want is not
// nil, a name want holds. A name with spaces before the colon is accepted, as headerField
// reads it; a line that starts with whitespace is a continuation, not a field line.
func spoolFieldName(line []byte, want [][]byte) bool {
	if len(line) == 0 || isspace(line[0]) {
		return false
	}
	colon := bytes.IndexByte(line, ':')
	if colon <= 0 {
		return false
	}
	name := bytes.TrimSpace(line[:colon])
	if !spoolValidFieldName(name) {
		return false
	}
	if want == nil {
		return true
	}
	for _, field := range want {
		if bytes.EqualFold(name, field) {
			return true
		}
	}
	return false
}

// spoolValidFieldName is validFieldName on the bytes of a field name: the classifier runs
// over millions of files, so it does not turn every line into a string first.
func spoolValidFieldName(name []byte) bool {
	if len(name) == 0 {
		return false
	}
	for _, c := range name {
		if c < 0x21 || c > 0x7e || c == ':' {
			return false
		}
	}
	return true
}
