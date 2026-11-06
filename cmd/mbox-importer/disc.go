package main

// Disc images and news spool inputs (plan mbox-import-discs). A disc image (.iso, .cdr,
// .sqsh, .img) is unpacked with the 7z program into a private directory under -tmpdir and
// read as a news spool (readSpool, spool.go), and the directory is removed again on every
// path. With -spool a positional directory or .zip argument is read as a news spool as it
// is, without unpacking: that is also how a hand-mounted image is imported
// (fuseiso IMG /mnt/x; mbox-importer -spool /mnt/x).

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// kindDisc is the inputKind of a disc image.
const kindDisc = "disc"

// discSuffixes are the disc image suffixes the 7z program unpacks for us: ISO 9660 images
// (".iso", Rock Ridge and Joliet included), raw CD dumps (".cdr"), squashfs images (".sqsh")
// and raw FAT floppy images (".img").
var discSuffixes = []string{".iso", ".cdr", ".sqsh", ".img"}

// isDiscImage reports whether path names a disc image: its suffix is one of discSuffixes,
// case-insensitive, with a trailing ".done" ignored like for every other input (inputKind).
func isDiscImage(path string) bool { return inputKind(path) == kindDisc }

// spoolKind is how an input is read as a news spool; spoolNone means it is an mbox input.
type spoolKind int

const (
	spoolNone spoolKind = iota
	spoolDisc           // a disc image: unpacked with 7z into -tmpdir, then walked
	spoolDir            // -spool: a directory, walked through os.DirFS
	spoolZip            // -spool: a zip file, walked through archive/zip (not unpacked)
)

// spoolInput decides how an input is read. A disc image is always a news spool; a directory
// or a zip file is one only with -spool (opts.spool), because without it a directory
// argument contributes its *.mbox* files (expandInputs) and a zip is an mbox archive.
func spoolInput(path string, opts *runOptions) spoolKind {
	switch {
	case isDiscImage(path):
		return spoolDisc
	case !opts.spool:
		return spoolNone
	case inputKind(path) == "zip":
		return spoolZip
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return spoolDir
	}
	return spoolNone
}

// spoolDirSizes zeroes the recorded size of every planned input that is a directory: a
// directory has no size of its own (os.Stat gives the size of its entry, a number of the
// file system), so the tracking DB keeps size 0 and the directory's modification time for a
// spool directory. A rerun of the same, unchanged directory is then "already imported"
// (trackSameFile); a changed mtime (entries added or removed at its top level) makes it an
// input again, as a changed file does.
func spoolDirSizes(inputs []plannedInput) {
	for i := range inputs {
		if fi, err := os.Stat(inputs[i].Path); err == nil && fi.IsDir() {
			inputs[i].Size = 0
		}
	}
}

// importSpool imports one news spool input: a disc image, a directory or a zip file. It is
// the spool twin of importInput (import.go): the same sink, counters, mark-done rule and
// summary line, only the articles come from readSpool instead of the mbox parser. c is nil
// when the input could not be unpacked or opened.
//
// A disc image is renamed to IMAGE.done like any other input; a spool directory is never
// renamed (it is not the thing that was imported, and -spool is also used on a mount point).
func importSpool(path string, kind spoolKind, opts *runOptions, sink articleSink, stop <-chan struct{}) (c *counters, inputErr, stopped bool) {
	opts, err := inputOptions(path, opts)
	if err != nil {
		log.Printf("[MBOX-IMPORT] input error: %v", err)
		return nil, true, false
	}
	fsys, release, err := openSpool(path, kind, opts, stop)
	switch {
	case errors.Is(err, errStopped):
		log.Printf("[MBOX-IMPORT] file=%q: unpacking ended by the shutdown signal", path)
		return nil, false, true
	case err != nil:
		log.Printf("[MBOX-IMPORT] input error: file=%q: %v", path, err)
		return nil, true, false
	}
	defer release()

	log.Printf("[MBOX-IMPORT] importing spool file=%q", path)
	if kind == spoolDir {
		// what the tracking DB can compare for a directory: the path it was given and the
		// directory's own modification time (spoolDirSizes keeps the size at 0). Articles
		// added below it do not change either, so such an input is "already imported".
		log.Printf("[MBOX-IMPORT] file=%q is a spool directory: the tracking database knows it by this path "+
			"and the directory's own modification time; a change deeper in the tree is not seen, "+
			"so import it again with -reimport", path)
	}
	in := sink.beginInput()
	c, readErr := readSpool(fsys, path, opts, in, stop)
	if c == nil {
		c = newCounters(path)
	}
	c.Group = opts.group
	stopped = errors.Is(readErr, errStopped)
	if readErr != nil && !stopped {
		// a 7z of the terminal's process group is killed by Ctrl-C as well; a walk that
		// ended with another error only stopped when the signal is what ended it
		select {
		case <-stop:
			stopped = true
			log.Printf("[MBOX-IMPORT] file=%q: spool ended by the shutdown signal: %v", path, readErr)
		default:
			c.StoppedEarly = true
			inputErr = true
			log.Printf("[MBOX-IMPORT] input error: file=%q: %v", path, readErr)
		}
	}
	// A walk that ended without an error and without one article is an input error: an
	// empty tree, the wrong directory, an image whose spool is somewhere else, or a format
	// the classifier does not recognise. Reporting it is the whole point — an input that is
	// silently marked done is never looked at again.
	noArticles := false
	if readErr == nil && !stopped && c.Read == 0 {
		noArticles = true
		inputErr = true
		log.Printf("[MBOX-IMPORT] input error: file=%q: no articles in the spool (files=%d not-article=%d)",
			path, c.Files, c.NotArticle)
	}
	waitErr := in.endInput(opts.commitTimeout)
	if waitErr != nil {
		log.Printf("[MBOX-IMPORT] file=%q commit wait: %v", path, waitErr)
		c.CommitFailed = true
	}

	switch {
	case opts.dryRun || !opts.markDone:
	case kind == spoolDir:
		log.Printf("[MBOX-IMPORT] file=%q is a spool directory: not renamed", path)
	case stopped || c.StoppedEarly || noArticles || c.Errors > 0 || waitErr != nil:
		log.Printf("[MBOX-IMPORT] file=%q not marked done (stopped=%t stopped-early=%t no-articles=%t errors=%d commit-wait-error=%t)",
			path, stopped, c.StoppedEarly, noArticles, c.Errors, waitErr != nil)
	case strings.HasSuffix(path, ".done"):
		log.Printf("[MBOX-IMPORT] file=%q already named .done: not renamed", path)
	default:
		marked, renameErr := markDoneInput(path, opts.tracked)
		c.MarkedDone = marked
		if renameErr {
			inputErr = true
		}
	}

	log.Printf("[MBOX-IMPORT] file=%q read=%d stored=%d present=%d skipped=%d (%s) rejected=%d errors=%d parser-skipped=%d near-misses=%d stopped-early=%t unread=%t marked-done=%t%s",
		path, c.Read, c.Stored, c.Present, c.skippedTotal(), c.skippedDetail(), c.Rejected, c.Errors,
		c.ParserSkipped, c.NearMisses, c.StoppedEarly, c.Unread, c.MarkedDone, c.spoolDetail())
	return c, inputErr, stopped
}

// openSpool opens the file system of a spool input and returns the function that releases it
// again; release is called on every path (success, error and stop) and is never nil when the
// error is nil. A disc image is unpacked into a private subdirectory of opts.tmpDir, which
// release removes; a zip is read through archive/zip without being unpacked; a directory
// needs nothing.
func openSpool(path string, kind spoolKind, opts *runOptions, stop <-chan struct{}) (fs.FS, func(), error) {
	switch kind {
	case spoolDir:
		return os.DirFS(path), func() {}, nil
	case spoolZip:
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, nil, fmt.Errorf("zip: %w", err)
		}
		return &zr.Reader, func() {
			if err := zr.Close(); err != nil {
				log.Printf("[MBOX-IMPORT] file=%q: closing the zip: %v", path, err)
			}
		}, nil
	case spoolDisc:
		if opts.tmpDir == "" {
			log.Printf("[MBOX-IMPORT] file=%q: -tmpdir is not set: the image is unpacked into the system "+
				"temp directory, which is often small or a tmpfs (in memory); pass -tmpdir on a file system "+
				"with room for the whole unpacked image", path)
		}
		dir, err := os.MkdirTemp(opts.tmpDir, "mbox-importer-disc-")
		if err != nil {
			return nil, nil, err
		}
		release := func() {
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("[MBOX-IMPORT] file=%q: removing the unpack directory %q: %v", path, dir, err)
			}
		}
		if err := unpackDisc(path, dir, opts.sevenZip, stop); err != nil {
			release()
			return nil, nil, err
		}
		return os.DirFS(dir), release, nil
	}
	return nil, nil, fmt.Errorf("not a news spool input (%d)", int(kind))
}

// unpackDisc unpacks the whole disc image into dir with
// `7z x -y -bd -bso0 -bsp0 -o<dir> -- IMAGE` and waits for the program; its listing and
// progress are off, its stderr is read line by line (sevenZipErrors) and its exit status is
// judged by discUnpackResult. The image can hold millions of files (utzoo: 5.2 GB in 2.08M
// files), so the shutdown must not wait for it: when stop is closed the process group is
// killed and errStopped is returned. 7z runs in a process group of its own, so Ctrl-C at a
// terminal does not reach it directly and this kill is what ends it.
func unpackDisc(image, dir, sevenZip string, stop <-chan struct{}) error {
	prog, err := exec.LookPath(sevenZip)
	if err != nil {
		return fmt.Errorf("7z program: %w", err)
	}
	select {
	case <-stop:
		return errStopped // nothing is started any more
	default:
	}
	size := int64(-1)
	if fi, err := os.Stat(image); err == nil {
		size = fi.Size()
	}
	log.Printf("[MBOX-IMPORT] unpacking disc file=%q (%d bytes) to %q with %s", image, size, dir, filepath.Base(prog))

	stderr := &sevenZipErrors{}
	// -bso0 -bsp0 turn the per-file listing and the progress off; the error stream stays
	cmd := exec.Command(prog, "x", "-y", "-bd", "-bso0", "-bsp0", "-o"+dir, "--", image)
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 10 * time.Second // Wait must not hang on children keeping the pipes open
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("7z: %w", err)
	}
	// buffered: the goroutine ends with Wait, whether or not anybody reads its result
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		stderr.flush() // Wait returned, so the stderr copy is done: no line is left
		if err := discUnpackResult(image, dir, waitErr, stderr); err != nil {
			return err
		}
	case <-stop:
		killGroup(cmd)
		<-done // reap it; WaitDelay bounds the wait for its output copies
		return errStopped
	}
	log.Printf("[MBOX-IMPORT] unpacked disc file=%q to %q", image, dir)
	return nil
}

// dangerousLinkMessage is what 7-Zip says about a symbolic link whose target leaves the
// image ("ERROR: Dangerous link path was ignored : SPOOL/alt/test/link : ../../../other/9").
const dangerousLinkMessage = "Dangerous link path was ignored"

// discUnpackResult turns the exit status of a `7z x` run into an error, or nil when the
// image was unpacked well enough to walk it.
//
// 7-Zip's exit codes are 0 (ok), 1 (warnings: something could not be read, the rest is
// there), 2 (a fatal error) and 255 (user break). Exit 1 is logged and taken, because every
// file that was extracted is still a file of the spool.
//
// Exit 2 has one harmless cause that old CD images run into: a symbolic link whose target
// leaves the image is refused, a 0-byte placeholder is written in its place and everything
// else is extracted (7-Zip 25.01 then exits 2). That image is taken as well — and marked
// done afterwards, deliberately: unpacking it again on the next scan would end exactly the
// same way. It is only taken when the unpack directory is not empty and every other ERROR
// line of the run is one of those refused links, so a real failure (an archive that cannot
// be opened, a truncated one — 7-Zip prints an "ERRORS:" header for that — or a killed 7z)
// stays an input error.
func discUnpackResult(image, dir string, waitErr error, stderr *sevenZipErrors) error {
	if waitErr == nil {
		stderr.logDangerous(image)
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return fmt.Errorf("7z x: %w%s", waitErr, stderr.detail())
	}
	switch code := exitErr.ExitCode(); {
	case code == 1:
		// warnings only: 7-Zip could not read something, the rest was extracted
		log.Printf("[MBOX-IMPORT] file=%q: 7z reported warnings (exit 1), importing what it extracted%s",
			image, stderr.detail())
		stderr.logDangerous(image)
		return nil
	case code == 2 && stderr.dangerousOnly() && !dirIsEmpty(dir):
		stderr.logDangerous(image)
		return nil
	}
	return fmt.Errorf("7z x: %w%s", waitErr, stderr.detail())
}

// dirIsEmpty reports whether dir holds no entry at all (or cannot be read).
func dirIsEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err != nil || len(entries) == 0
}

// discErrorKeep is the most "ERROR" lines of one 7z run kept for the error message.
const discErrorKeep = 5

// sevenZipErrors is the stderr of a 7z run, read line by line: it counts the refused
// symbolic links (dangerousLinkMessage) and keeps the other "ERROR" lines, which is what
// tells a disc image with such links apart from a real failure — the tail of the output
// alone cannot, because the lines may be anywhere in it. It also keeps the last stderrKeep
// bytes for the error message, as the other 7z paths do. exec writes into it from its own
// goroutine, so everything is locked.
type sevenZipErrors struct {
	tail tailBuffer // locked itself

	mu        sync.Mutex
	partial   []byte   // bytes since the last newline
	dangerous int      // lines about a refused symbolic link
	other     []string // the first discErrorKeep other ERROR lines
	otherSeen int      // all other ERROR lines
}

func (s *sevenZipErrors) Write(p []byte) (int, error) {
	if _, err := s.tail.Write(p); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		s.line(string(s.partial[:i]))
		s.partial = append([]byte(nil), s.partial[i+1:]...)
	}
	if len(s.partial) > stderrKeep {
		// a program writing one endless line must not grow this buffer without end
		s.line(string(s.partial))
		s.partial = nil
	}
	return len(p), nil
}

// flush classifies a last line that had no newline. Call it after Wait returned, which is
// after exec finished copying the process's stderr.
func (s *sevenZipErrors) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.partial) > 0 {
		s.line(string(s.partial))
		s.partial = nil
	}
}

// line classifies one line of the output; the caller holds the lock.
func (s *sevenZipErrors) line(l string) {
	t := strings.TrimSpace(strings.TrimRight(l, "\r"))
	switch {
	case strings.Contains(t, dangerousLinkMessage):
		s.dangerous++
	case strings.HasPrefix(t, "ERROR"): // "ERROR: ..." and the "ERRORS:" header
		s.otherSeen++
		if len(s.other) < discErrorKeep {
			s.other = append(s.other, t)
		}
	}
}

// dangerousOnly reports whether the run complained about refused symbolic links and about
// nothing else.
func (s *sevenZipErrors) dangerousOnly() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dangerous > 0 && s.otherSeen == 0
}

// logDangerous logs the refused symbolic links of the run, if there were any.
func (s *sevenZipErrors) logDangerous(image string) {
	s.mu.Lock()
	n := s.dangerous
	s.mu.Unlock()
	if n > 0 {
		log.Printf("[MBOX-IMPORT] file=%q: %d dangerous link(s) skipped by 7z: a symbolic link with \"..\" in its target "+
			"is left as an empty file, everything else was extracted", image, n)
	}
}

// detail is what goes into an error message or a warning: ": " and the ERROR lines of the
// run, or the tail of its output when there were none; "" when it said nothing at all.
func (s *sevenZipErrors) detail() string {
	s.mu.Lock()
	lines, seen := append([]string(nil), s.other...), s.otherSeen
	s.mu.Unlock()
	if len(lines) > 0 {
		msg := strings.Join(lines, "; ")
		if seen > len(lines) {
			msg = fmt.Sprintf("%s (and %d more)", msg, seen-len(lines))
		}
		return ": " + msg
	}
	if msg := s.tail.String(); msg != "" {
		return ": " + msg
	}
	return ""
}

// killGroup kills the process group of a started command (so its children go too), or the
// process alone when the group cannot be signalled.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		log.Printf("[MBOX-IMPORT] 7z: kill: %v", err)
	}
}
