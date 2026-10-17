package main

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	mbox "github.com/go-while/go-lib-mbox2nntp"
)

// errNotImplemented is still used by the import.go stub (wave 2 replaces it).
var errNotImplemented = errors.New("not implemented")

// inputBufferSize is the read buffer between the decompressor (or file) and the parser.
const inputBufferSize = 1024 * 1024

// stderrKeep bounds how much of the 7z program's stderr is kept for error messages.
const stderrKeep = 4096

// source is one opened input: a plain, gzip, zip or 7z mbox stream.
type source struct {
	Path   string
	Stream *mbox.MboxStream // nil: the input is an empty file

	eof     *eofReader  // what the parser reads; nil for an empty input
	closers []io.Closer // closed in order by Close
	cmd     *exec.Cmd   // the 7z process, nil for the other kinds
	stderr  *tailBuffer // the 7z process's stderr (last stderrKeep bytes)
	closed  bool
}

// eofReader remembers whether its source has reported io.EOF, and the first other read
// error. The mbox library returns io.EOF too when it finds no separator where the next
// message should start, so this is how Unread tells the two apart.
//
// The layout is parser (its own small bufio) -> eofReader -> 1 MiB bufio -> file or
// decompressor: the eofReader sits on top of the big buffer, so sawEOF is set only when
// the big buffer is drained and the underlying reader hit io.EOF. Bytes still held in
// the big buffer, or never read from the underlying reader, both leave sawEOF false.
type eofReader struct {
	r      io.Reader
	n      int64 // bytes delivered to the parser
	sawEOF bool
	err    error // first read error other than io.EOF
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	e.n += int64(n)
	if err == io.EOF {
		e.sawEOF = true
	} else if err != nil && e.err == nil {
		e.err = err
	}
	return n, err
}

// tailBuffer keeps the last stderrKeep bytes written to it. exec copies the process's
// stderr into it from its own goroutine; Wait returns after that copy ended.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrKeep {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-stderrKeep:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// isInputName reports whether a directory entry is an input: its name contains ".mbox"
// or ".bag" and does not end in ".done".
func isInputName(name string) bool {
	if strings.HasSuffix(name, ".done") {
		return false
	}
	return strings.Contains(name, ".mbox") || strings.Contains(name, ".bag")
}

// expandInputs turns the positional arguments into the list of files to import:
// a file is itself, a directory contributes its *.mbox* / *.bag* entries (not *.done),
// sorted by name, without recursion. Missing paths are reported in errs.
func expandInputs(args []string) (files []string, errs []error) {
	for _, arg := range args {
		fi, err := os.Stat(arg)
		if err != nil {
			errs = append(errs, fmt.Errorf("input %q: %w", arg, err))
			continue
		}
		if !fi.IsDir() {
			files = append(files, arg)
			continue
		}
		entries, err := os.ReadDir(arg)
		if err != nil {
			errs = append(errs, fmt.Errorf("input directory %q: %w", arg, err))
			continue
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() || !isInputName(e.Name()) {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			files = append(files, filepath.Join(arg, name))
		}
	}
	return files, errs
}

// inputKind returns the lower-case kind of path by its suffix (a trailing ".done" is
// ignored, so a renamed input can be imported again): "gz", "zip", "7z" or "mbox" for
// everything else (.mbox, .bag, any other name: the parser rejects a non-mbox).
func inputKind(p string) string {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(p)), ".done")
	switch {
	case strings.HasSuffix(name, ".gz"):
		return "gz"
	case strings.HasSuffix(name, ".zip"):
		return "zip"
	case strings.HasSuffix(name, ".7z"):
		return "7z"
	}
	return "mbox"
}

// isMboxMemberName reports whether an archive member is named *.mbox or *.bag.
func isMboxMemberName(name string) bool {
	base := strings.ToLower(path.Base(name))
	return strings.HasSuffix(base, ".mbox") || strings.HasSuffix(base, ".bag")
}

// openSource opens path by its suffix (.mbox/.bag, .gz, .zip, .7z via the sevenZip program).
// An empty (0-byte) file, or a gzip/zip input that decompresses to nothing, gives a source
// with a nil Stream. A non-empty input whose stream does not start with an mbox separator
// is an error (for 7z this includes an archive without a *.mbox / *.bag member).
func openSource(path, sevenZip string) (*source, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := fh.Stat()
	if err != nil {
		_ = fh.Close()
		return nil, err
	}
	if fi.IsDir() {
		_ = fh.Close()
		return nil, fmt.Errorf("%s: is a directory", path)
	}
	if fi.Size() == 0 {
		_ = fh.Close()
		return &source{Path: path, closed: true}, nil
	}

	s := &source{Path: path}
	var r io.Reader
	kind := inputKind(path)
	switch kind {
	case "mbox":
		s.closers = append(s.closers, fh)
		r = fh
	case "gz":
		s.closers = append(s.closers, fh)
		gz, err := gzip.NewReader(fh)
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("%s: gzip: %w", path, err)
		}
		// gzip.Reader.Close does not close fh; it is closed after it
		s.closers = append([]io.Closer{gz}, s.closers...)
		r = gz
	case "zip":
		zr, err := zip.NewReader(fh, fi.Size())
		if err != nil {
			_ = fh.Close()
			return nil, fmt.Errorf("%s: zip: %w", path, err)
		}
		s.closers = append(s.closers, fh)
		member, err := pickZipMember(zr)
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rc, err := member.Open()
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("%s: zip member %q: %w", path, member.Name, err)
		}
		s.closers = append([]io.Closer{rc}, s.closers...)
		r = rc
	case "7z":
		_ = fh.Close() // 7z opens the archive itself
		stdout, err := s.start7z(path, sevenZip)
		if err != nil {
			return nil, err
		}
		r = stdout
	}

	s.eof = &eofReader{r: bufio.NewReaderSize(r, inputBufferSize)}
	stream, err := mbox.CreateMboxStream(s.eof)
	if err != nil {
		if kind != "7z" && s.eof.n == 0 && s.eof.sawEOF && s.eof.err == nil {
			// a compressed empty mbox: nothing to import, like a 0-byte file
			if cerr := s.Close(); cerr != nil {
				return nil, fmt.Errorf("%s: %w", path, cerr)
			}
			return &source{Path: path, closed: true}, nil
		}
		// Close reports a read error and a failed 7z run (it kills a 7z still writing)
		cerr := s.Close()
		switch {
		case cerr != nil:
			return nil, fmt.Errorf("%s: %w", path, cerr)
		case kind == "7z" && s.eof.n == 0:
			return nil, fmt.Errorf("%s: no *.mbox or *.bag member in the archive (empty stream)", path)
		case !errors.Is(err, io.EOF):
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return nil, fmt.Errorf("%s: no mbox separator at the start (not an mbox)", path)
	}
	s.Stream = stream
	return s, nil
}

// pickZipMember returns the first member named *.mbox / *.bag, else the only file member.
func pickZipMember(zr *zip.Reader) (*zip.File, error) {
	var files []*zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if isMboxMemberName(f.Name) {
			return f, nil
		}
		files = append(files, f)
	}
	switch len(files) {
	case 0:
		return nil, errors.New("zip: no member")
	case 1:
		return files[0], nil
	}
	return nil, fmt.Errorf("zip: %d members and none named *.mbox or *.bag", len(files))
}

// start7z starts `sevenZip x -so -bd -r -- archive *.mbox *.bag *.MBOX *.BAG` (members in
// subdirectories too, concatenated in archive order), sets s.cmd and s.stderr
// and returns the process's stdout.
func (s *source) start7z(archive, sevenZip string) (io.Reader, error) {
	prog, err := exec.LookPath(sevenZip)
	if err != nil {
		return nil, fmt.Errorf("%s: 7z program: %w", archive, err)
	}
	// 7z patterns are case-sensitive and match top-level members only without -r
	cmd := exec.Command(prog, "x", "-so", "-bd", "-r", "--", archive, "*.mbox", "*.bag", "*.MBOX", "*.BAG")
	// Close must not hang on a wrapper whose children keep the pipes open
	cmd.WaitDelay = 10 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: 7z: %w", archive, err)
	}
	s.stderr = &tailBuffer{}
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: 7z: %w", archive, err)
	}
	s.cmd = cmd
	return stdout, nil
}

// Unread reports whether the parser returned io.EOF before the underlying reader did:
// bytes are left in the read buffer or were never read from the file or decompressor.
// It is meaningful once ReadMessage returned io.EOF (or the caller stopped reading).
func (s *source) Unread() bool {
	if s.eof == nil {
		return false // empty input: nothing to read
	}
	return !s.eof.sawEOF
}

// Close releases the input; for 7z it waits for the process and returns its exit error.
// When the stream was not read to its end (Unread), the 7z process is killed first and
// its exit status is not an error: the caller sees Unread instead. A read error of the
// underlying reader (gzip/zip checksum, 7z pipe) is returned too.
func (s *source) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	for _, c := range s.closers {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.cmd != nil {
		killed := false
		if s.Unread() {
			if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				errs = append(errs, fmt.Errorf("7z: kill: %w", err))
			}
			killed = true
		}
		// Wait closes the stdout pipe and waits for the stderr copy
		if err := s.cmd.Wait(); err != nil && !killed {
			if msg := s.stderr.String(); msg != "" {
				err = fmt.Errorf("7z: %w: %s", err, msg)
			} else {
				err = fmt.Errorf("7z: %w", err)
			}
			errs = append(errs, err)
		}
	}
	if s.eof != nil && s.eof.err != nil {
		errs = append(errs, fmt.Errorf("read: %w", s.eof.err))
	}
	return errors.Join(errs...)
}

// markDone renames path to path+".done"; an existing path+".done" is never replaced.
// (Check-then-rename: a target created in between by another process is not caught.)
func markDone(path string) error {
	dst := path + ".done"
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("mark done %q: %q already exists: %w", path, dst, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("mark done %q: %w", path, err)
	}
	return os.Rename(path, dst)
}
