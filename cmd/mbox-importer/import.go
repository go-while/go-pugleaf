package main

import (
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// counters are the per-input (and total) results.
type counters struct {
	Read, Stored, Present, Rejected, Errors, ParserSkipped, NearMisses int
	Skipped                                                            map[skipReason]int
	StoppedEarly, Unread, MarkedDone                                   bool
}

// articleSink receives every importable article of an input: the dry run counts, the
// real run looks the id up, creates the group, takes the commit mark and calls ProcessArticle.
type articleSink interface {
	Handle(msgid string, art func() (*models.Article, error), group string) (stored, present bool, err error)
}

// runOptions are the parsed flags of one run.
type runOptions struct {
	dataDir, hostname, group, sevenZip string
	useShortHashLen                    int
	history, dryRun, keepBinary        bool
	markDone, strictMsgID              bool
	commitTimeout                      int64 // nanoseconds, see wave 2
}

// readMbox reads one input and feeds the sink.
func readMbox(src *source, opts *runOptions, sink articleSink, stop <-chan struct{}) (*counters, error) {
	_ = src
	_ = opts
	_ = sink
	_ = stop
	return nil, errNotImplemented
}

// newProcessor builds the processor for a real run; tests replace it.
var newProcessor = func(db *database.Database, hashLen int) (*processor.Processor, error) {
	return processor.NewProcessorE(db, nil, hashLen)
}
