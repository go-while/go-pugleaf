package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-while/mbox2nntp/uat"
)

// uatSuffixes are the input suffixes of Usenet Archive Toolkit archives: the plain
// package, and the .xz (usenet.nereid.pl) and .7z (archive.org) downloads.
var uatSuffixes = []string{".usenet", ".usenet.xz", ".usenet.7z"}

// uatSuffix returns the archive suffix of a UAT input (".usenet", ".usenet.xz" or
// ".usenet.7z"; a trailing ".done" is ignored) and "" for any other input.
func uatSuffix(path string) string {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".done")
	for _, s := range uatSuffixes {
		if strings.HasSuffix(name, s) {
			return s
		}
	}
	return ""
}

// uatUnpackedName is the file name of the unpacked package of a .usenet.xz or .usenet.7z
// archive: the archive's base name without ".done" and the compression suffix
// (NAME.usenet.xz -> NAME.usenet); for 7z it is also the entry looked for.
func uatUnpackedName(path string) string {
	name := filepath.Base(path)
	if strings.HasSuffix(strings.ToLower(name), ".done") {
		name = name[:len(name)-len(".done")]
	}
	return name[:len(name)-len(filepath.Ext(name))]
}

// importUAT imports one Usenet Archive Toolkit archive like importInput imports an mbox:
// the same sink, counters, mark-done rule and summary line. A .usenet.xz or .usenet.7z
// archive is unpacked into a private directory under opts.tmpDir first, which is removed
// on every path; the archive given (not the unpacked copy) is what gets renamed .done.
// c is nil when the archive could not be unpacked or opened.
func importUAT(path string, opts *runOptions, sink articleSink, stop <-chan struct{}) (c *counters, inputErr, stopped bool) {
	suffix := uatSuffix(path)
	zstd, err := exec.LookPath(opts.zstd) // before anything is unpacked
	if err != nil {
		log.Printf("[MBOX-IMPORT] input error: file=%q: zstd program: %v", path, err)
		return nil, true, false
	}

	file := path
	workDir := opts.tmpDir // where Each writes the zstd dictionary
	if suffix != ".usenet" {
		dir, err := os.MkdirTemp(opts.tmpDir, "mbox-importer-")
		if err != nil {
			log.Printf("[MBOX-IMPORT] input error: file=%q: %v", path, err)
			return nil, true, false
		}
		defer func() {
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("[MBOX-IMPORT] file=%q: removing the unpack directory: %v", path, err)
			}
		}()
		workDir = dir
		file = filepath.Join(dir, uatUnpackedName(path))
		log.Printf("[MBOX-IMPORT] unpacking file=%q to %q", path, file)
		if suffix == ".usenet.xz" {
			err = unpackXz(path, file, opts.xz, opts.sevenZip)
		} else {
			err = unpack7z(path, file, opts.sevenZip)
		}
		if err != nil {
			// Ctrl-C at a terminal also kills the unpack program (same process group)
			select {
			case <-stop:
				log.Printf("[MBOX-IMPORT] file=%q: unpack ended by the shutdown signal: %v", path, err)
				return nil, false, true
			default:
			}
			log.Printf("[MBOX-IMPORT] input error: file=%q: unpack: %v", path, err)
			return nil, true, false
		}
	}

	p, err := uat.Open(file)
	if err != nil {
		log.Printf("[MBOX-IMPORT] input error: file=%q: %v", path, err)
		return nil, true, false
	}
	log.Printf("[MBOX-IMPORT] importing file=%q", path)
	log.Printf("[MBOX-IMPORT] uat file=%q group=%q version=%d messages=%d", path, p.Name, p.Version, p.Count())

	c = newCounters(path)
	var encoded, addedMIME int
	raw := make(map[string]int)
	sink.beginInput()
	eachErr := p.Each(zstd, workDir, func(i int, msg []byte) error {
		select {
		case <-stop:
			return errStopped
		default:
		}
		c.Read++
		head, body, info := uat.Fix(msg)
		encoded += info.Encoded
		if info.AddedMIMEVersion {
			addedMIME++
		}
		for _, name := range info.Raw {
			raw[name]++
		}
		body = removeFinalEmptyLines(body)
		isBinary := !opts.keepBinary && hasYEnc(body)
		if isBinary {
			body = nil
		}
		handleLines(c, head, body, isBinary, opts, sink)
		if c.Read%progressEvery == 0 {
			log.Printf("[MBOX-IMPORT] progress file=%q read=%d of %d stored=%d present=%d skipped=%d rejected=%d errors=%d",
				path, c.Read, p.Count(), c.Stored, c.Present, c.skippedTotal(), c.Rejected, c.Errors)
		}
		return nil
	})
	switch {
	case errors.Is(eachErr, errStopped):
		stopped = true
	case eachErr != nil:
		// Ctrl-C at a terminal also kills zstd (same process group): a stop, not an input error
		select {
		case <-stop:
			stopped = true
			log.Printf("[MBOX-IMPORT] file=%q: read ended by the shutdown signal: %v", path, eachErr)
		default:
			c.StoppedEarly = true
			inputErr = true
			log.Printf("[MBOX-IMPORT] input error: file=%q: %v", path, eachErr)
		}
	}
	closeErr := p.Close()
	if closeErr != nil {
		log.Printf("[MBOX-IMPORT] input error: file=%q: %v", path, closeErr)
		inputErr = true
	}
	waitErr := sink.endInput(opts.commitTimeout)
	if waitErr != nil {
		log.Printf("[MBOX-IMPORT] file=%q commit wait: %v", path, waitErr)
		c.CommitFailed = true
	}

	switch {
	case opts.dryRun || !opts.markDone:
	case stopped || c.StoppedEarly || closeErr != nil || c.Errors > 0 || waitErr != nil:
		log.Printf("[MBOX-IMPORT] file=%q not marked done (stopped=%t stopped-early=%t unread=%t close-error=%t errors=%d commit-wait-error=%t)",
			path, stopped, c.StoppedEarly, c.Unread, closeErr != nil, c.Errors, waitErr != nil)
	case strings.HasSuffix(path, ".done"):
		log.Printf("[MBOX-IMPORT] file=%q already named .done: not renamed", path)
	default:
		if err := markDone(path); err != nil {
			log.Printf("[MBOX-IMPORT] file=%q not marked done: %v", path, err)
			if !errors.Is(err, os.ErrExist) {
				inputErr = true
			}
		} else {
			c.MarkedDone = true
		}
	}

	log.Printf("[MBOX-IMPORT] file=%q read=%d stored=%d present=%d skipped=%d (%s) rejected=%d errors=%d parser-skipped=%d near-misses=%d stopped-early=%t unread=%t marked-done=%t",
		path, c.Read, c.Stored, c.Present, c.skippedTotal(), c.skippedDetail(), c.Rejected, c.Errors,
		c.ParserSkipped, c.NearMisses, c.StoppedEarly, c.Unread, c.MarkedDone)
	log.Printf("[MBOX-IMPORT] uat file=%q re-encoded-fields=%d added-mime-version=%d raw-fields=%s",
		path, encoded, addedMIME, rawFieldsDetail(raw))
	return c, inputErr, stopped
}

// rawFieldsDetail lists the header fields uat.Fix left with non-ASCII in them as
// "name=count,..." sorted by name, or "-".
func rawFieldsDetail(raw map[string]int) string {
	if len(raw) == 0 {
		return "-"
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s=%d", name, raw[name])
	}
	return strings.Join(parts, ",")
}

// unpackXz writes the decompressed .xz archive to dst with the xz program, or with
// 7-Zip (-txz) when xz is not found.
func unpackXz(archive, dst, xz, sevenZip string) error {
	prog, err := exec.LookPath(xz)
	if err == nil {
		return runTo(dst, prog, "-dc", "--", archive)
	}
	prog7, err7 := exec.LookPath(sevenZip)
	if err7 != nil {
		return fmt.Errorf("no xz program (%v) and no 7-Zip program (%v): install one, or give its path with -xz or -7z", err, err7)
	}
	return runTo(dst, prog7, "e", "-so", "-bd", "-txz", "--", archive)
}

// unpack7z writes the entry named like dst of a 7z archive to dst. The archive is listed
// first, because 7z succeeds and writes nothing when asked for a name that is not there;
// "--" ends the switches and -spd turns off wildcards in the entry name.
func unpack7z(archive, dst, sevenZip string) error {
	prog, err := exec.LookPath(sevenZip)
	if err != nil {
		return fmt.Errorf("7z program: %w", err)
	}
	name := filepath.Base(dst)
	stderr := &tailBuffer{}
	list := exec.Command(prog, "l", "-slt", "-spd", "--", archive)
	list.Stderr = stderr
	list.WaitDelay = 10 * time.Second
	out, err := list.Output()
	if err != nil {
		return fmt.Errorf("%s l: %w: %s", filepath.Base(prog), err, stderr.String())
	}
	if !has7zEntry(string(out), name) {
		return fmt.Errorf("no entry %q in the archive", name)
	}
	return runTo(dst, prog, "e", "-so", "-bd", "-spd", "--", archive, name)
}

// has7zEntry reports whether the output of "7z l -slt" lists a top-level entry called
// name. Entries follow the "----------" line, one "Path = ..." line each; the
// "Path = " lines above it are the archive's own.
func has7zEntry(listing, name string) bool {
	entries := false
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "----------" {
			entries = true
		} else if entries && line == "Path = "+name {
			return true
		}
	}
	return false
}

// runTo runs prog with args and streams its standard output to the new file dst. A failing
// program is an error even when it wrote something (its stderr tail is in the error), and
// so is an empty result. The process is killed when writing fails. dst is removed on error.
func runTo(dst, prog string, args ...string) (err error) {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	stderr := &tailBuffer{}
	cmd := exec.Command(prog, args...)
	cmd.Stderr = stderr
	cmd.WaitDelay = 10 * time.Second // Wait must not hang on children keeping the pipes open
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = out.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = out.Close()
		return fmt.Errorf("%s: %w", filepath.Base(prog), err)
	}
	n, werr := out.ReadFrom(stdout)
	if werr != nil {
		_ = cmd.Process.Kill() // it would block on a full pipe
	}
	if cerr := out.Close(); cerr != nil && werr == nil {
		werr = cerr
	}
	waitErr := cmd.Wait()
	if werr != nil {
		return fmt.Errorf("writing %s: %w", dst, werr) // the wait error follows from the kill
	}
	if waitErr != nil {
		if msg := stderr.String(); msg != "" {
			return fmt.Errorf("%s %s: %w: %s", filepath.Base(prog), args[0], waitErr, msg)
		}
		return fmt.Errorf("%s %s: %w", filepath.Base(prog), args[0], waitErr)
	}
	if n == 0 {
		return fmt.Errorf("%s %s wrote nothing", filepath.Base(prog), args[0])
	}
	return nil
}

// hasYEnc reports a yEnc body: a line starting with "=ybegin".
func hasYEnc(body []string) bool {
	for _, line := range body {
		if strings.HasPrefix(line, "=ybegin") {
			return true
		}
	}
	return false
}
