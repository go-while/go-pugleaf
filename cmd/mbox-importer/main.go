// mbox-importer imports Usenet mbox archives (archive.org collections) directly into the
// go-pugleaf databases through the processor's bulk path: every article is filed into one
// group (the first of its Newsgroups header, or -group), crossposts are not processed.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
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
	"github.com/go-while/mbox2nntp/uat"
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
		tmpDir          = fs.String("tmpdir", "", "directory for the unpacked copy of a .usenet.xz / .usenet.7z input or of a disc image (as large as the unpacked contents) and the zstd dictionary (\"\" = system temp dir)")
		spoolFlag       = fs.Bool("spool", false, "read a positional directory or .zip input as a news spool (one article per file, as on a CD image) instead of scanning the directory for *.mbox* files; disc images (.iso, .cdr, .sqsh, .img) are always read this way")
		dryRun          = fs.Bool("dry-run", false, "parse and count only: no database is opened, no input is renamed")
		keepBinary      = fs.Bool("keep-binary", false, "import yEnc binary articles (=ybegin) instead of skipping them")
		markDoneFlag    = fs.Bool("mark-done", true, "rename a fully imported input to INPUT.done")
		strictMsgID     = fs.Bool("strict-msgid", false, "require Message-IDs that pass go-pugleaf's validator instead of the lax mbox2nntp rules")
		commitTimeout   = fs.Duration("commit-timeout", 30*time.Minute, "per input: how long to wait until the batch writer committed its articles (0 = no limit)")
		parallel        = fs.Int("parallel", 1, "inputs imported at the same time (1-32; each worker takes the next input in order)")
		maxBatch        = fs.Int("max-batch", 1000, "articles the batch writer commits per group database transaction (100-1000)")
		maxArtSize      = fs.Int("max-article-size", defaultMaxArticleSize, "skip articles larger than this many bytes, header and body, counted as too-big (0 = no limit)")
		maxQueue        = fs.Int("max-queue", 0, "articles the batch writer may hold queued over all groups (16-100000; 0 = 2 x parallel x max-batch, at least 1280)")
		trackDBFlag     = fs.String("trackdb", "", "import tracking database (\"\" = <data>/cfg/"+trackDBName+", \"off\" = no tracking); never opened in a dry run")
		reimport        = fs.Bool("reimport", false, "import an input the tracking database knows as done again")
		logDB           = fs.Bool("log-db", true, "write the log output of the run into the tracking database as well")
		logKeepRuns     = fs.Int("log-keep-runs", 0, "at start, delete the log rows of all but the newest N runs, this run included (0 = keep all)")
		statusFlag      = fs.Bool("status", false, "print what the tracking database knows and exit, importing nothing")
		jsonFlag        = fs.Bool("json", false, "print -status as JSON")
		fromIAFetch     = fs.String("from-iafetch", "", "take the inputs from the download database of mbox2nntp's iafetch at this path (read-only) instead of positional inputs")
		fromIAFetchBase = fs.String("from-iafetch-base", "", "iafetch's working directory: a relative path in its database is resolved against it (needs -from-iafetch)")
		verifyMD5       = fs.Bool("verify-md5", false, "check the md5 iafetch recorded before an input is imported (needs -from-iafetch)")
		follow          = fs.Duration("follow", 0, "after the inputs of a scan are done, wait this long and scan again, until a signal stops the run (0 = one scan; needs -from-iafetch)")
		pprofAddr       = fs.String("pprof", "", "debug: serve net/http/pprof on this address (e.g. 127.0.0.1:6061), with block and mutex profiling on")
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
	if *pprofAddr != "" {
		stopPprof := startPprof(*pprofAddr)
		defer stopPprof()
	}
	trackPath := trackDBPath(*trackDBFlag, *dataDir)
	if *statusFlag {
		if trackPath == "" {
			fmt.Fprintln(fs.Output(), "mbox-importer: -status needs a tracking database (-trackdb off)")
			return 2
		}
		if err := writeStatus(os.Stdout, trackPath, *jsonFlag); err != nil {
			log.Printf("[MBOX-IMPORT] -status %q: %v", trackPath, err)
			return 1
		}
		return 0
	}
	if *logKeepRuns < 0 {
		fmt.Fprintf(fs.Output(), "mbox-importer: -log-keep-runs %d is negative\n", *logKeepRuns)
		return 2
	}
	if msg := checkIAFetchFlags(*fromIAFetch, *fromIAFetchBase, *verifyMD5, *follow, fs.NArg(), trackPath, *dryRun); msg != "" {
		fmt.Fprintln(fs.Output(), "mbox-importer: "+msg)
		return 2
	}
	if *spoolFlag && *fromIAFetch != "" {
		// -spool is about positional directory and zip arguments, and -from-iafetch has
		// none; a disc image of the mirror is read as a news spool without the flag.
		fmt.Fprintln(fs.Output(), "mbox-importer: -spool and -from-iafetch exclude each other: "+
			"-spool is for positional directory or .zip inputs (a disc image is read as a news spool anyway)")
		return 2
	}
	if *fromIAFetch == "" && fs.NArg() == 0 {
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
	if *maxArtSize < 0 {
		fmt.Fprintf(fs.Output(), "mbox-importer: -max-article-size %d is negative\n", *maxArtSize)
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
		spool:           *spoolFlag,
		useShortHashLen: *useShortHashLen,
		parallel:        *parallel,
		groupFromName:   *groupFromName,
		history:         *useHistory,
		dryRun:          *dryRun,
		keepBinary:      *keepBinary,
		markDone:        *markDoneFlag,
		strictMsgID:     *strictMsgID,
		verifyMD5:       *verifyMD5,
		follow:          *follow,
		commitTimeout:   *commitTimeout,
		maxArticleSize:  *maxArtSize,
	}

	// the UAT reader reads past bigger messages and counts them (Package.TooBig)
	uat.MaxMessageSize = math.MaxInt
	if opts.maxArticleSize > 0 {
		uat.MaxMessageSize = opts.maxArticleSize
	}
	history.ENABLE_HISTORY = opts.history // read once by NewHistory
	database.GlobalDateParser = processor.ParseNNTPDate

	stop := stopChan
	if stop == nil {
		sigStop, stopSignals := notifyShutdown()
		defer stopSignals()
		stop = sigStop
	}

	// The inputs of this run: the positional arguments, or the files iafetch recorded in its
	// download database (-from-iafetch). The iafetch scan stays a function, because -follow
	// runs it again after every wait; an error in the first scan (a relative path without
	// -from-iafetch-base, a database that cannot be read) ends the run before it starts.
	var (
		planned    []plannedInput
		expandErrs []error
		scan       scanFunc
	)
	if *fromIAFetch != "" {
		scan = func() ([]plannedInput, error) { return iafetchInputs(*fromIAFetch, *fromIAFetchBase) }
		first, err := scan()
		if err != nil {
			fmt.Fprintf(fs.Output(), "mbox-importer: %v\n", err)
			return 2
		}
		planned = first
		opts.markDone = false // iafetch's mirror is only read, never renamed
	} else {
		files, errs := expandInputs(fs.Args(), opts.spool)
		expandErrs = errs
		planned = plannedFromPaths(files)
		if opts.spool {
			spoolDirSizes(planned) // a spool directory is tracked with size 0 (disc.go)
		}
	}

	// The tracking database is opened before the run is logged: its log tee catches every
	// line of the run, and its close (deferred before the database shutdown, so it runs
	// after it) catches the shutdown lines too. A run with nothing to import does not
	// create it (and a dry run never touches it); a -follow run does, because a later scan
	// will have inputs.
	var track *tracker
	if trackPath != "" && !opts.dryRun && (len(planned) > 0 || opts.follow > 0) {
		t, err := openTracker(trackPath, args, *reimport, *logDB, *logKeepRuns)
		if err != nil {
			log.Printf("[MBOX-IMPORT] tracking database %q: %v", trackPath, err)
			return 1
		}
		track = t
		opts.tracked = true
		defer func() { track.close(lastRun, code) }()
	}
	log.Printf("[MBOX-IMPORT] Starting go-pugleaf mbox-importer (version %s) parallel=%d max-batch=%d max-queue=%d", config.AppVersion, *parallel, *maxBatch, queueSize)
	for _, err := range expandErrs {
		log.Printf("[MBOX-IMPORT] input error: %v", err)
	}
	if *fromIAFetch != "" {
		logIAFetchSource(*fromIAFetch, *fromIAFetchBase, *markDoneFlag)
	}

	if opts.dryRun {
		if opts.follow > 0 {
			log.Printf("[MBOX-IMPORT] -follow %v is ignored in a dry run: one scan", opts.follow)
			opts.follow = 0
		}
		sink := newCountingSink()
		res := importScans(planned, scan, opts, sink, stop, nil)
		res.InputErrors += len(expandErrs)
		res.Inputs += len(expandErrs)
		logDryRunGroups(sink.groups)
		logTotals(res)
		lastRun = res
		return res.exitCode()
	}

	if len(planned) == 0 && opts.follow <= 0 {
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

	res := importScans(planned, scan, opts, newProcessorSink(db, proc), stop, track)
	res.InputErrors += len(expandErrs)
	res.Inputs += len(expandErrs)
	logTotals(res)
	lastRun = res
	return res.exitCode()
}

// checkIAFetchFlags checks the flag combinations of -from-iafetch (plan mbox-import-tracking)
// and returns the message of exit code 2, or "" when the command line is fine.
//
// -from-iafetch-base, -verify-md5 and -follow only mean something for the inputs of
// iafetch's database: the base resolves its relative paths, the md5 to check against comes
// from it, and a list of positional inputs is not scanned again. -from-iafetch needs the
// tracking database, because without it a scan has no memory and would import every
// recorded file again — except in a dry run, which imports nothing and opens no database
// at all, so -dry-run -trackdb off is fine there.
func checkIAFetchFlags(dbPath, base string, verifyMD5 bool, follow time.Duration, args int, trackPath string, dryRun bool) string {
	if dbPath == "" {
		switch {
		case base != "":
			return "-from-iafetch-base needs -from-iafetch"
		case verifyMD5:
			return "-verify-md5 needs -from-iafetch: only iafetch's database records an md5 to check against"
		case follow != 0:
			return "-follow needs -from-iafetch: a list of positional inputs is not scanned again"
		}
		return ""
	}
	switch {
	case args > 0:
		return "-from-iafetch and positional inputs exclude each other"
	case trackPath == "" && !dryRun:
		return "-from-iafetch needs the tracking database: with -trackdb off every scan would import every recorded file again"
	case follow < 0:
		return fmt.Sprintf("-follow %v is negative", follow)
	}
	return ""
}

// logIAFetchSource logs the one line a -from-iafetch run says about its source: the mirror
// is only read, so no input is renamed, whatever -mark-done says (runOptions.markDone is
// already false by then). markDoneFlag is the value the command line asked for.
func logIAFetchSource(dbPath, base string, markDoneFlag bool) {
	where := "paths as recorded: absolute, a relative one needs -from-iafetch-base"
	if base != "" {
		where = fmt.Sprintf("relative paths resolved under %q", base)
	}
	if markDoneFlag {
		log.Printf("[MBOX-IMPORT] -from-iafetch %q (%s): read-only, -mark-done is ignored (the tracking database holds the import state)", dbPath, where)
		return
	}
	log.Printf("[MBOX-IMPORT] -from-iafetch %q (%s): read-only, nothing is renamed (-mark-done was already off)", dbPath, where)
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
	fmt.Fprintln(out, "       mbox-importer [flags] -from-iafetch DB [-from-iafetch-base DIR]   (inputs from iafetch's download database)")
	fmt.Fprintln(out, "       mbox-importer [flags] -status [-json]   (prints the tracking database, imports nothing)")
	fmt.Fprintln(out, "  INPUT: NAME.mbox, NAME.bag, NAME.mbox.gz, NAME.mbox.zip, NAME.mbox.7z (via -7z),")
	fmt.Fprintln(out, "         UAT archives NAME.usenet, NAME.usenet.xz (via -xz), NAME.usenet.7z (via -7z), all via -zstd,")
	fmt.Fprintln(out, "         disc images IMAGE.iso, IMAGE.cdr, IMAGE.sqsh, IMAGE.img (news spools, unpacked into -tmpdir via -7z),")
	fmt.Fprintln(out, "         a directory (its *.mbox* / *.bag* / *.usenet* files not ending in .done, sorted by name),")
	fmt.Fprintln(out, "         or, with -spool, a directory or SPOOL.zip read as a news spool (one article per file)")
	fmt.Fprintln(out, "Examples:")
	fmt.Fprintln(out, "  mbox-importer -dry-run de.comm.internet.commerce.mbox")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -nntphostname news.example.org de.comm.internet.commerce.mbox")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -group local.archive /archive/usenet/de.comm/")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -tmpdir /big/tmp pl.comp.lang.c.usenet.xz")
	fmt.Fprintln(out, "  mbox-importer -dry-run -tmpdir /big/tmp NN_1992_16.iso                            # analyse a CD image first")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -tmpdir /big/tmp utzoo.sqsh                  # -tmpdir needs the unpacked size")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -spool /mnt/netnews-cd NN_1992_25.zip        # a mounted image and a zipped spool")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -parallel 4 -max-batch 500 /archive/usenet/")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -group-from-name -parallel 4 /archive/uat-mbox/   # de.comm.foo.mbox.gz -> de.comm.foo")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -status                                         # what the tracking database knows")
	fmt.Fprintln(out, "  mbox-importer -data ./data-test-mbox -from-iafetch /archive.org/.iafetch/iafetch.sqlite -group-from-name -parallel 4 -follow 10m")
	fmt.Fprintln(out, "Exit codes: 0 ok, 1 input, article, commit or database error, 2 bad command line, 130 stopped by a signal")
	fmt.Fprintln(out, "Flags:")
	fs.PrintDefaults()
}
