package main

// News spools packed as .tar.gz / .tgz (plan importer-targz-spool): the textusenet backups
// and the gmane archives of the archive.org mirror hold one article per member
// ("<group path>/.artN"), a binary ".db" per group directory and the directory entries.
// A gzip stream has no random access and the archives are huge (rec.tar.gz: 381 GiB), so
// an archive is read in one sequential pass through compress/gzip and archive/tar, never
// unpacked, and every member is handed to the spool walker (spool.go) as it comes out of
// the archive: in archive order, one member at a time, nothing buffered beyond what the
// walker reads of one file. The archive order is not numeric (".art10124", ".art1", ...),
// so neither is the order the articles of a group are stored in (the overview sorts by
// date). Like in every spool, a one-article member is read whole into memory before it is
// handed over: up to -max-article-size (and the rest read past), or whole without the cap.
//
// After the tar's end-of-archive blocks the rest of the decompressed stream must be zeros
// (the padding to the record size): anything else (a second tar, `cat a.tar.gz b.tar.gz`)
// is an input error, never silently dropped.

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-while/go-pugleaf/internal/common"
)

// kindTarGz is the inputKind of a .tar.gz / .tgz news spool.
const kindTarGz = "targz"

// isTarSpool reports whether path names a .tar.gz or .tgz news spool (case-insensitive,
// with a trailing ".done" ignored like for every other input, inputKind).
func isTarSpool(path string) bool { return inputKind(path) == kindTarGz }

// errTarTrailing: the decompressed stream holds more than zero padding after the end of the
// tar archive (a second archive, concatenated .tar.gz files, garbage).
var errTarTrailing = errors.New("data after the end of the tar archive")

// tarIsFile reports whether a member type holds a file the walker reads: a regular file,
// '\x00' (tar.TypeRegA of the oldest tars, which the reader turns into TypeReg; kept for
// safety), or a GNU sparse file, whose expanded data the reader returns. Directories,
// links (a crosspost link would import twice), devices and the reader's own header types
// are not.
func tarIsFile(typ byte) bool {
	switch typ {
	case tar.TypeReg, '\x00', tar.TypeGNUSparse:
		return true
	}
	return false
}

// openTarSpool opens a .tar.gz input for readTarSpool: the file and the gzip reader over
// it. release closes both and is never nil when the error is nil. A file that is not gzip
// (or empty) is an error here, before anything is read.
func openTarSpool(path string) (io.Reader, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	gz, err := gzip.NewReader(bufio.NewReaderSize(f, inputBufferSize))
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("gzip: %w", err)
	}
	release := func() {
		// gzip.Reader.Close only returns the stream's own (sticky) error, which the read
		// that met it has reported already as the input error
		_ = gz.Close()
		if err := f.Close(); err != nil {
			log.Printf("[MBOX-IMPORT] file=%q: closing the file: %v", path, err)
		}
	}
	return gz, release, nil
}

// readTarSpool reads a .tar.gz news spool from r (the decompressed stream, openTarSpool) and
// hands every member the walker reads as a file (tarIsFile: regular and GNU sparse members)
// to it in archive order: it is counted in Files and classified by content like a file of
// any other spool (a ".db" counts in NotArticle, so does an empty member). Directory
// entries, symbolic and hard links (a crosspost link would import twice) and devices are
// passed over and count nowhere. label names the input in the log and decides the
// hierarchy prefix of the member directories (tarGroupPrefix, sourceDirOf).
//
// It returns errStopped (with the counters so far) when stop was closed between two
// members (or two articles of an mbox member). A gzip or tar error (a truncated or corrupt
// archive, a bad header, a checksum error at its end), a tar that ends without its
// end-of-archive blocks (errTarNoEnd) and data after them (errTarTrailing) are returned as
// an error, so the input is an input error and is not
// marked done. A one-article member the error cut short is never handed over (the walker
// reads it whole first); the members before it stay imported, as the messages of an mbox
// read before its gzip stream broke. An mbox or A-news member is streamed, as in every
// spool: the message an error cuts short there is handed over as if the member had ended.
func readTarSpool(r io.Reader, label string, opts *runOptions, sink inputSink, stop <-chan struct{}) (*counters, error) {
	w := newSpoolWalker(nil, label, opts, sink, stop)
	w.prefixDir, w.topDir = tarGroupPrefix(label)
	if w.stopped() {
		return w.c, errStopped
	}
	pr := &tarPosReader{r: r}
	tr := tar.NewReader(pr)
	var src spoolErrReader // the archive's read errors inside a member
	var dataEnd int64      // where the data of the last member ends (padded); -1: unknown
	for {
		if w.stopped() {
			return w.c, errStopped
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			// the reader also says io.EOF when the stream simply ends at a header boundary:
			// a tar cut between two members is no complete archive
			if !pr.endBlocksAfter(dataEnd) {
				return w.c, errTarNoEnd
			}
			// tar stops at its end-of-archive blocks: the rest of the stream must be zero
			// padding, and reading it to its end checks the gzip checksum and length too
			return w.c, w.tarTrailer(r)
		}
		if err != nil {
			return w.c, fmt.Errorf("reading the archive: %w", err)
		}
		dataEnd = tarDataEnd(hdr, pr.pos)
		if !tarIsFile(hdr.Typeflag) {
			continue // a directory, a link (a crosspost would import twice), a device
		}
		name := tarMemberName(hdr.Name)
		if name == "" {
			continue
		}
		src.reset(tr)
		if err := w.readMember(name, &src); err != nil {
			return w.c, err
		}
		if src.err != nil {
			// the walker counted it as a file error; for an archive it ends the input
			return w.c, fmt.Errorf("reading the archive: member %q: %w", name, src.err)
		}
	}
}

// errTarNoEnd: the tar stream ended without its end-of-archive blocks (cut between two
// members, e.g. a partial tar compressed whole).
var errTarNoEnd = errors.New("tar ended without its end-of-archive blocks")

// tarPosReader counts the bytes the tar reader takes and where the run of zero bytes that
// ends at the current position started, so that an io.EOF of tar.Reader.Next can be told
// apart: after end-of-archive blocks, or at a bare header boundary. It does not implement
// io.Seeker, so the tar reader reads (and skips) every byte through it.
type tarPosReader struct {
	r        io.Reader
	pos      int64 // bytes read
	zeroFrom int64 // the start of the zero bytes ending at pos (== pos after a non-zero byte)
}

func (p *tarPosReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	for i := n - 1; i >= 0; i-- {
		if b[i] != 0 {
			p.zeroFrom = p.pos + int64(i) + 1
			break
		}
	}
	p.pos += int64(n)
	return n, err
}

// endBlocksAfter reports whether at least one whole 512-byte block of zeros (an end-of-
// archive block; Go's reader takes one followed by the end of the stream as well) was read
// after dataEnd, the end of the last member's padded data (0 before the first member), and
// nothing but zeros since dataEnd. When dataEnd is unknown (-1: the last member was sparse,
// its stored size is not in the header Next returns), the last 512 bytes read must be zeros.
func (p *tarPosReader) endBlocksAfter(dataEnd int64) bool {
	if dataEnd < 0 {
		return p.pos-p.zeroFrom >= 512
	}
	return p.pos-dataEnd >= 512 && p.zeroFrom <= dataEnd
}

// tarDataEnd returns where the data of the member hdr ends in the stream, padded to the
// block size, when its header ended at pos: header-only types (links, directories, devices)
// have none, a sparse member's stored size is not known (-1), every other member stores
// hdr.Size bytes.
func tarDataEnd(hdr *tar.Header, pos int64) int64 {
	switch hdr.Typeflag {
	case tar.TypeLink, tar.TypeSymlink, tar.TypeChar, tar.TypeBlock, tar.TypeDir, tar.TypeFifo:
		return pos
	case tar.TypeGNUSparse:
		return -1
	}
	for k := range hdr.PAXRecords {
		if strings.HasPrefix(k, "GNU.sparse.") {
			return -1 // a PAX sparse file
		}
	}
	return pos + (hdr.Size+511)/512*512
}

// tarTrailer reads what follows the tar's end-of-archive blocks to the end of the stream:
// only zero bytes (the padding to the record size) may come; anything else is errTarTrailing
// (a second archive in the same gzip stream, or `cat a.tar.gz b.tar.gz`, whose second gzip
// member the reader reads as more of the stream). A read error (a gzip checksum or length
// error at the end) is returned as well. The stop signal is checked between two reads.
func (w *spoolWalker) tarTrailer(r io.Reader) error {
	buf := w.buf // the walker's peek buffer: no file is being read
	var n int64
	for {
		if w.stopped() {
			return errStopped
		}
		k, err := r.Read(buf)
		for i, b := range buf[:k] {
			if b != 0 {
				return fmt.Errorf("%w (a non-zero byte %d bytes after its end-of-archive blocks)",
					errTarTrailing, n+int64(i))
			}
		}
		n += int64(k)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the archive after the end of the tar: %w", err)
		}
	}
}

// tarMemberName is the member's path as the walker names it: cleaned, without a leading "/"
// or "./" ("./psychology/x/.art1" -> "psychology/x/.art1"); "" for a name that is no file.
// The name is only matched against the article's groups and logged, never used as a path on
// disk, so a ".." in it does no harm.
func tarMemberName(name string) string {
	name = path.Clean(strings.TrimLeft(name, "/"))
	if name == "." || name == "/" {
		return ""
	}
	return name
}

// tarGroupPrefix splits the file name of a .tar.gz spool named after a hierarchy into the
// part its member paths leave out and their top directory: gmane.psychology.tar.gz holds
// psychology/behavior-analysis/.art1 (Newsgroups: gmane.psychology.behavior-analysis), so
// it gives prefixDir "gmane" and topDir "psychology" (gmane.comp.lang.tar.gz: "gmane/comp"
// and "lang"). A name without a dot (rec.tar.gz is no hierarchy prefix: rec/... is the
// whole group path already) or that is no valid group name gives "", "". The walker uses
// them in sourceDirOf (spool.go).
func tarGroupPrefix(p string) (prefixDir, topDir string) {
	name := filepath.Base(p)
	if strings.HasSuffix(strings.ToLower(name), ".done") {
		name = name[:len(name)-len(".done")]
	}
	for _, s := range []string{".tar.gz", ".tgz"} {
		if strings.HasSuffix(strings.ToLower(name), s) {
			name = name[:len(name)-len(s)]
			break
		}
	}
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 || !common.IsValidGroupName(name) {
		return "", ""
	}
	return strings.ReplaceAll(name[:i], ".", "/"), name[i+1:]
}
