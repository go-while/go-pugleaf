package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
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
	Read, Stored, Present, Rejected, Errors, ParserSkipped, NearMisses int
	Skipped                                                            map[skipReason]int
	StoppedEarly, Unread, MarkedDone                                   bool
	CommitFailed                                                       bool // the commit wait of the input failed
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
	for r := skipNoMessageID; r <= skipBadGroup; r++ {
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
	for r, n := range o.Skipped {
		c.Skipped[r] += n
	}
}

// runSummary is the result of one run (all inputs).
type runSummary struct {
	Inputs, InputErrors, MarkedDone int
	CommitErrors                    int // inputs whose commit wait failed
	Stopped                         bool
	Total                           *counters
	PerInput                        []*counters
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

// articleSink receives every importable article of an input: the dry run counts, the
// real run looks the id up, creates the group, takes the commit mark and calls ProcessArticle.
// A Handle error wrapping errRejected counts as rejected, any other error as an error.
type articleSink interface {
	Handle(msgid string, art func() (*models.Article, error), group string) (stored, present bool, err error)
	// beginInput is called before the first article of an input.
	beginInput()
	// endInput waits until the articles handed over for the input are committed.
	endInput(timeout time.Duration) error
}

// runOptions are the parsed flags of one run.
type runOptions struct {
	dataDir, hostname, group, sevenZip string
	useShortHashLen                    int
	history, dryRun, keepBinary        bool
	markDone, strictMsgID              bool
	commitTimeout                      time.Duration // 0: no limit
}

// readMbox reads one input and feeds the sink. It returns errStopped (with the counters
// so far) when stop was closed between two articles; the current article is finished first.
func readMbox(src *source, opts *runOptions, sink articleSink, stop <-chan struct{}) (*counters, error) {
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
// The body is always drained.
func handleMessage(c *counters, rawHead []string, bodyReader io.Reader, opts *runOptions, sink articleSink) {
	body, isBinary := readBody(bodyReader, opts.keepBinary)
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

// importInputs imports files one after the other until stop is closed.
func importInputs(files []string, opts *runOptions, sink articleSink, stop <-chan struct{}) *runSummary {
	res := &runSummary{Total: newCounters("")}
	for i, path := range files {
		select {
		case <-stop:
			res.Stopped = true
			log.Printf("[MBOX-IMPORT] stopped by shutdown signal: %d inputs not started", len(files)-i)
			return res
		default:
		}
		res.Inputs++
		c, inputErr, stopped := importInput(path, opts, sink, stop)
		if inputErr {
			res.InputErrors++
		}
		if c != nil {
			res.PerInput = append(res.PerInput, c)
			res.Total.add(c)
			if c.MarkedDone {
				res.MarkedDone++
			}
			if c.CommitFailed {
				res.CommitErrors++
			}
		}
		if stopped {
			res.Stopped = true
			if left := len(files) - i - 1; left > 0 {
				log.Printf("[MBOX-IMPORT] stopped by shutdown signal: %d inputs not started", left)
			}
			return res
		}
	}
	return res
}

// importInput opens, reads and closes one input, waits for its commits and marks it done.
// c is nil when the input could not be opened; inputErr reports an open, read or close error
// (or a failed rename); stopped reports that the stop channel ended the read.
func importInput(path string, opts *runOptions, sink articleSink, stop <-chan struct{}) (c *counters, inputErr, stopped bool) {
	src, err := openSource(path, opts.sevenZip)
	if err != nil {
		log.Printf("[MBOX-IMPORT] input error: %v", err)
		return nil, true, false
	}
	log.Printf("[MBOX-IMPORT] importing file=%q", path)
	sink.beginInput()
	c, readErr := readMbox(src, opts, sink, stop)
	stopped = errors.Is(readErr, errStopped)
	c.Unread = src.Unread() // before Close: Close kills a 7z that was not read to its end
	closeErr := src.Close()
	if closeErr != nil {
		log.Printf("[MBOX-IMPORT] input error: file=%q: %v", path, closeErr)
		inputErr = true
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
	waitErr := sink.endInput(opts.commitTimeout)
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
	for _, nm := range samples {
		log.Printf("[MBOX-IMPORT]   near-miss file=%q line=%d %q", path, nm.line, nm.text)
	}
	return c, inputErr, stopped
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
// rejection) and counts it for its target group as stored.
type countingSink struct {
	groups map[string]int
}

func newCountingSink() *countingSink {
	return &countingSink{groups: make(map[string]int)}
}

func (s *countingSink) Handle(msgid string, art func() (*models.Article, error), group string) (bool, bool, error) {
	if _, err := art(); err != nil {
		return false, false, fmt.Errorf("%w: %v", errRejected, err)
	}
	s.groups[group]++
	return true, false, nil
}

func (s *countingSink) beginInput() {}

func (s *countingSink) endInput(time.Duration) error { return nil }

// processorSink is the real run's sink: it creates each group once per run, takes a
// commit mark per (input, group), skips articles already in the group and hands the
// rest to ProcessArticle in bulk mode.
type processorSink struct {
	db      *database.Database
	proc    *processor.Processor
	ensured map[string]bool                // groups created or found this run
	marks   map[string]database.CommitMark // commit marks of the current input
	order   []string                       // groups of the current input, first sight order
}

func newProcessorSink(db *database.Database, proc *processor.Processor) *processorSink {
	return &processorSink{db: db, proc: proc, ensured: make(map[string]bool)}
}

func (s *processorSink) beginInput() {
	s.marks = make(map[string]database.CommitMark)
	s.order = nil
}

func (s *processorSink) Handle(msgid string, art func() (*models.Article, error), group string) (bool, bool, error) {
	if !s.ensured[group] {
		if err := processor.EnsureNewsgroup(s.db, group, ""); err != nil {
			return false, false, fmt.Errorf("create newsgroup: %w", err)
		}
		s.ensured[group] = true
	}
	if _, ok := s.marks[group]; !ok {
		s.marks[group] = s.db.Batch.CommitMark(group)
		s.order = append(s.order, group)
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
// signal still lets the committed count settle, bounded by timeout (0: no limit).
func (s *processorSink) endInput(timeout time.Duration) error {
	var errs []error
	for _, group := range s.order {
		if err := s.db.Batch.WaitCommitted(s.marks[group], nil, timeout); err != nil {
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
