// mbox-importer imports Usenet mbox archives (archive.org collections) directly into the
// go-pugleaf databases through the processor's bulk path: every article is filed into one
// group (the first of its Newsgroups header, or -group), crossposts are not processed.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/processor"
)

var appVersion = "-unset-"

func main() {
	os.Exit(run(os.Args[1:]))
}

// run parses args and imports the inputs; it returns the process exit code
// (0 ok, 1 error, 2 bad command line, 130 stopped by a signal).
func run(args []string) int {
	return runWith(args, nil)
}

// runWith is run with an optional stop channel instead of the signal handler (tests):
// stopChan == nil installs the SIGINT/SIGTERM handler (notifyShutdown).
// Nothing after OpenDatabase returns without the shutdown (processor, database).
func runWith(args []string, stopChan <-chan struct{}) (code int) {
	lastRun = nil
	config.AppVersion = appVersion
	database.NO_CACHE_BOOT = true // prevents booting caches
	database.DBidleTimeOut = 15 * time.Second

	fs := flag.NewFlagSet("mbox-importer", flag.ContinueOnError)
	var (
		showHelp        = fs.Bool("help", false, "Show the usage and exit")
		dataDir         = fs.String("data", "./data", "go-pugleaf data directory (main DB, group DBs, history)")
		nntpHostname    = fs.String("nntphostname", "", "your NNTP hostname: required on the first run, later runs use the value stored in the database (must resolve in DNS)")
		useShortHashLen = fs.Int("useshorthashlen", 7, "legacy short hash length (2-7); fixed after the first run, later runs use the stored value")
		useHistory      = fs.Bool("history", true, "maintain and use the message-id history index (<data>/history)")
		forceGroup      = fs.String("group", "", "file every article into this group instead of the first group of its Newsgroups header")
		groupFromName   = fs.Bool("group-from-name", false, "file every article of an input into the group named by its file name (de.comm.foo.mbox.gz -> de.comm.foo); not with -group")
		sevenZip        = fs.String("7z", "7z", "program used to unpack .7z inputs (name in PATH or a full path)")
		zstdProg        = fs.String("zstd", "zstd", "program used to read UAT archives (.usenet, .usenet.xz, .usenet.7z; name in PATH or a full path)")
		xzProg          = fs.String("xz", "xz", "program used to unpack .usenet.xz inputs; when it is not found, -7z is used (7z e -txz)")
		tmpDir          = fs.String("tmpdir", "", "directory for the unpacked copy of a .usenet.xz / .usenet.7z input (as large as the package) and the zstd dictionary (\"\" = system temp dir)")
		dryRun          = fs.Bool("dry-run", false, "parse and count only: no database is opened, no input is renamed")
		keepBinary      = fs.Bool("keep-binary", false, "import yEnc binary articles (=ybegin) instead of skipping them")
		markDoneFlag    = fs.Bool("mark-done", true, "rename a fully imported input to INPUT.done")
		strictMsgID     = fs.Bool("strict-msgid", false, "require Message-IDs that pass go-pugleaf's validator instead of the lax mbox2nntp rules")
		commitTimeout   = fs.Duration("commit-timeout", 30*time.Minute, "per input: how long to wait until the batch writer committed its articles (0 = no limit)")
		parallel        = fs.Int("parallel", 1, "inputs imported at the same time (1-32; each worker takes the next input in order)")
		maxBatch        = fs.Int("max-batch", 1000, "articles the batch writer commits per group database transaction (100-1000)")
		maxQueue        = fs.Int("max-queue", 0, "articles the batch writer may hold queued over all groups (16-100000; 0 = 2 x parallel x max-batch, at least 1280)")
	)
	fs.Usage = func() { usage(fs) }
	if err := fs.Parse(args); err != nil {
		// Parse already printed the error and the usage; -h (not defined) is flag.ErrHelp
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showHelp {
		fs.Usage()
		return 0
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(fs.Output(), "mbox-importer: no input given")
		fs.Usage()
		return 2
	}
	if *forceGroup != "" && !common.IsValidGroupName(*forceGroup) {
		fmt.Fprintf(fs.Output(), "mbox-importer: -group %q is not a valid newsgroup name\n", *forceGroup)
		return 2
	}
	if *forceGroup != "" && *groupFromName {
		fmt.Fprintln(fs.Output(), "mbox-importer: -group and -group-from-name exclude each other")
		return 2
	}
	if *useShortHashLen < 2 || *useShortHashLen > 7 {
		fmt.Fprintf(fs.Output(), "mbox-importer: -useshorthashlen %d is not in 2-7\n", *useShortHashLen)
		return 2
	}
	if *commitTimeout < 0 {
		fmt.Fprintf(fs.Output(), "mbox-importer: -commit-timeout %v is negative\n", *commitTimeout)
		return 2
	}
	if *parallel < 1 || *parallel > maxParallel {
		fmt.Fprintf(fs.Output(), "mbox-importer: -parallel %d is not in 1-%d\n", *parallel, maxParallel)
		return 2
	}
	if *maxBatch < 100 || *maxBatch > 1000 {
		fmt.Fprintf(fs.Output(), "mbox-importer: -max-batch %d is not in 100-1000\n", *maxBatch)
		return 2
	}
	if *maxQueue != 0 && (*maxQueue < minBatchQueue || *maxQueue > maxBatchQueue) {
		fmt.Fprintf(fs.Output(), "mbox-importer: -max-queue %d is not 0 or in %d-%d\n", *maxQueue, minBatchQueue, maxBatchQueue)
		return 2
	}
	queueSize := batchQueueSize(*parallel, *maxBatch, *maxQueue)

	opts := &runOptions{
		dataDir:         *dataDir,
		hostname:        *nntpHostname,
		group:           *forceGroup,
		sevenZip:        *sevenZip,
		zstd:            *zstdProg,
		xz:              *xzProg,
		tmpDir:          *tmpDir,
		useShortHashLen: *useShortHashLen,
		parallel:        *parallel,
		groupFromName:   *groupFromName,
		history:         *useHistory,
		dryRun:          *dryRun,
		keepBinary:      *keepBinary,
		markDone:        *markDoneFlag,
		strictMsgID:     *strictMsgID,
		commitTimeout:   *commitTimeout,
	}

	history.ENABLE_HISTORY = opts.history // read once by NewHistory
	database.GlobalDateParser = processor.ParseNNTPDate
	log.Printf("[MBOX-IMPORT] Starting go-pugleaf mbox-importer (version %s) parallel=%d max-batch=%d max-queue=%d", config.AppVersion, *parallel, *maxBatch, queueSize)

	stop := stopChan
	if stop == nil {
		sigStop, stopSignals := notifyShutdown()
		defer stopSignals()
		stop = sigStop
	}

	files, expandErrs := expandInputs(fs.Args())
	for _, err := range expandErrs {
		log.Printf("[MBOX-IMPORT] input error: %v", err)
	}

	if opts.dryRun {
		sink := newCountingSink()
		res := importInputs(files, opts, sink, stop)
		res.InputErrors += len(expandErrs)
		res.Inputs += len(expandErrs)
		logDryRunGroups(sink.groups)
		logTotals(res)
		lastRun = res
		return res.exitCode()
	}

	if len(files) == 0 {
		// nothing to import (empty directory, every input already .done, or only missing paths)
		log.Printf("[MBOX-IMPORT] no inputs to import")
		res := &runSummary{Inputs: len(expandErrs), InputErrors: len(expandErrs)}
		logTotals(res)
		lastRun = res
		return res.exitCode()
	}

	cfg := database.DefaultDBConfig()
	cfg.DataDir = opts.dataDir
	cfg.BackupDir = filepath.Join(opts.dataDir, "backups")
	cfg.MaxDBbatch = *maxBatch // articles per group database transaction (db_batch.go)
	cfg.MaxQueued = queueSize  // queued over all groups before the batch divider brakes (db_batch.go)
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		log.Printf("[MBOX-IMPORT] Failed to initialize database: %v", err)
		return 1
	}
	var proc *processor.Processor
	defer func() {
		if !shutdown(proc, db) && code == 0 {
			code = 1
		}
	}()

	if opts.hostname != "" {
		if err := processor.SetHostname(opts.hostname, db); err != nil {
			log.Printf("[MBOX-IMPORT] failed to set NNTP hostname: %v", err)
			return 1
		}
	}
	hashLen, err := lockShortHashLen(db, opts.useShortHashLen)
	if err != nil {
		log.Printf("[MBOX-IMPORT] %v", err)
		return 1
	}
	proc, err = newProcessor(db, hashLen)
	if err != nil {
		log.Printf("[MBOX-IMPORT] failed to create the processor: %v", err)
		proc = nil
		return 1
	}

	res := importInputs(files, opts, newProcessorSink(db, proc), stop)
	res.InputErrors += len(expandErrs)
	res.Inputs += len(expandErrs)
	logTotals(res)
	lastRun = res
	return res.exitCode()
}

// lockShortHashLen returns the stored short hash length, storing want on the first run.
func lockShortHashLen(db *database.Database, want int) (int, error) {
	locked, isLocked, err := db.GetHistoryUseShortHashLen(want)
	if err != nil {
		return 0, fmt.Errorf("failed to get locked history hash length: %w", err)
	}
	if !isLocked {
		log.Printf("[MBOX-IMPORT] Locking UseShortHashLen to %d (first run)", want)
		if err := db.SetHistoryUseShortHashLen(want); err != nil {
			return 0, fmt.Errorf("failed to lock history hash length: %w", err)
		}
		return want, nil
	}
	if want != locked {
		log.Printf("[MBOX-IMPORT] WARNING: -useshorthashlen %d differs from the locked value %d: using the locked value", want, locked)
	}
	return locked, nil
}

// notifyShutdown returns a channel that is closed on SIGINT or SIGTERM, and stop, which ends the
// watcher goroutine and the signal notification (call it once, when the run returns).
func notifyShutdown() (shutdownChan <-chan struct{}, stop func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	closeChan := make(chan struct{})
	runDone := make(chan struct{})

	go func() {
		select {
		case sig := <-sigChan:
			log.Printf("[MBOX-IMPORT] Received %v: finishing the current article, then shutting down...", sig)
			close(closeChan)
			// a second signal gets the default action (terminates the process)
			signal.Stop(sigChan)
		case <-runDone:
		}
	}()
	return closeChan, func() {
		close(runDone)
		signal.Stop(sigChan)
	}
}

// shutdown closes the processor (flushes the history) first, then stops the database
// background workers and the database. It reports false on any error.
func shutdown(proc *processor.Processor, db *database.Database) bool {
	ok := true
	if proc != nil {
		if err := proc.Close(); err != nil {
			log.Printf("[MBOX-IMPORT] Warning: failed to close the processor: %v", err)
			ok = false
		}
	}
	close(db.StopChan)
	db.WG.Wait()
	if err := db.Shutdown(); err != nil {
		log.Printf("[MBOX-IMPORT] Failed to shutdown database: %v", err)
		return false
	}
	log.Printf("[MBOX-IMPORT] Database shutdown successfully")
	return ok
}

// Bounds of -max-queue: the hardcoded limits of the batch writer (database.NewSQ3batch).
const (
	minBatchQueue = 16
	maxBatchQueue = 100000
)

// batchQueueSize is the MaxQueued value for the batch writer. maxQueue != 0 is taken as given.
// Otherwise it is twice parallel*maxBatch, at least the database default: the batch divider
// stops accepting articles at 80 % of MaxQueued and resumes at 20 %, and a group's batch is
// committed early only once that group has maxBatch articles queued. With the default (1280)
// and several inputs none of the groups ever fills a batch, so every article waits for the
// 3-second fallback timer. Twice the sum leaves room for the batches that are being
// committed: their articles stay counted until the commit is done.
func batchQueueSize(parallel, maxBatch, maxQueue int) int {
	if maxQueue != 0 {
		return maxQueue
	}
	n := 2 * parallel * maxBatch
	if floor := database.DefaultDBConfig().MaxQueued; n < floor {
		n = floor
	}
	if n > maxBatchQueue {
		n = maxBatchQueue
	}
	return n
}

// usage prints the command line help.
func usage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintln(out, "Usage: mbox-importer [flags] INPUT...")
	fmt.Fprintln(out, "  INPUT: NAME.mbox, NAME.bag, NAME.mbox.gz, NAME.mbox.zip, NAME.mbox.7z (via -7z),")
	fmt.Fprintln(out, "         UAT archives NAME.usenet, NAME.usenet.xz (via -xz), NAME.usenet.7z (via -7z), all via -zstd,")
	fmt.Fprintln(out, "         or a directory (its *.mbox* / *.bag* / *.usenet* files not ending in .done, sorted by name)")
	fmt.Fprintln(out, "Examples:")
	fmt.Fprintln(out, "  mbox-importer -dry-run de.comm.internet.commerce.mbox")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -nntphostname news.example.org de.comm.internet.commerce.mbox")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -group local.archive /archive/usenet/de.comm/")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -tmpdir /big/tmp pl.comp.lang.c.usenet.xz")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -parallel 4 -max-batch 500 /archive/usenet/")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -group-from-name -parallel 4 /archive/uat-mbox/   # de.comm.foo.mbox.gz -> de.comm.foo")
	fmt.Fprintln(out, "Exit codes: 0 ok, 1 input, article, commit or database error, 2 bad command line, 130 stopped by a signal")
	fmt.Fprintln(out, "Flags:")
	fs.PrintDefaults()
}
