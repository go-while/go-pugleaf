package main

// News spools packed as .tar.gz / .tgz (plan importer-targz-spool): the textusenet backups
// and the gmane archives of the archive.org mirror hold one article per member
// ("<group path>/.artN"), a binary ".db" per group directory and the directory entries.
// A gzip stream has no random access and the archives are huge (rec.tar.gz: 381 GiB), so
// an archive is read in one sequential pass through compress/gzip and archive/tar and never
// unpacked. The archive order is not numeric (".art10124", ".art1", ".art10062", ...), so
// the members of each directory are collected while they come one after the other (a run)
// and handed to the spool walker (spool.go) sorted by spoolNameLess when the archive moves
// on to another directory or ends. A directory that comes back later in the archive is a
// run of its own, sorted within itself.
//
// A run's bytes are kept in memory up to tarRunMemory (members up to tarMemberMemory each);
// the rest of the run goes to one spill file under -tmpdir, which is unlinked as soon as it
// is created (its space comes back when it is closed, also when the process is killed) and
// truncated after every run. So memory is bounded by tarRunMemory plus the member list of
// the largest run (its names and offsets, about 80 bytes per member), and the disk by the
// largest run, not by the archive. With -max-article-size a one-article member is kept only
// up to the bytes the walker reads of it anyway (readArticleFile), so a big binary member
// costs at most that much.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-while/go-pugleaf/internal/common"
)

// kindTarGz is the inputKind of a .tar.gz / .tgz news spool.
const kindTarGz = "targz"

// isTarSpool reports whether path names a .tar.gz or .tgz news spool (case-insensitive,
// with a trailing ".done" ignored like for every other input, inputKind).
func isTarSpool(path string) bool { return inputKind(path) == kindTarGz }

const (
	// tarRunMemory is how many bytes of a run's members are kept in memory; the rest of
	// the run goes to the spill file.
	tarRunMemory = 64 << 20
	// tarMemberMemory is the largest member kept in memory; a bigger one always goes to
	// the spill file.
	tarMemberMemory = 1 << 20
	// tarSpillBufferSize is the write buffer in front of the spill file.
	tarSpillBufferSize = 1 << 20
)

// tarMember is one regular member of the current run.
type tarMember struct {
	base      string // the member's file name (".art10124"); its directory is the run's
	off, size int64  // where its kept bytes are: in tarRun.mem, or in the spill file
	inMem     bool
}

// tarRun holds the members of the current run (one directory, members one after the other
// in the archive) until the walker reads them, and the spill file behind it.
type tarRun struct {
	label          string // the input, for the log
	tmpDir         string // where the spill file is created ("" = the system temp dir)
	memLimit       int64  // tarRunMemory (smaller in the tests)
	memberLimit    int64  // tarMemberMemory (smaller in the tests)
	maxArticleSize int    // -max-article-size

	dir     string // the directory of the run ("." for top-level members)
	members []tarMember
	mem     []byte // the in-memory bytes, cap memLimit once allocated
	peek    []byte // the first bytes of the member being added

	spill     *os.File      // nil until a run outgrows memory
	spillName string        // set only when the spill file could not be unlinked at once
	spillW    *bufio.Writer // in front of spill
	spillOff  int64         // bytes written to spill in this run
	spilled   int           // members written to the spill file in this input (tests, log)

	src    spoolErrReader // the archive's read errors while a member is added
	memR   bytes.Reader   // hands an in-memory member to the walker
	spillR tarSpillReader // hands a spilled member to the walker
}

func newTarRun(label, tmpDir string, memLimit, memberLimit int64, maxArticleSize int) *tarRun {
	return &tarRun{
		label:          label,
		tmpDir:         tmpDir,
		memLimit:       memLimit,
		memberLimit:    memberLimit,
		maxArticleSize: maxArticleSize,
		peek:           make([]byte, spoolPeekSize),
	}
}

// tarSpillReader reads a spilled member back and records the first read error, which is a
// problem of the spill file (the local disk), not of the archive.
type tarSpillReader struct {
	r   *io.SectionReader
	err error
}

func (s *tarSpillReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && s.err == nil {
		s.err = err
	}
	return n, err
}

// errTarSpill marks an error of the spill file, as opposed to one of the archive.
var errTarSpill = errors.New("spill file")

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
		if err := gz.Close(); err != nil {
			log.Printf("[MBOX-IMPORT] file=%q: closing the gzip reader: %v", path, err)
		}
		if err := f.Close(); err != nil {
			log.Printf("[MBOX-IMPORT] file=%q: closing the file: %v", path, err)
		}
	}
	return gz, release, nil
}

// readTarSpool reads a .tar.gz news spool from r (the decompressed stream, openTarSpool) and
// feeds every article to sink, run by run (see the top of this file). label names the input
// in the log and decides the hierarchy prefix (tarGroupPrefix). Only regular members are
// read: directory entries, symbolic and hard links (a crosspost link would import twice) and
// devices are passed over and count nowhere; every regular member counts in Files and is
// classified by content like a file of any other spool (a ".db" counts in NotArticle).
//
// It returns errStopped (with the counters so far) when stop was closed between two
// members. A gzip or tar error (a truncated or corrupt archive, a checksum error at its end)
// is returned as an error, so the input is an input error and is not marked done; a member
// the error cut short is never handed over, while the members read whole before it are (as
// the messages of an mbox read before its gzip stream broke). An error of the spill file
// ends the input the same way.
func readTarSpool(r io.Reader, label string, opts *runOptions, sink inputSink, stop <-chan struct{}) (*counters, error) {
	t := newTarRun(label, opts.tmpDir, tarRunMemory, tarMemberMemory, opts.maxArticleSize)
	defer t.close()
	return readTarRuns(r, t, label, opts, sink, stop)
}

// readTarRuns is readTarSpool on a given run buffer (the tests pass small limits).
func readTarRuns(r io.Reader, t *tarRun, label string, opts *runOptions, sink inputSink, stop <-chan struct{}) (*counters, error) {
	w := newSpoolWalker(nil, label, opts, sink, stop)
	w.prefixDir, w.topDir = tarGroupPrefix(label)
	if w.stopped() {
		return w.c, errStopped
	}
	tr := tar.NewReader(r)
	var readErr error
	for {
		if w.stopped() {
			return w.c, errStopped
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			// tar stops at its end-of-archive blocks: read the gzip stream to its end as
			// well, so its checksum and length are checked
			if _, err := io.Copy(io.Discard, r); err != nil {
				readErr = fmt.Errorf("reading the archive after the end of the tar: %w", err)
			}
			break
		}
		if err != nil {
			readErr = fmt.Errorf("reading the archive: %w", err)
			break
		}
		// '\x00' is the regular file of the oldest tars (tar.TypeRegA, deprecated: the
		// reader turns it into TypeReg, this only keeps that so)
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != '\x00' {
			continue // a directory, a link (a crosspost would import twice), a device
		}
		name := tarMemberName(hdr.Name)
		if name == "" {
			continue
		}
		dir, base := path.Dir(name), path.Base(name)
		if dir != t.dir && len(t.members) > 0 {
			if err := w.walkTarRun(t); err != nil {
				return w.c, err
			}
		}
		t.dir = dir
		if err := t.add(base, hdr.Size, tr); err != nil {
			if errors.Is(err, errTarSpill) {
				readErr = fmt.Errorf("member %q: %w", name, err)
			} else {
				readErr = fmt.Errorf("reading the archive: member %q: %w", name, err)
			}
			break
		}
	}
	// the members read whole before an error are as good as the runs walked before it
	if err := w.walkTarRun(t); err != nil {
		if readErr != nil && !errors.Is(err, errStopped) {
			return w.c, errors.Join(readErr, err)
		}
		return w.c, err
	}
	return w.c, readErr
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

// add reads one regular member of size bytes from r into the run: into memory while it fits
// (memberLimit, memLimit), else into the spill file. With -max-article-size a member that is
// neither an mbox nor an A-news batch (by its first bytes, classifySpoolFile) is kept only
// up to what readArticleFile reads of it, and the rest is read past: the walker then sees
// the same bytes it would have read from the whole member. A member cut short is not added;
// the error is the archive's, or wraps errTarSpill for one of the spill file.
func (t *tarRun) add(base string, size int64, r io.Reader) error {
	t.src.reset(r)
	n, err := io.ReadFull(&t.src, t.peek[:min(size, int64(len(t.peek)))])
	if err != nil {
		return err
	}
	peek := t.peek[:n]
	keep := size
	if limit := t.maxArticleSize; limit > 0 {
		capped := max(int64(limit)+spoolSizeSlack, int64(spoolPeekSize))
		if size > capped {
			switch classifySpoolFile(peek, true) {
			case spoolMboxFile, spoolANewsBatch: // read whole by the walker
			default:
				keep = capped
			}
		}
	}
	m := tarMember{base: strings.Clone(base), size: keep}
	if keep <= t.memberLimit && int64(len(t.mem))+keep <= t.memLimit {
		if t.mem == nil {
			t.mem = make([]byte, 0, t.memLimit)
		}
		start := len(t.mem)
		t.mem = append(t.mem, peek...)
		t.mem = t.mem[:start+int(keep)]
		if _, err := io.ReadFull(&t.src, t.mem[start+n:]); err != nil {
			t.mem = t.mem[:start]
			return err
		}
		m.off, m.inMem = int64(start), true
	} else {
		if err := t.openSpill(); err != nil {
			return err
		}
		m.off = t.spillOff
		if err := t.spillMember(peek, keep-int64(n)); err != nil {
			return err
		}
	}
	if rest := size - keep; rest > 0 {
		// read past the part not kept: the archive's errors there are still errors
		if _, err := io.CopyN(io.Discard, &t.src, rest); err != nil {
			t.drop(m)
			return err
		}
	}
	t.members = append(t.members, m)
	return nil
}

// spillMember writes peek and the next rest bytes of t.src to the spill file.
func (t *tarRun) spillMember(peek []byte, rest int64) error {
	w, err := t.spillW.Write(peek)
	t.spillOff += int64(w)
	if err != nil {
		return fmt.Errorf("%w: %v", errTarSpill, err)
	}
	c, err := io.CopyN(t.spillW, &t.src, rest)
	t.spillOff += c
	if err != nil {
		if t.src.err != nil || errors.Is(err, io.EOF) {
			return err // the archive's: the member is cut short
		}
		return fmt.Errorf("%w: %v", errTarSpill, err)
	}
	t.spilled++
	return nil
}

// drop forgets the bytes of a member that was not added (its spilled bytes stay as dead
// space until the run ends).
func (t *tarRun) drop(m tarMember) {
	if m.inMem {
		t.mem = t.mem[:m.off]
	}
}

// openSpill creates the spill file the first time a run outgrows memory. It is unlinked at
// once, so nothing is left in -tmpdir whatever ends the process; where the file system
// refuses that, close removes it by name.
func (t *tarRun) openSpill() error {
	if t.spill != nil {
		return nil
	}
	f, err := os.CreateTemp(t.tmpDir, "mbox-importer-targz-")
	if err != nil {
		return fmt.Errorf("%w: %v", errTarSpill, err)
	}
	if err := os.Remove(f.Name()); err != nil {
		t.spillName = f.Name()
	}
	t.spill = f
	t.spillW = bufio.NewWriterSize(f, tarSpillBufferSize)
	log.Printf("[MBOX-IMPORT] file=%q: directory %q holds more than %d MiB of articles: the rest of it, and of every "+
		"such directory of this archive, goes to a spill file in %q (unlinked, emptied after each directory)",
		t.label, t.dir, t.memLimit>>20, filepath.Dir(f.Name()))
	return nil
}

// reset empties the run after it was walked (or given up): the memory is kept for the next
// run, the spill file is truncated.
func (t *tarRun) reset() error {
	if cap(t.members) > 1<<16 {
		t.members = nil // a big run's list is not kept for the small runs after it
	} else {
		clear(t.members) // the names are not kept alive
		t.members = t.members[:0]
	}
	t.mem = t.mem[:0]
	if t.spill == nil {
		return nil
	}
	t.spillW.Reset(t.spill)
	if t.spillOff == 0 {
		return nil
	}
	t.spillOff = 0
	if err := t.spill.Truncate(0); err != nil {
		return fmt.Errorf("%w: %v", errTarSpill, err)
	}
	if _, err := t.spill.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w: %v", errTarSpill, err)
	}
	return nil
}

// close closes (and, if it could not be unlinked at once, removes) the spill file. It is
// called on every path out of readTarSpool.
func (t *tarRun) close() {
	if t.spill == nil {
		return
	}
	if err := t.spill.Close(); err != nil {
		log.Printf("[MBOX-IMPORT] file=%q: closing the spill file: %v", t.label, err)
	}
	if t.spillName != "" {
		if err := os.Remove(t.spillName); err != nil {
			log.Printf("[MBOX-IMPORT] file=%q: removing the spill file %q: %v", t.label, t.spillName, err)
		}
	}
	t.spill, t.spillW, t.spillName = nil, nil, ""
}

// walkTarRun hands the members of the run to the walker in spoolNameLess order of their
// names (".art9" before ".art10") and empties the run, also when the walk ends early.
func (w *spoolWalker) walkTarRun(t *tarRun) error {
	if len(t.members) == 0 {
		return nil
	}
	err := w.walkTarMembers(t)
	if rerr := t.reset(); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

func (w *spoolWalker) walkTarMembers(t *tarRun) error {
	if t.spillW != nil {
		if err := t.spillW.Flush(); err != nil {
			return fmt.Errorf("%w: %v", errTarSpill, err)
		}
	}
	sort.SliceStable(t.members, func(i, j int) bool {
		return spoolNameLess(t.members[i].base, t.members[j].base)
	})
	for i := range t.members {
		if w.stopped() {
			return errStopped
		}
		m := &t.members[i]
		name := path.Join(t.dir, m.base)
		if m.inMem {
			t.memR.Reset(t.mem[m.off : m.off+m.size])
			if err := w.readMember(name, &t.memR); err != nil {
				return err
			}
			continue
		}
		t.spillR = tarSpillReader{r: io.NewSectionReader(t.spill, m.off, m.size)}
		if err := w.readMember(name, &t.spillR); err != nil {
			return err
		}
		if t.spillR.err != nil {
			return fmt.Errorf("%w: reading %q back: %v", errTarSpill, name, t.spillR.err)
		}
	}
	return nil
}
