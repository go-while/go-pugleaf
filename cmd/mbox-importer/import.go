package main

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// progressEvery is the number of messages between two progress lines of an input.
const progressEvery = 10000

// maxNearMissSamples is the most near-miss samples logged per input.
const maxNearMissSamples = 5

// errStopped is returned by readMbox when the stop channel was closed between articles.
var errStopped = errors.New("stopped by shutdown signal")

// errRejected marks a sink error for an article that can never be stored (the processor's
// bulk checks refused it, or it could not be built): counted as rejected, not as an error.
var errRejected = errors.New("rejected")

// counters are the per-input (and total) results.
type counters struct {
	File                                                               string
	Group                                                              string // forced target group of the input (-group, -group-from-name), "" otherwise
	Read, Stored, Present, Rejected, Errors, ParserSkipped, NearMisses int
	Skipped                                                            map[skipReason]int
	StoppedEarly, Unread, MarkedDone                                   bool
	CommitFailed                                                       bool // the commit wait of the input failed
	// news spool inputs (-spool, disc images; plan mbox-import-discs), 0 for an mbox input:
	Files        int // regular files walked
	NotArticle   int // files that are no article, A-news batch or mbox (binaries, tar, README, ...)
	XrefPointers int // NetNews/CD "<n>.XRF" crosspost pointer files (not read)
	ANewsBatches int // A-news batch files (several articles each)
}

// spoolDetail is the spool part of the per-input log line ("" for an mbox input).
func (c *counters) spoolDetail() string {
	if c.Files == 0 {
		return ""
	}
	return fmt.Sprintf(" files=%d not-article=%d xref-pointers=%d anews-batches=%d",
		c.Files, c.NotArticle, c.XrefPointers, c.ANewsBatches)
}

func newCounters(file string) *counters {
	return &counters{File: file, Skipped: make(map[skipReason]int)}
}

// skippedTotal is the sum of the skip counts.
func (c *counters) skippedTotal() int {
	n := 0
	for _, v := range c.Skipped {
		n += v
	}
	return n
}

// skippedDetail lists the non-zero skip counts as "name=count,..." in reason order, or "-".
func (c *counters) skippedDetail() string {
	var parts []string
	for r := skipNoMessageID; r <= skipLast; r++ {
		if n := c.Skipped[r]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", r, n))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

// add adds the counts of o to c (the flags are not summed).
func (c *counters) add(o *counters) {
	c.Read += o.Read
	c.Stored += o.Stored
	c.Present += o.Present
	c.Rejected += o.Rejected
	c.Errors += o.Errors
	c.ParserSkipped += o.ParserSkipped
	c.NearMisses += o.NearMisses
	c.Files += o.Files
	c.NotArticle += o.NotArticle
	c.XrefPointers += o.XrefPointers
	c.ANewsBatches += o.ANewsBatches
	for r, n := range o.Skipped {
		c.Skipped[r] += n
	}
}

// runSummary is the result of one run (all inputs; with -follow the scans added up).
type runSummary struct {
	Inputs, InputErrors, MarkedDone int
	CommitErrors                    int // inputs whose commit wait failed
	AlreadyImported                 int // inputs the tracking DB knows as done (same file)
	Unsupported                     int // planned inputs the importer cannot read at all
	Pending                         int // planned inputs worth another look on the next scan
	HashStopped                     int // -verify-md5: inputs whose hashing the signal ended
	HeldByRun                       int // inputs another live run of the tracking DB holds
	Scans                           int // scans of this run (1 without -follow)
	Stopped                         bool
	Total                           *counters
	PerInput                        []*counters
}

// addScan adds the result of one scan to r: with -follow a process imports one scan after
// the other, and the run row of the tracking DB holds the totals of all of them.
func (r *runSummary) addScan(o *runSummary) {
	if o == nil {
		return
	}
	r.Scans++
	r.Inputs += o.Inputs
	r.InputErrors += o.InputErrors
	r.MarkedDone += o.MarkedDone
	r.CommitErrors += o.CommitErrors
	r.AlreadyImported += o.AlreadyImported
	r.Unsupported += o.Unsupported
	r.Pending += o.Pending
	r.HashStopped += o.HashStopped
	r.HeldByRun += o.HeldByRun
	r.Stopped = r.Stopped || o.Stopped
	r.PerInput = append(r.PerInput, o.PerInput...)
	if o.Total != nil {
		r.Total.add(o.Total)
	}
}

// exitCode is the exit code of a run that got past the database and processor setup:
// 130 when stopped, 1 on an input error, an article error or a failed commit wait, else 0.
func (r *runSummary) exitCode() int {
	switch {
	case r.Stopped:
		return 130
	case r.InputErrors > 0, r.CommitErrors > 0:
		return 1
	case r.Total != nil && r.Total.Errors > 0:
		return 1
	}
	return 0
}

// lastRun is the summary of the last runWith that imported (tests read it after runWith returned).
var lastRun *runSummary

// articleSink receives every importable article of a run. beginInput opens the state of one
// input; with -parallel several inputs are open at once, so a sink must allow concurrent
// beginInput calls and concurrent use of the inputSinks it returned.
type articleSink interface {
	// beginInput is called before the first article of an input and returns the sink the
	// articles of that input are handed to.
	beginInput() inputSink
}

// inputSink is the sink of one input: the dry run counts, the real run looks the id up,
// creates the group, takes the commit mark and calls ProcessArticle.
// A Handle error wrapping errRejected counts as rejected, any other error as an error.
type inputSink interface {
	Handle(msgid string, art func() (*models.Article, error), group string) (stored, present bool, err error)
	// endInput waits until the articles handed over for the input are committed.
	endInput(timeout time.Duration) error
}

// runOptions are the parsed flags of one run.
type runOptions struct {
	dataDir, hostname, group, sevenZip string
	zstd, xz, tmpDir                   string // UAT archives: programs and the unpack directory ("" = system temp)
	useShortHashLen                    int
	parallel                           int  // inputs imported at the same time (<= 1: one after the other)
	groupFromName                      bool // -group-from-name: the target group of an input is its file name (inputOptions)
	spool                              bool // -spool: a positional directory or .zip input is a news spool (disc.go)
	history, dryRun, keepBinary        bool
	markDone, strictMsgID              bool
	tracked                            bool          // the tracking DB is on: it, not the rename, holds the import state
	verifyMD5                          bool          // -verify-md5: hash an input before importing it (-from-iafetch)
	follow                             time.Duration // -follow: wait this long, then scan the source again; 0: one scan
	commitTimeout                      time.Duration // 0: no limit
	maxArticleSize                     int           // -max-article-size in bytes, header and body; 0: no limit
}

// readMbox reads one input and feeds the sink. It returns errStopped (with the counters
// so far) when stop was closed between two articles; the current article is finished first.
func readMbox(src *source, opts *runOptions, sink inputSink, stop <-chan struct{}) (*counters, error) {
	c := newCounters(src.Path)
	if src.Stream == nil {
		return c, nil // empty input
	}
	lastErr := ""
	for {
		select {
		case <-stop:
			return c, errStopped
		default:
		}
		msg, err := src.Stream.ReadMessage()
		if err == io.EOF {
			return c, nil
		}
		if err != nil {
			if err.Error() == lastErr {
				log.Printf("[MBOX-IMPORT] file=%q parser stuck (same error twice): %v", src.Path, err)
				c.StoppedEarly = true
				return c, nil
			}
			lastErr = err.Error()
			c.ParserSkipped++
			log.Printf("[MBOX-IMPORT] file=%q parser skipped a message: %v", src.Path, err)
			continue
		}
		lastErr = ""
		c.Read++
		handleMessage(c, msg.Headers(), msg.BodyReader(), opts, sink)
		if c.Read%progressEvery == 0 {
			log.Printf("[MBOX-IMPORT] progress file=%q read=%d stored=%d present=%d skipped=%d rejected=%d errors=%d",
				src.Path, c.Read, c.Stored, c.Present, c.skippedTotal(), c.Rejected, c.Errors)
		}
	}
}

// handleMessage checks one message, hands it to the sink and counts the outcome.
// The body is always drained. An article over -max-article-size is skipped as "too-big";
// one written by mbox2nntp's exporters (X-Mbox2nntp-Format: mboxrd) is unquoted first.
func handleMessage(c *counters, rawHead []string, bodyReader io.Reader, opts *runOptions, sink inputSink) {
	maxBody := -1 // no limit
	if opts.maxArticleSize > 0 {
		maxBody = opts.maxArticleSize
		for _, l := range rawHead {
			maxBody -= len(l) + 1
		}
		maxBody = max(maxBody, 0)
	}
	body, isBinary, tooBig := readBody(bodyReader, opts.keepBinary, maxBody)
	if tooBig {
		c.Skipped[skipTooBig]++
		msgid, _ := headerField(rawHead, "Message-ID")
		log.Printf("[MBOX-IMPORT] skipped msgid=%q: article over -max-article-size %d bytes", msgid, opts.maxArticleSize)
		return
	}
	if isMboxrdQuoted(rawHead) {
		for i, l := range rawHead {
			rawHead[i] = mboxrdUnquote(l)
		}
		for i, l := range body {
			body[i] = mboxrdUnquote(l)
		}
	}
	handleLines(c, rawHead, body, isBinary, opts, sink)
}

// handleLines runs one article (header lines and body lines, no line endings) through the
// skip rules, the header rewrite, the target-group rule and the sink. isBinary says the body
// was yEnc and left out (readBody), so the article counts as "binary", not "no-body".
func handleLines(c *counters, rawHead, body []string, isBinary bool, opts *runOptions, sink inputSink) {
	msgid, reason := messageIDFromHeaders(rawHead, opts.strictMsgID)
	if reason != skipNone {
		c.Skipped[reason]++
		return
	}
	if isBinary {
		c.Skipped[skipBinary]++
		return
	}
	if len(body) == 0 {
		c.Skipped[skipNoBody]++
		return
	}
	head, reason := rewriteHead(msgid, rawHead)
	if reason != skipNone {
		c.Skipped[reason]++
		return
	}
	group, reason := firstNewsgroup(head)
	if opts.group != "" {
		// the processor requires the header even when the target group is forced
		if reason == skipNoNewsgroups {
			c.Skipped[reason]++
			return
		}
		group = opts.group
	} else if reason != skipNone {
		c.Skipped[reason]++
		return
	}

	build := func() (*models.Article, error) { return buildArticle(msgid, head, body) }
	stored, present, err := sink.Handle(msgid, build, group)
	switch {
	case errors.Is(err, errRejected):
		c.Rejected++
		log.Printf("[MBOX-IMPORT] rejected msgid=%s group=%s: %v", msgid, group, err)
	case err != nil:
		c.Errors++
		log.Printf("[MBOX-IMPORT] error msgid=%s group=%s: %v", msgid, group, err)
	case present:
		c.Present++
	case stored:
		c.Stored++
	default:
		c.Errors++
		log.Printf("[MBOX-IMPORT] error msgid=%s group=%s: neither stored nor present", msgid, group)
	}
}

// maxParallel is the upper bound of -parallel.
const maxParallel = 32

// importInputs imports the files of a run without a tracking DB (the paths are turned into
// plannedInputs of source cli).
func importInputs(files []string, opts *runOptions, sink articleSink, stop <-chan struct{}) *runSummary {
	return importPlanned(plannedFromPaths(files), opts, sink, stop, nil)
}

// importPlanned imports inputs until stop is closed: one after the other, or opts.parallel
// at a time, each worker taking the next input in the given order. Once stop is closed no
// further input is started. The summary lists the inputs in the given order, whatever order
// they finished in.
//
// With a tracking DB (track != nil) every input is claimed first: one that the DB knows as
// done (the same file) is counted as already imported and not read again, one another live
// run holds is left to that run, and an input the importer cannot read is only recorded.
// A nil track imports every input, as a run without the tracking DB does.
func importPlanned(inputs []plannedInput, opts *runOptions, sink articleSink, stop <-chan struct{}, track *tracker) *runSummary {
	inputs = dedupePlanned(inputs)
	res := &runSummary{Total: newCounters("")}
	type outcome struct {
		c                                                *counters
		started, inputErr, stopped                       bool
		alreadyImported, heldByRun, unsupported, pending bool
		hashStopped                                      bool // -verify-md5: the signal ended the hashing
	}
	outcomes := make([]outcome, len(inputs))

	workers := opts.parallel
	if workers < 1 {
		workers = 1
	}
	if workers > len(inputs) {
		workers = len(inputs)
	}
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				select {
				case <-stop:
					continue // drained, never started
				default:
				}
				o := &outcomes[i]
				in := inputs[i]
				if !in.Supported {
					if err := track.recordPlanned(in); err != nil {
						track.trackErr(fmt.Sprintf("input %q", in.Path), err)
					}
					if plannedPending(in) {
						o.pending = true
					} else {
						o.unsupported = true
					}
					continue
				}
				id, state, prev, err := track.claimInputRow(in)
				if err != nil {
					// not knowing the state is no reason to skip the input
					track.trackErr(fmt.Sprintf("claim input %q", in.Path), err)
				}
				switch state {
				case claimDone:
					o.alreadyImported = true
					continue
				case claimHeld:
					o.heldByRun = true
					continue
				}
				// the claim came first on purpose: only an input this run is really going
				// to read is hashed, so a done input is never hashed again (-verify-md5)
				if opts.verifyMD5 {
					switch verifyClaimedMD5(track, id, in, prev, stop) {
					case md5Released:
						o.pending = true
						continue
					case md5Stopped:
						o.hashStopped = true
						continue
					}
				}
				o.started = true
				switch spool := spoolInput(in.Path, opts); {
				case uatSuffix(in.Path) != "":
					o.c, o.inputErr, o.stopped = importUAT(in.Path, opts, sink, stop)
				case spool != spoolNone:
					// a disc image, or a directory or zip file of -spool (disc.go)
					o.c, o.inputErr, o.stopped = importSpool(in.Path, spool, opts, sink, stop)
				default:
					o.c, o.inputErr, o.stopped = importInput(in.Path, opts, sink, stop)
				}
				track.finishInput(id, o.c, o.inputErr, o.stopped)
			}
		}()
	}
feed:
	for i := range inputs {
		select {
		case <-stop:
			break feed
		default:
		}
		select {
		case <-stop:
			break feed
		case next <- i:
		}
	}
	close(next)
	wg.Wait()

	notStarted := 0
	for _, o := range outcomes {
		switch {
		case o.unsupported:
			res.Unsupported++
			continue
		case o.pending:
			res.Pending++
			continue
		case o.hashStopped:
			// the shutdown signal ended the hashing before the input was read: no input
			// was in progress, so this alone does not make the run a stopped one
			res.HashStopped++
			continue
		case o.alreadyImported:
			res.AlreadyImported++
			continue
		case o.heldByRun:
			res.HeldByRun++
			continue
		case !o.started:
			notStarted++
			continue
		}
		res.Inputs++
		if o.inputErr {
			res.InputErrors++
		}
		if o.c != nil {
			res.PerInput = append(res.PerInput, o.c)
			res.Total.add(o.c)
			if o.c.MarkedDone {
				res.MarkedDone++
			}
			if o.c.CommitFailed {
				res.CommitErrors++
			}
		}
		if o.stopped {
			res.Stopped = true
		}
	}
	if notStarted > 0 {
		res.Stopped = true
		log.Printf("[MBOX-IMPORT] stopped by shutdown signal: %d inputs not started", notStarted)
	}
	if res.AlreadyImported > 0 || res.Unsupported > 0 || res.Pending > 0 || res.HeldByRun > 0 {
		log.Printf("[MBOX-IMPORT] tracking: not imported: already-imported=%d pending=%d unsupported=%d held-by-another-run=%d",
			res.AlreadyImported, res.Pending, res.Unsupported, res.HeldByRun)
	}
	return res
}

// scanFunc returns the inputs of one scan of the source: iafetch's DB with -from-iafetch
// (iafetchInputs). It is nil for a run whose inputs are the positional arguments, which are
// read once and never scanned again.
type scanFunc func() ([]plannedInput, error)

// followWait waits for the next scan of -follow: it returns true when d has passed and false
// when the stop channel was closed first (the run then ends without an error). Tests replace
// it with a channel they control, so no test has to wait for a clock.
var followWait = func(d time.Duration, stop <-chan struct{}) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-stop:
		return false
	}
}

// importScans imports the inputs of one scan after the other: the inputs of first, and with
// -follow (opts.follow > 0 and a scan function) the inputs of a new scan after every wait,
// until the stop channel is closed. The returned summary is the total of every scan, because
// one process is one run of the tracking DB.
//
// A failed later scan is not fatal: iafetch keeps writing its DB while the importer runs, so
// the error is logged and the next wait scans again, without a round of its own (the first
// scan is checked by the caller, before the run starts). Exit code 130 is for a stop inside
// an input only: a stop while waiting, between two scans, or while an input was being hashed
// for -verify-md5 ends the run without an error.
func importScans(first []plannedInput, scan scanFunc, opts *runOptions, sink articleSink, stop <-chan struct{}, track *tracker) *runSummary {
	total := &runSummary{Total: newCounters("")}
	planned := first
	for round := 1; ; round++ {
		if scan != nil {
			logPlannedCounts(round, planned)
		}
		res := importPlanned(planned, opts, sink, stop, track)
		if scan != nil {
			logScanTotals(round, res)
		}
		total.addScan(res)
		if scan == nil || opts.follow <= 0 || total.Stopped {
			return total
		}
		next, ok := followScan(scan, opts.follow, stop, total.Scans)
		if !ok {
			return total
		}
		planned = next
	}
}

// followScan waits follow and scans again, until a scan succeeds; it reports false when the
// run is to end instead (the stop channel closed while it waited, while it scanned, or
// between the two). A failed scan is logged and tried again after the next wait.
func followScan(scan scanFunc, follow time.Duration, stop <-chan struct{}, scans int) ([]plannedInput, bool) {
	for {
		if !followWait(follow, stop) {
			log.Printf("[MBOX-IMPORT] -follow: shutdown signal while waiting %v for the next scan: %d scan(s) done",
				follow, scans)
			return nil, false
		}
		next, err := scan()
		if err != nil {
			log.Printf("[MBOX-IMPORT] -follow: scan failed after %d scan(s), trying again in %v: %v",
				scans, follow, err)
			continue
		}
		// the signal may have arrived while the scan was running: no further input is
		// started, and nothing was in progress, so the run ends without an error
		select {
		case <-stop:
			log.Printf("[MBOX-IMPORT] -follow: shutdown signal before the next scan was imported: %d scan(s) done", scans)
			return nil, false
		default:
		}
		return next, true
	}
}

// logPlannedCounts logs what a scan found before anything is imported: the inputs the
// importer can read, the ones it will look at again on the next scan (pending: the file is
// missing, has another size or failed -verify-md5) and the ones it cannot read at all, with
// the count per reason in reason order.
func logPlannedCounts(round int, planned []plannedInput) {
	supported, pending, unsupported := 0, 0, 0
	pendingBy, unsupportedBy := map[string]int{}, map[string]int{}
	for _, in := range planned {
		switch {
		case in.Supported:
			supported++
		case plannedPending(in):
			pending++
			pendingBy[in.Reason]++
		default:
			unsupported++
			unsupportedBy[in.Reason]++
		}
	}
	log.Printf("[MBOX-IMPORT] scan %d: %d planned input(s): supported=%d pending=%d unsupported=%d",
		round, len(planned), supported, pending, unsupported)
	if pending > 0 {
		log.Printf("[MBOX-IMPORT] scan %d: pending %s", round, countsByReason(pendingBy))
	}
	if unsupported > 0 {
		log.Printf("[MBOX-IMPORT] scan %d: unsupported %s", round, countsByReason(unsupportedBy))
	}
}

// countsByReason formats a reason histogram as "reason=count ..." in reason order.
func countsByReason(by map[string]int) string {
	reasons := make([]string, 0, len(by))
	for r := range by {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		name := r
		if name == "" {
			name = "(no reason)"
		}
		parts = append(parts, fmt.Sprintf("%s=%d", name, by[r]))
	}
	return strings.Join(parts, " ")
}

// logScanTotals logs the result of one scan: what it imported and what it left alone.
func logScanTotals(round int, r *runSummary) {
	t := r.Total
	if t == nil {
		t = newCounters("")
	}
	log.Printf("[MBOX-IMPORT] scan %d done: imported=%d already-imported=%d pending=%d unsupported=%d held-by-another-run=%d md5-stopped=%d input-errors=%d read=%d stored=%d present=%d errors=%d",
		round, r.Inputs, r.AlreadyImported, r.Pending, r.Unsupported, r.HeldByRun, r.HashStopped,
		r.InputErrors, t.Read, t.Stored, t.Present, t.Errors)
}

// Reasons of -verify-md5 (plannedInput.Reason). Both are pending reasons: the recorded md5
// belongs to the file iafetch downloaded, so a file that does not match it (or cannot be
// read) is worth another look on the next scan instead of being written off.
const (
	reasonMD5Mismatch = "md5-mismatch" // the file's md5 is not the one the source recorded
	reasonMD5Error    = "md5-error"    // the file could not be read for hashing
)

// md5ChunkSize is how much of a file is hashed between two checks of the stop channel.
const md5ChunkSize = 1 << 20

// plannedPending reports whether an input the importer is not going to read now is worth
// another look on the next scan: iafetch's own "the file on disk is not ready yet" reasons
// (iafetchPending) and the two -verify-md5 reasons. The format reasons (iso, txt, ...) are
// final: there is no reader for such a name.
func plannedPending(in plannedInput) bool {
	if in.Supported {
		return false
	}
	return iafetchPending(in.Reason) || in.Reason == reasonMD5Mismatch || in.Reason == reasonMD5Error
}

// md5Result is what -verify-md5 decided about an input this run has claimed.
type md5Result int

const (
	md5Pass     md5Result = iota // the sum matches (or there is none to check): import it
	md5Released                  // put back to pending: another sum, or the file could not be read
	md5Stopped                   // the shutdown signal ended the hashing: the row says stopped
)

// verifyFileMD5 is the hash function of -verify-md5; tests replace it to count the files
// that are really hashed and to stop a run inside the hashing.
var verifyFileMD5 = fileMD5

// verifyClaimedMD5 checks the md5 of an input this run has claimed (claimTake), before the
// input is read. The claim comes first so that only a file that is really going to be
// imported is hashed; a file the tracking DB knows as done is never hashed again.
//
// A mismatch is no input error: the row goes back to pending with the reason
// reasonMD5Mismatch and the sum that was read (md5_seen), so a file iafetch is replacing
// right now is imported on a later scan — and an unchanged file that still does not match
// is not hashed again (md5KnownMismatch). A file that cannot be read becomes
// pending/reasonMD5Error. The shutdown signal during the hashing leaves the row "stopped"
// (nothing of the input was read), which is not a stopped import: no input was in progress.
func verifyClaimedMD5(track *tracker, id int64, in plannedInput, prev claimPrev, stop <-chan struct{}) md5Result {
	if in.MD5 == "" {
		log.Printf("[MBOX-IMPORT] file=%q -verify-md5: the source recorded no md5: not checked", in.Path)
		return md5Pass
	}
	if md5KnownMismatch(in, prev) {
		log.Printf("[MBOX-IMPORT] file=%q -verify-md5: unchanged since its md5 %s did not match the recorded %s: not hashed again, still pending",
			in.Path, prev.md5Seen, in.MD5)
		track.releaseInput(id, reasonMD5Mismatch, prev.md5Seen)
		return md5Released
	}
	sum, err := verifyFileMD5(in.Path, stop)
	switch {
	case errors.Is(err, errStopped):
		log.Printf("[MBOX-IMPORT] file=%q -verify-md5: hashing ended by the shutdown signal: not imported", in.Path)
		track.finishInput(id, nil, false, true)
		return md5Stopped
	case err != nil:
		log.Printf("[MBOX-IMPORT] file=%q -verify-md5: %v: not imported, pending for the next scan", in.Path, err)
		track.releaseInput(id, reasonMD5Error, "")
		return md5Released
	case !strings.EqualFold(sum, in.MD5):
		log.Printf("[MBOX-IMPORT] file=%q md5 %s does not match the recorded %s: not imported, pending for the next scan",
			in.Path, sum, in.MD5)
		track.releaseInput(id, reasonMD5Mismatch, sum)
		return md5Released
	}
	return md5Pass
}

// md5KnownMismatch reports whether this file was hashed before and did not match: its row
// was left pending/md5-mismatch, the file still has the same size and mtime, the source
// still records the same md5, and the sum that was read then is still another one. Such a
// file is not read again (it would give the same sum). A re-download by iafetch changes the
// recorded md5, a repaired or replaced file its size or mtime, and either makes it hash
// again.
func md5KnownMismatch(in plannedInput, prev claimPrev) bool {
	return prev.status == trackPending && prev.reason == reasonMD5Mismatch &&
		prev.size == in.Size && prev.mtime == in.Mtime &&
		prev.md5 != "" && strings.EqualFold(prev.md5, in.MD5) &&
		prev.md5Seen != "" && !strings.EqualFold(prev.md5Seen, in.MD5)
}

// fileMD5 is the hex md5 of the file at path. It reads the file in md5ChunkSize blocks and
// checks stop between two of them, so hashing a multi-gigabyte input does not hold up the
// shutdown; it returns errStopped when stop was closed.
func fileMD5(path string, stop <-chan struct{}) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	// #nosec G401 -- md5 is the checksum archive.org publishes and iafetch records, not a security hash
	h := md5.New()
	buf := make([]byte, md5ChunkSize)
	for {
		select {
		case <-stop:
			return "", errStopped
		default:
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := h.Write(buf[:n]); werr != nil {
				return "", werr
			}
		}
		if errors.Is(rerr, io.EOF) {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if rerr != nil {
			return "", rerr
		}
	}
}

// inputOptions returns the options of one input: opts itself, or with -group-from-name a copy
// whose group is derived from the file name (an input whose name gives no valid group is an
// input error and is not opened).
func inputOptions(path string, opts *runOptions) (*runOptions, error) {
	if !opts.groupFromName {
		return opts, nil
	}
	group, err := groupFromFileName(path)
	if err != nil {
		return nil, err
	}
	in := *opts
	in.group = group
	log.Printf("[MBOX-IMPORT] file=%q group=%q (from the file name)", path, group)
	return &in, nil
}

// importInput opens, reads and closes one input, waits for its commits and marks it done.
// c is nil when the input could not be opened; inputErr reports an open, read or close error
// (or a failed rename); stopped reports that the stop channel ended the read.
func importInput(path string, opts *runOptions, sink articleSink, stop <-chan struct{}) (c *counters, inputErr, stopped bool) {
	opts, err := inputOptions(path, opts)
	if err != nil {
		log.Printf("[MBOX-IMPORT] input error: %v", err)
		return nil, true, false
	}
	src, err := openSource(path, opts.sevenZip)
	if err != nil {
		log.Printf("[MBOX-IMPORT] input error: %v", err)
		return nil, true, false
	}
	log.Printf("[MBOX-IMPORT] importing file=%q", path)
	in := sink.beginInput()
	c, readErr := readMbox(src, opts, in, stop)
	c.Group = opts.group
	stopped = errors.Is(readErr, errStopped)
	c.Unread = src.Unread() // before Close: Close kills a 7z that was not read to its end
	closeErr := src.Close()
	if closeErr != nil {
		// Ctrl-C at a terminal also kills the 7z process (same process group): a stop
		select {
		case <-stop:
			stopped = true
			log.Printf("[MBOX-IMPORT] file=%q: input ended by the shutdown signal: %v", path, closeErr)
		default:
			log.Printf("[MBOX-IMPORT] input error: file=%q: %v", path, closeErr)
			inputErr = true
		}
	}
	var samples []nearMiss
	if src.Stream != nil {
		n, s := src.Stream.NearMisses()
		c.NearMisses = n
		for _, nm := range s {
			if len(samples) == maxNearMissSamples {
				break
			}
			samples = append(samples, nearMiss{line: nm.Line, text: nm.Text})
		}
	}
	waitErr := in.endInput(opts.commitTimeout)
	if waitErr != nil {
		log.Printf("[MBOX-IMPORT] file=%q commit wait: %v", path, waitErr)
		c.CommitFailed = true
	}

	switch {
	case opts.dryRun || !opts.markDone:
	case stopped || c.StoppedEarly || c.Unread || closeErr != nil || c.Errors > 0 || waitErr != nil:
		log.Printf("[MBOX-IMPORT] file=%q not marked done (stopped=%t stopped-early=%t unread=%t close-error=%t errors=%d commit-wait-error=%t)",
			path, stopped, c.StoppedEarly, c.Unread, closeErr != nil, c.Errors, waitErr != nil)
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
	for _, nm := range samples {
		log.Printf("[MBOX-IMPORT]   near-miss file=%q line=%d %q", path, nm.line, nm.text)
	}
	return c, inputErr, stopped
}

// markDoneInput renames a fully imported input to INPUT.done. It reports whether the rename
// happened and whether the failure is an input error. An existing INPUT.done is never one.
// With the tracking DB on (tracked), a rename the file system refuses is not one either: the
// DB holds the import state, so a read-only input tree (the archive.org mirror) is fine.
func markDoneInput(path string, tracked bool) (marked, inputErr bool) {
	err := markDone(path)
	if err == nil {
		return true, false
	}
	log.Printf("[MBOX-IMPORT] file=%q not marked done: %v", path, err)
	switch {
	case errors.Is(err, os.ErrExist):
	case tracked && errors.Is(err, fs.ErrPermission):
		log.Printf("[MBOX-IMPORT] file=%q rename not permitted: the tracking database holds the import state", path)
	default:
		return false, true
	}
	return false, false
}

// nearMiss is one logged near-miss sample.
type nearMiss struct {
	line int
	text string
}

// logTotals logs the totals line of a run.
func logTotals(r *runSummary) {
	t := r.Total
	if t == nil {
		t = newCounters("")
	}
	log.Printf("[MBOX-IMPORT] done inputs=%d marked-done=%d input-errors=%d read=%d stored=%d present=%d skipped=%d rejected=%d errors=%d commit-errors=%d",
		r.Inputs, r.MarkedDone, r.InputErrors, t.Read, t.Stored, t.Present, t.skippedTotal(), t.Rejected, t.Errors, r.CommitErrors)
}

// logDryRunGroups logs the article count per target group, largest first.
func logDryRunGroups(groups map[string]int) {
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Slice(names, func(i, j int) bool {
		if groups[names[i]] != groups[names[j]] {
			return groups[names[i]] > groups[names[j]]
		}
		return names[i] < names[j]
	})
	log.Printf("[MBOX-IMPORT] dry-run: %d target groups", len(names))
	for _, g := range names {
		log.Printf("[MBOX-IMPORT] dry-run group=%q articles=%d", g, groups[g])
	}
}

// countingSink is the dry run's sink: it builds every article (a build error is a
// rejection) and counts it for its target group as stored. One countingSink serves every
// input of the run (the counts are per run), so the map is locked.
type countingSink struct {
	mu     sync.Mutex
	groups map[string]int
}

func newCountingSink() *countingSink {
	return &countingSink{groups: make(map[string]int)}
}

func (s *countingSink) Handle(msgid string, art func() (*models.Article, error), group string) (bool, bool, error) {
	if _, err := art(); err != nil {
		return false, false, fmt.Errorf("%w: %v", errRejected, err)
	}
	s.mu.Lock()
	s.groups[group]++
	s.mu.Unlock()
	return true, false, nil
}

func (s *countingSink) beginInput() inputSink { return s }

func (s *countingSink) endInput(time.Duration) error { return nil }

// processorSink is the real run's sink: it creates each group once per run and hands out
// one processorInput per input, which takes a commit mark per group, skips articles already
// in the group and passes the rest to ProcessArticle in bulk mode. Inputs may run at the
// same time (-parallel): ProcessArticle and the batch writer are built for concurrent callers,
// the per-run group set is locked here.
type processorSink struct {
	db      *database.Database
	proc    *processor.Processor
	mu      sync.Mutex      // ensured
	ensured map[string]bool // groups created or found this run
}

func newProcessorSink(db *database.Database, proc *processor.Processor) *processorSink {
	return &processorSink{db: db, proc: proc, ensured: make(map[string]bool)}
}

func (s *processorSink) beginInput() inputSink {
	return &processorInput{sink: s, marks: make(map[string]database.CommitMark)}
}

// ensure creates group once per run; concurrent inputs that meet the same new group wait
// for the first creation instead of racing EnsureNewsgroup.
func (s *processorSink) ensure(group string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ensured[group] {
		return nil
	}
	if err := processor.EnsureNewsgroup(s.db, group, ""); err != nil {
		return err
	}
	s.ensured[group] = true
	return nil
}

// processorInput is the processorSink state of one input: its commit marks per group.
type processorInput struct {
	sink  *processorSink
	marks map[string]database.CommitMark // commit marks of this input
	order []string                       // groups of this input, first sight order
}

func (p *processorInput) Handle(msgid string, art func() (*models.Article, error), group string) (bool, bool, error) {
	s := p.sink
	if err := s.ensure(group); err != nil {
		return false, false, fmt.Errorf("create newsgroup: %w", err)
	}
	if _, ok := p.marks[group]; !ok {
		p.marks[group] = s.db.Batch.CommitMark(group)
		p.order = append(p.order, group)
	}

	groupDB, err := s.db.GetGroupDB(group)
	if err != nil {
		if groupDB != nil {
			groupDB.Return()
		}
		return false, false, fmt.Errorf("open group database: %w", err)
	}
	found, err := groupDB.LookupMsgIdInArticlesDB(msgid)
	groupDB.Return()
	if err != nil {
		return false, false, fmt.Errorf("message-id lookup: %w", err)
	}
	if found {
		return false, true, nil
	}

	article, err := art()
	if err != nil {
		return false, false, fmt.Errorf("%w: %v", errRejected, err)
	}
	// the capture goroutine owns the article after the call: never read it again
	res, err := s.proc.ProcessArticle(article, group, true)
	switch {
	case res == history.CasePass && err == nil:
		return true, false, nil
	case res == history.CaseError:
		return false, false, fmt.Errorf("%w: %v", errRejected, err)
	case err != nil:
		return false, false, fmt.Errorf("process (case %d): %w", res, err)
	}
	return false, false, fmt.Errorf("process: unexpected case %d", res)
}

// endInput waits for every commit mark of the input. stop is nil on purpose: a shutdown
// signal still lets the committed count settle, bounded by timeout (0: no limit). The count
// is per group, so with -parallel the wait also covers articles other inputs handed to the
// same group before this call: a longer wait, never a shorter one.
func (p *processorInput) endInput(timeout time.Duration) error {
	var errs []error
	for _, group := range p.order {
		if err := p.sink.db.Batch.WaitCommitted(p.marks[group], nil, timeout); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// newProcessor builds the processor for a real run; tests replace it. A nil pool is fine:
// the bulk path of ProcessArticle never uses the pool.
var newProcessor = func(db *database.Database, hashLen int) (*processor.Processor, error) {
	return processor.NewProcessorE(db, nil, hashLen)
}
