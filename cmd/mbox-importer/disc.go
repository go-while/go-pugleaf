package main

// Disc images and news spool inputs (plan mbox-import-discs). A disc image (.iso, .cdr,
// .sqsh, .img) is unpacked with the 7z program into a private directory under -tmpdir and
// read as a news spool (readSpool, spool.go), and the directory is removed again on every
// path. With -spool a positional directory or .zip argument is read as a news spool as it
// is, without unpacking: that is also how a hand-mounted image is imported
// (fuseiso IMG /mnt/x; mbox-importer -spool /mnt/x).

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	waitErr := in.endInput(opts.commitTimeout)
	if waitErr != nil {
		log.Printf("[MBOX-IMPORT] file=%q commit wait: %v", path, waitErr)
		c.CommitFailed = true
	}

	switch {
	case opts.dryRun || !opts.markDone:
	case kind == spoolDir:
		log.Printf("[MBOX-IMPORT] file=%q is a spool directory: not renamed", path)
	case stopped || c.StoppedEarly || c.Errors > 0 || waitErr != nil:
		log.Printf("[MBOX-IMPORT] file=%q not marked done (stopped=%t stopped-early=%t errors=%d commit-wait-error=%t)",
			path, stopped, c.StoppedEarly, c.Errors, waitErr != nil)
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

// unpackDisc unpacks the whole disc image into dir with `7z x -y -bd -o<dir> -- IMAGE` and
// waits for the program; its per-file output is dropped, the tail of its stderr goes into
// the error. The image can hold millions of files (utzoo: 5.2 GB in 2.08M files), so the
// shutdown must not wait for it: when stop is closed the process group is killed and
// errStopped is returned. 7z runs in a process group of its own, so Ctrl-C at a terminal
// does not reach it directly and this kill is what ends it.
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

	stderr := &tailBuffer{}
	cmd := exec.Command(prog, "x", "-y", "-bd", "-o"+dir, "--", image)
	cmd.Stdout = io.Discard // the per-file listing
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
	case err := <-done:
		if err != nil {
			if msg := stderr.String(); msg != "" {
				return fmt.Errorf("7z x: %w: %s", err, msg)
			}
			return fmt.Errorf("7z x: %w", err)
		}
	case <-stop:
		killGroup(cmd)
		<-done // reap it; WaitDelay bounds the wait for its output copies
		return errStopped
	}
	log.Printf("[MBOX-IMPORT] unpacked disc file=%q to %q", image, dir)
	return nil
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
