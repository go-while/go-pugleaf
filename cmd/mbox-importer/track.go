package main

// Import tracking DB (plan mbox-import-tracking): one SQLite file that remembers every
// input the importer saw, the result of every run and the run's log lines. It replaces the
// INPUT.done rename as the memory of what was imported (the rename stays, but a rename the
// file system refuses is no longer an input error while the DB is on).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3" // the tracking DB uses the driver go-pugleaf already uses
)

const (
	// trackDBOff is the -trackdb value that turns the tracking DB off.
	trackDBOff = "off"
	// trackDBName is the file name of the tracking DB under <data>/cfg.
	trackDBName = "mbox-import.sq3"
	// trackSchemaVersion is the PRAGMA user_version of the schema below.
	trackSchemaVersion = 1
	// trackLogQueue is the capacity of the log tee's channel: a full channel drops the line.
	trackLogQueue = 10000
	// trackLogBatch is the most log lines inserted in one transaction.
	trackLogBatch = 1000
	// trackLogFlush is how long a batch waits for more lines before it is inserted.
	trackLogFlush = time.Second
	// trackLogMaxFails is how many failed log inserts in a row stop the log writer
	// (the import goes on, the lines are counted as dropped).
	trackLogMaxFails = 3
)

// Statuses of an inputs row.
const (
	trackPending     = "pending"     // known, not imported
	trackImporting   = "importing"   // a run holds it
	trackDone        = "done"        // imported
	trackFailed      = "failed"      // input error, article errors or a failed commit wait
	trackStopped     = "stopped"     // a signal ended the import of the input
	trackUnsupported = "unsupported" // the importer cannot read this file (reason says why)
)

// trackSchema is the schema of version trackSchemaVersion. CREATE IF NOT EXISTS only
// creates what is missing, so a later version 2 needs its own ALTER TABLE / migration code
// here (keyed on the user_version read by migrate), not a changed CREATE statement: an
// existing table is never altered by this list.
var trackSchema = []string{
	`CREATE TABLE IF NOT EXISTS runs (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		started   TEXT NOT NULL,
		finished  TEXT,
		version   TEXT,
		host      TEXT,
		pid       INTEGER,
		boot_id   TEXT,
		pstart    INTEGER,
		args      TEXT,
		exit_code INTEGER,
		inputs    INTEGER NOT NULL DEFAULT 0,
		read      INTEGER NOT NULL DEFAULT 0,
		stored    INTEGER NOT NULL DEFAULT 0,
		present   INTEGER NOT NULL DEFAULT 0,
		skipped   INTEGER NOT NULL DEFAULT 0,
		rejected  INTEGER NOT NULL DEFAULT 0,
		errors    INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS inputs (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		path           TEXT UNIQUE NOT NULL,
		source         TEXT NOT NULL,
		identifier     TEXT,
		name           TEXT,
		size           INTEGER,
		mtime          INTEGER,
		md5            TEXT,
		status         TEXT NOT NULL,
		reason         TEXT,
		group_name     TEXT,
		read           INTEGER NOT NULL DEFAULT 0,
		stored         INTEGER NOT NULL DEFAULT 0,
		present        INTEGER NOT NULL DEFAULT 0,
		skipped        INTEGER NOT NULL DEFAULT 0,
		skipped_detail TEXT,
		rejected       INTEGER NOT NULL DEFAULT 0,
		errors         INTEGER NOT NULL DEFAULT 0,
		parser_skipped INTEGER NOT NULL DEFAULT 0,
		near_misses    INTEGER NOT NULL DEFAULT 0,
		first_run      INTEGER,
		last_run       INTEGER,
		started        TEXT,
		finished       TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_inputs_status ON inputs(status)`,
	`CREATE INDEX IF NOT EXISTS idx_inputs_identifier ON inputs(identifier)`,
	`CREATE TABLE IF NOT EXISTS log (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id   INTEGER NOT NULL,
		input_id INTEGER,
		ts       TEXT NOT NULL,
		msg      TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_log_run ON log(run_id)`,
	`CREATE INDEX IF NOT EXISTS idx_log_input ON log(input_id)`,
}

// trackDBPath returns the tracking DB of a run: "" when it is off, the -trackdb value when
// one was given, else <data>/cfg/mbox-import.sq3.
func trackDBPath(flagValue, dataDir string) string {
	switch flagValue {
	case trackDBOff:
		return ""
	case "":
		return filepath.Join(dataDir, "cfg", trackDBName)
	}
	return flagValue
}

// trackNow is the timestamp written into the DB (UTC, sortable as text).
func trackNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// tracker is the open tracking DB of one run. Every method tolerates a nil receiver, so the
// import path does not have to check whether tracking is on.
type tracker struct {
	db       *sql.DB
	path     string
	runID    int64
	reimport bool // -reimport: import a done input again

	// live and foreign are the unfinished runs seen at open (liveRuns): the ones whose
	// process is still running on this host, and the ones of another host. Both are
	// written once, before the first worker starts, and only read after that.
	live    map[int64]bool
	foreign map[int64]string

	mu  sync.Mutex       // ids
	ids map[string]int64 // input path -> inputs.id, for the log tee

	tee     *logTee   // nil with -log-db=false
	prevLog io.Writer // the log output before the tee was installed
	errs    atomic.Int64
}

// openTracker opens (and creates) the tracking DB at path, brings the schema up, marks the
// inputs an earlier run left behind as failed/interrupted, inserts the row of this run,
// prunes old log rows (keepRuns > 0) and installs the log tee (logDB).
func openTracker(path string, args []string, reimport, logDB bool, keepRuns int) (*tracker, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite3", trackDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		return nil, trackClose(db, err)
	}
	t := &tracker{db: db, path: path, reimport: reimport, ids: make(map[string]int64)}
	if err := t.migrate(); err != nil {
		return nil, trackClose(db, err)
	}
	if err := t.insertRun(args); err != nil {
		return nil, trackClose(db, err)
	}
	if logDB {
		t.startLogTee() // before the first line, so the whole run is in the log table
	}
	log.Printf("[MBOX-IMPORT] tracking db=%q run=%d", path, t.runID)
	// both log, so both run with the tee installed; close() takes the tee out again
	if err := t.sweepInterrupted(); err != nil {
		t.close(nil, 1)
		return nil, err
	}
	if err := t.pruneLog(keepRuns); err != nil {
		t.close(nil, 1)
		return nil, err
	}
	return t, nil
}

// trackDSN is the DSN the tracking database is opened with. The mattn driver hands every
// "file:" DSN to SQLite's URI parser, so the path has to be escaped (iafetchDSN does the
// same): an unescaped '?' or '#' in it would cut the path short and drop the parameters.
// _txlock=immediate makes every transaction of the pool a BEGIN IMMEDIATE, so the claim of
// an input takes the write lock before it reads the row.
func trackDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_busy_timeout=30000&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
}

// trackStatusDSN is the read-only DSN of -status: it never creates or writes the file.
func trackStatusDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=ro&_busy_timeout=10000"
}

// trackClose closes db on an error path and keeps the first error.
func trackClose(db *sql.DB, err error) error {
	if cerr := db.Close(); cerr != nil {
		return fmt.Errorf("%w (also: close: %v)", err, cerr)
	}
	return err
}

// migrate creates the schema of a new DB and refuses a DB written by a newer version.
func (t *tracker) migrate() error {
	var version int
	if err := database.RetryableQueryRowScan(t.db, "PRAGMA user_version", nil, &version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if version > trackSchemaVersion {
		return fmt.Errorf("schema version %d is newer than %d: use a newer mbox-importer", version, trackSchemaVersion)
	}
	for _, stmt := range trackSchema {
		if _, err := database.RetryableExec(t.db, stmt); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	}
	if version != trackSchemaVersion {
		if _, err := database.RetryableExec(t.db, fmt.Sprintf("PRAGMA user_version = %d", trackSchemaVersion)); err != nil {
			return fmt.Errorf("set user_version: %w", err)
		}
	}
	return nil
}

// insertRun writes the row of this run and keeps its id. Besides the pid it stores what
// tells this process apart from a later one that happens to get the same pid: the kernel's
// boot id and the process's start time (trackSelf).
func (t *tracker) insertRun(args []string) error {
	pid := os.Getpid()
	res, err := database.RetryableExec(t.db,
		`INSERT INTO runs (started, version, host, pid, boot_id, pstart, args) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		trackNow(), config.AppVersion, trackHostname(), pid, trackBootID(), trackProcStart(pid),
		strings.Join(args, " "))
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	t.runID = id
	return nil
}

// sweepInterrupted marks every input still "importing" whose run is gone as
// failed/interrupted, so the next run imports it again. Two kinds of row survive it:
// one held by a run of this host that is really still running (liveRuns), so a second
// importer on the same tracking DB does not steal the inputs of the first one, and one held
// by an unfinished run of another host, which cannot be checked from here (claimInput hands
// that one to -reimport instead).
func (t *tracker) sweepInterrupted() error {
	live, foreign, err := t.liveRuns()
	if err != nil {
		return err
	}
	t.live = live
	t.foreign = foreign
	keep := make([]int64, 0, len(live)+len(foreign))
	for id := range live {
		keep = append(keep, id)
	}
	for id := range foreign {
		keep = append(keep, id)
	}
	query := `UPDATE inputs SET status = ?, reason = ?, finished = ? WHERE status = ?`
	args := []interface{}{trackFailed, "interrupted", trackNow(), trackImporting}
	if len(keep) > 0 {
		marks := make([]string, len(keep))
		for i, id := range keep {
			marks[i] = "?"
			args = append(args, id)
		}
		query += " AND (last_run IS NULL OR last_run NOT IN (" + strings.Join(marks, ",") + "))"
	}
	res, err := database.RetryableExec(t.db, query, args...)
	if err != nil {
		return fmt.Errorf("sweep interrupted inputs: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		log.Printf("[MBOX-IMPORT] tracking: %d input(s) left importing by an earlier run marked %s/interrupted", n, trackFailed)
	}
	return nil
}

// liveRuns reads the unfinished run rows: live are the ones whose process is still running
// on this host (this run included), foreign maps the id of every unfinished run of another
// host to that host name. Both are written once, at open, and only read afterwards.
func (t *tracker) liveRuns() (live map[int64]bool, foreign map[int64]string, err error) {
	self := trackSelf()
	rows, err := database.RetryableQuery(t.db,
		`SELECT id, COALESCE(host, ''), COALESCE(pid, 0), COALESCE(boot_id, ''), COALESCE(pstart, 0)
			FROM runs WHERE finished IS NULL`)
	if err != nil {
		return nil, nil, fmt.Errorf("read runs: %w", err)
	}
	defer rows.Close()
	live, foreign = make(map[int64]bool), make(map[int64]string)
	for rows.Next() {
		var other trackProc
		var id int64
		if err := rows.Scan(&id, &other.host, &other.pid, &other.bootID, &other.pstart); err != nil {
			return nil, nil, fmt.Errorf("read runs: %w", err)
		}
		switch {
		case id == t.runID || trackProcAlive(other, self):
			live[id] = true
		case other.host != self.host:
			foreign[id] = other.host
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read runs: %w", err)
	}
	return live, foreign, nil
}

// trackProc identifies the process of a run row.
type trackProc struct {
	host   string
	pid    int
	bootID string // the kernel's boot id, "" when it could not be read
	pstart int64  // the process start time of /proc/<pid>/stat, 0 when it could not be read
}

// trackSelf is the identity of this process.
func trackSelf() trackProc {
	pid := os.Getpid()
	return trackProc{host: trackHostname(), pid: pid, bootID: trackBootID(), pstart: trackProcStart(pid)}
}

// trackProcAlive reports whether the process of a run row is still running. It is identity
// aware: another host is never "alive" here (liveRuns keeps those separately), a row written
// before the last reboot is not (another boot id), and a pid the kernel handed out again is
// not either (another process start time). Only where /proc gave neither a boot id nor a
// start time (not Linux) does it fall back to the plain "does this pid exist" test, which
// cannot tell a recycled pid apart.
func trackProcAlive(row, self trackProc) bool {
	if row.pid <= 0 || row.host != self.host {
		return false
	}
	if row.bootID != "" && self.bootID != "" {
		if row.bootID != self.bootID {
			return false // the row is from before a reboot: that process is gone
		}
		if row.pstart > 0 {
			return trackProcStart(row.pid) == row.pstart
		}
	}
	return trackPidAlive(row.pid)
}

// trackHostname is os.Hostname with "" on error.
func trackHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

// trackBootID is the kernel's boot id, a new value after every reboot ("" when it cannot be
// read, for example where /proc is not mounted).
func trackBootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// trackProcStart is the start time of a process (field 22 of /proc/<pid>/stat, in clock
// ticks since boot), 0 when it cannot be read. Two processes with the same pid and the same
// boot id but different start times are different processes. Field 2 (the command) may hold
// blanks and brackets, so the fields are counted after the last ')': field 3 comes first.
func trackProcStart(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 || i+2 >= len(b) {
		return 0
	}
	fields := strings.Fields(string(b[i+2:]))
	const startTimeField = 22 // of /proc/<pid>/stat, counting from 1
	if len(fields) < startTimeField-2 {
		return 0
	}
	v, err := strconv.ParseInt(fields[startTimeField-3], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// trackPidAlive reports whether a process with this pid exists (signal 0). It cannot tell a
// recycled pid apart: only trackProcAlive's identity test can.
func trackPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// pruneLog deletes the log rows of all but the newest keepRuns runs (0: keep everything).
func (t *tracker) pruneLog(keepRuns int) error {
	if keepRuns <= 0 {
		return nil
	}
	res, err := database.RetryableExec(t.db,
		`DELETE FROM log WHERE run_id NOT IN (SELECT id FROM runs ORDER BY id DESC LIMIT ?)`, keepRuns)
	if err != nil {
		return fmt.Errorf("prune log rows: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		log.Printf("[MBOX-IMPORT] tracking: %d log row(s) of older runs deleted (-log-keep-runs %d)", n, keepRuns)
	}
	return nil
}

// trackErr logs a tracking error and counts it; it never stops the import.
func (t *tracker) trackErr(what string, err error) {
	if t == nil {
		log.Printf("[MBOX-IMPORT] tracking: %s: %v", what, err)
		return
	}
	t.errs.Add(1)
	log.Printf("[MBOX-IMPORT] tracking: %s: %v", what, err)
}

// remember maps an input path to its row id, for the log tee.
func (t *tracker) remember(path string, id int64) {
	t.mu.Lock()
	t.ids[path] = id
	t.mu.Unlock()
}

// inputIDFor returns the row id of the input a log line belongs to, 0 when the line names
// no file (or an unknown one).
func (t *tracker) inputIDFor(msg string) int64 {
	path := trackFilePath(msg)
	if path == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ids[path]
}

// trackFilePath returns the path of the file="..." field of a log line, "" when there is
// none. Every per-input line of the importer carries it (log.Printf with %q).
func trackFilePath(msg string) string {
	i := strings.Index(msg, ` file="`)
	if i < 0 {
		return ""
	}
	quoted := msg[i+len(` file=`):]
	for k := 1; k < len(quoted); k++ {
		switch quoted[k] {
		case '\\':
			k++
		case '"':
			path, err := strconv.Unquote(quoted[:k+1])
			if err != nil {
				return ""
			}
			return path
		}
	}
	return ""
}

// recordPlanned inserts or refreshes the row of in without importing it: a supported input
// becomes "pending", an unsupported one "unsupported" with its Reason. A row that is done
// or held by a run keeps its status, size and mtime (claimInput compares them with the file
// on disk). Wave 2 of the plan calls this for every input of a scan.
func (t *tracker) recordPlanned(in plannedInput) error {
	if t == nil {
		return nil
	}
	return database.RetryableTransactionExec(t.db, func(tx *sql.Tx) error {
		var (
			id     int64
			status string
		)
		err := tx.QueryRow(`SELECT id, status FROM inputs WHERE path = ?`, in.Path).Scan(&id, &status)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.Exec(`INSERT INTO inputs (path, source, identifier, name, size, mtime, md5, status, reason, first_run, last_run)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				in.Path, in.Source, in.Identifier, in.Name, in.Size, in.Mtime, in.MD5,
				trackPlannedStatus(in), in.Reason, t.runID, t.runID)
			return err
		case err != nil:
			return err
		case status == trackDone || status == trackImporting:
			// Keep everything the import wrote: status, size, mtime and md5 are what
			// claimInput compares the file on disk with, so a new md5 from the source
			// would make a changed file look like the one that was imported.
			_, err = tx.Exec(`UPDATE inputs SET identifier = ?, name = ? WHERE id = ?`,
				in.Identifier, in.Name, id)
			return err
		}
		_, err = tx.Exec(`UPDATE inputs SET source = ?, identifier = ?, name = ?, size = ?, mtime = ?,
			md5 = CASE WHEN ? != '' THEN ? ELSE md5 END, status = ?, reason = ? WHERE id = ?`,
			in.Source, in.Identifier, in.Name, in.Size, in.Mtime, in.MD5, in.MD5,
			trackPlannedStatus(in), in.Reason, id)
		return err
	})
}

// trackPlannedStatus is the status a planned input is recorded with.
func trackPlannedStatus(in plannedInput) string {
	if in.Supported {
		return trackPending
	}
	return trackUnsupported
}

// claimState is what claimInput found.
type claimState int

const (
	claimTake claimState = iota // the input is ours to import
	claimDone                   // the same file was imported before (-reimport ignores this)
	claimHeld                   // another live run is importing it
)

// claimInput takes the input for this run: it inserts or updates its row to "importing" in
// one BEGIN IMMEDIATE transaction, so two runs on the same tracking DB never import the
// same input. It returns claimDone when the row says done and the file on disk is the same
// one (size, mtime, and the md5 when both are known), claimHeld when a live run holds it.
// With a nil tracker every input is claimTake with id 0.
func (t *tracker) claimInput(in plannedInput) (int64, claimState, error) {
	if t == nil {
		return 0, claimTake, nil
	}
	var (
		id    int64
		state claimState
	)
	err := database.RetryableTransactionExec(t.db, func(tx *sql.Tx) error {
		id, state = 0, claimTake
		var (
			rowID   int64
			status  string
			size    sql.NullInt64
			mtime   sql.NullInt64
			md5     sql.NullString
			lastRun sql.NullInt64
		)
		err := tx.QueryRow(`SELECT id, status, size, mtime, md5, last_run FROM inputs WHERE path = ?`, in.Path).
			Scan(&rowID, &status, &size, &mtime, &md5, &lastRun)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.Exec(`INSERT INTO inputs (path, source, identifier, name, size, mtime, md5, status, first_run, last_run, started)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				in.Path, in.Source, in.Identifier, in.Name, in.Size, in.Mtime, in.MD5,
				trackImporting, t.runID, t.runID, trackNow())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		case err != nil:
			return err
		case status == trackImporting && !t.takeHeld(in.Path, lastRun):
			id, state = rowID, claimHeld
			return nil
		case status == trackDone && !t.reimport && trackSameFile(in, size, mtime, md5):
			id, state = rowID, claimDone
			return nil
		}
		id = rowID
		_, err = tx.Exec(`UPDATE inputs SET source = ?, identifier = ?, name = ?, size = ?, mtime = ?,
			md5 = CASE WHEN ? != '' THEN ? ELSE md5 END, status = ?, reason = NULL,
			read = 0, stored = 0, present = 0, skipped = 0, skipped_detail = NULL, rejected = 0,
			errors = 0, parser_skipped = 0, near_misses = 0,
			first_run = COALESCE(first_run, ?), last_run = ?, started = ?, finished = NULL
			WHERE id = ?`,
			in.Source, in.Identifier, in.Name, in.Size, in.Mtime, in.MD5, in.MD5,
			trackImporting, t.runID, t.runID, trackNow(), rowID)
		return err
	})
	if err != nil {
		return 0, claimTake, err
	}
	if state == claimTake {
		t.remember(in.Path, id)
	}
	return id, state, nil
}

// takeHeld reports whether this run takes an input row that still says "importing" away
// from the run that holds it. Only -reimport does that, and only when the holder is not a
// live run of this host: that leaves a row of an unfinished run of another host (which this
// host cannot check) and a row whose run is gone from the table, the two cases the sweep at
// start cannot clear. It logs one line per row it takes.
func (t *tracker) takeHeld(path string, lastRun sql.NullInt64) bool {
	if !t.reimport {
		return false
	}
	if lastRun.Valid && t.live[lastRun.Int64] {
		return false // that importer is running right now: leave its input alone
	}
	switch {
	case !lastRun.Valid:
		log.Printf("[MBOX-IMPORT] tracking: -reimport takes input %q: it is marked %s without a run", path, trackImporting)
	default:
		host := t.foreign[lastRun.Int64]
		if host == "" {
			host = "(unknown)"
		}
		log.Printf("[MBOX-IMPORT] tracking: -reimport takes input %q from run %d on host %q",
			path, lastRun.Int64, host)
	}
	return true
}

// trackSameFile reports whether the file of in is the one the row was written for: the same
// size and mtime, and the same md5 when the row and the source both name one.
func trackSameFile(in plannedInput, size, mtime sql.NullInt64, md5 sql.NullString) bool {
	if !size.Valid || !mtime.Valid || size.Int64 != in.Size || mtime.Int64 != in.Mtime {
		return false
	}
	if in.MD5 != "" && md5.Valid && md5.String != "" && !strings.EqualFold(in.MD5, md5.String) {
		return false
	}
	return true
}

// finishInput writes the result of an input: "done", "failed" (input error, article errors
// or a failed commit wait) or "stopped" (a signal ended it). c is nil when the input could
// not be opened.
func (t *tracker) finishInput(id int64, c *counters, inputErr, stopped bool) {
	if t == nil || id == 0 {
		return
	}
	status, reason := trackDone, ""
	switch {
	case stopped:
		status, reason = trackStopped, "shutdown signal"
	case inputErr:
		status, reason = trackFailed, "input error"
	case c == nil:
		status, reason = trackFailed, "not opened"
	case c.Errors > 0:
		status, reason = trackFailed, fmt.Sprintf("%d article error(s)", c.Errors)
	case c.CommitFailed:
		status, reason = trackFailed, "commit wait failed"
	}
	if c == nil {
		c = newCounters("")
	}
	var group interface{}
	if c.Group != "" {
		group = c.Group
	}
	if _, err := database.RetryableExec(t.db,
		`UPDATE inputs SET status = ?, reason = ?, group_name = ?, read = ?, stored = ?, present = ?,
			skipped = ?, skipped_detail = ?, rejected = ?, errors = ?, parser_skipped = ?,
			near_misses = ?, finished = ? WHERE id = ?`,
		status, reason, group, c.Read, c.Stored, c.Present, c.skippedTotal(), c.skippedDetail(),
		c.Rejected, c.Errors, c.ParserSkipped, c.NearMisses, trackNow(), id); err != nil {
		t.trackErr(fmt.Sprintf("input %d", id), err)
	}
}

// finishRun writes the totals and the exit code of the run.
func (t *tracker) finishRun(res *runSummary, code int) {
	if t == nil {
		return
	}
	c := newCounters("")
	inputs := 0
	if res != nil {
		inputs = res.Inputs
		if res.Total != nil {
			c = res.Total
		}
	}
	if _, err := database.RetryableExec(t.db,
		`UPDATE runs SET finished = ?, exit_code = ?, inputs = ?, read = ?, stored = ?, present = ?,
			skipped = ?, rejected = ?, errors = ? WHERE id = ?`,
		trackNow(), code, inputs, c.Read, c.Stored, c.Present, c.skippedTotal(), c.Rejected,
		c.Errors, t.runID); err != nil {
		t.trackErr("run totals", err)
	}
}

// close finishes the run row, stops the log tee (the log output is restored first, so no
// line can arrive after the channel is closed), writes the dropped-line count and closes
// the database. It is the last thing a run does.
func (t *tracker) close(res *runSummary, code int) {
	if t == nil {
		return
	}
	t.finishRun(res, code)
	var dropped int64
	if t.tee != nil {
		log.SetOutput(t.prevLog)
		dropped = t.tee.stop()
	}
	if n := t.errs.Load(); n > 0 {
		log.Printf("[MBOX-IMPORT] tracking: %d database error(s) while recording this run", n)
	}
	if err := t.db.Close(); err != nil {
		log.Printf("[MBOX-IMPORT] tracking: close %q: %v", t.path, err)
	}
	if dropped > 0 {
		log.Printf("[MBOX-IMPORT] tracking: %d log line(s) dropped: the log writer could not keep up", dropped)
	}
}

// trackLogLine is one line of the process's log output.
type trackLogLine struct {
	ts  string
	msg string
}

// logTee is the non-blocking log sink of a run: log.SetOutput writes every line to the
// previous output and to this writer, which puts it on a buffered channel. One goroutine
// reads the channel and inserts the lines in batches. A full channel drops the line and
// counts it: the import is never slowed down or blocked by the tracking DB.
//
// The line channel is never closed; stop() closes quit instead. A Write that arrives while
// or after the writer stops therefore cannot panic on a closed channel: it is dropped and
// counted, whoever logs it and whenever (the database and processor goroutines log from
// their own goroutines, and log.SetOutput only orders the lines of the standard logger).
type logTee struct {
	t       *tracker
	lines   chan trackLogLine
	quit    chan struct{} // closed by stop: the writer drains and returns
	done    chan struct{} // closed by the writer when it has returned
	dropped atomic.Int64
	closed  atomic.Bool // set by stop before quit is closed: Write only counts from then on
}

// startLogTee installs the tee as the log output of the process and starts its writer.
func (t *tracker) startLogTee() {
	tee := &logTee{
		t:     t,
		lines: make(chan trackLogLine, trackLogQueue),
		quit:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	t.tee = tee
	t.prevLog = log.Writer()
	go tee.run()
	log.SetOutput(io.MultiWriter(t.prevLog, tee))
}

// Write never blocks and never fails: it splits the log output into lines and queues them.
func (w *logTee) Write(p []byte) (int, error) {
	ts := trackNow()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		if w.closed.Load() {
			w.dropped.Add(1) // the writer is gone: the line only reaches the real output
			continue
		}
		select {
		case w.lines <- trackLogLine{ts: ts, msg: trackStripLogPrefix(line)}:
		default:
			w.dropped.Add(1)
		}
	}
	return len(p), nil
}

// trackStripLogPrefix removes the standard logger's own date and time
// ("2006/01/02 15:04:05 ") from a line: the ts column holds the time. Only exactly that
// shape is removed, so a line written with other log flags (or none) is kept as it is.
func trackStripLogPrefix(line string) string {
	const shape = "0000/00/00 00:00:00 "
	if len(line) < len(shape) {
		return line
	}
	for i := 0; i < len(shape); i++ {
		switch shape[i] {
		case '0':
			if line[i] < '0' || line[i] > '9' {
				return line
			}
		default:
			if line[i] != shape[i] {
				return line
			}
		}
	}
	return line[len(shape):]
}

// stop stops the writer, waits for it to insert what is left, records the number of dropped
// lines as one last row and returns it. The caller has already taken the tee out of the
// logger, so in the normal case no Write can be in flight; a late one is counted, not lost
// in a panic.
func (w *logTee) stop() int64 {
	w.closed.Store(true)
	close(w.quit)
	<-w.done
	// lines that were queued but no longer inserted (a Write that raced with the stop)
	if left := len(w.lines); left > 0 {
		w.dropped.Add(int64(left))
	}
	dropped := w.dropped.Load()
	if dropped > 0 {
		msg := fmt.Sprintf("[MBOX-IMPORT] tracking: %d log line(s) dropped: the log writer could not keep up", dropped)
		if _, err := database.RetryableExec(w.t.db,
			`INSERT INTO log (run_id, input_id, ts, msg) VALUES (?, NULL, ?, ?)`,
			w.t.runID, trackNow(), msg); err != nil {
			w.t.trackErr("drop count", err)
		}
	}
	return dropped
}

// run inserts the queued lines in batches of trackLogBatch, or every trackLogFlush, until
// stop closes quit; it then drains what is queued and inserts that too. It stops writing
// after trackLogMaxFails failed inserts in a row (the lines are then counted as dropped) so
// a broken tracking DB cannot stall or flood the run.
func (w *logTee) run() {
	defer close(w.done)
	ticker := time.NewTicker(trackLogFlush)
	defer ticker.Stop()
	batch := make([]trackLogLine, 0, trackLogBatch)
	fails := 0
	for {
		select {
		case line := <-w.lines:
			batch = append(batch, line)
			if len(batch) >= trackLogBatch {
				w.flush(&batch, &fails)
			}
		case <-ticker.C:
			w.flush(&batch, &fails)
		case <-w.quit:
			w.drain(&batch, &fails)
			return
		}
	}
}

// drain inserts everything that is still queued and returns.
func (w *logTee) drain(batch *[]trackLogLine, fails *int) {
	for {
		select {
		case line := <-w.lines:
			*batch = append(*batch, line)
			if len(*batch) >= trackLogBatch {
				w.flush(batch, fails)
			}
		default:
			w.flush(batch, fails)
			return
		}
	}
}

// flush inserts and empties batch, counting a failed insert.
func (w *logTee) flush(batch *[]trackLogLine, fails *int) {
	lines := *batch
	*batch = (*batch)[:0]
	if len(lines) == 0 {
		return
	}
	if *fails >= trackLogMaxFails {
		w.dropped.Add(int64(len(lines)))
		return
	}
	if err := w.insert(lines); err != nil {
		*fails++
		w.dropped.Add(int64(len(lines)))
		w.t.errs.Add(1) // reported by close; trackErr would log from inside the writer
		// the message goes through the tee itself: it is queued, never written from here
		if *fails >= trackLogMaxFails {
			log.Printf("[MBOX-IMPORT] tracking: log insert failed %d times, no more log rows this run: %v", *fails, err)
		} else {
			log.Printf("[MBOX-IMPORT] tracking: log insert failed: %v", err)
		}
		return
	}
	*fails = 0
}

// insert writes one batch of log lines in a single transaction.
func (w *logTee) insert(lines []trackLogLine) error {
	return database.RetryableTransactionExec(w.t.db, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO log (run_id, input_id, ts, msg) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, line := range lines {
			var inputID interface{}
			if id := w.t.inputIDFor(line.msg); id != 0 {
				inputID = id
			}
			if _, err := database.RetryableStmtExec(stmt, w.t.runID, inputID, line.ts, line.msg); err != nil {
				return err
			}
		}
		return nil
	})
}

// plannedFromPaths turns the positional inputs of a run into plannedInputs: source cli, with
// the size and mtime the file has now (the tracking DB compares them with the row of an
// earlier import). Every positional input counts as supported, whatever its name: the open
// decides, as it does without the tracking DB.
//
// The path is made absolute and cleaned, because it is the key of the inputs table: the same
// file named "./g.mbox" and "/tmp/x/g.mbox" has to be one row, not two. A path that cannot
// be made absolute (no working directory) is kept as it was.
func plannedFromPaths(files []string) []plannedInput {
	out := make([]plannedInput, 0, len(files))
	for _, file := range files {
		path := file
		if abs, err := filepath.Abs(file); err == nil {
			path = abs
		} else {
			path = filepath.Clean(file)
		}
		in := plannedInput{Path: path, Source: sourceCLI, Name: filepath.Base(path), Supported: true}
		if fi, err := os.Stat(path); err == nil {
			in.Size = fi.Size()
			in.Mtime = fi.ModTime().Unix()
		}
		out = append(out, in)
	}
	return out
}

// dedupePlanned keeps the first plannedInput of every path. The path is the key of the
// inputs table and of the log tee's line-to-input map, and two workers on one path would
// claim it twice (the second one would see "importing"), so a repeated input is dropped
// before the import starts. One line is logged per dropped repeat.
func dedupePlanned(inputs []plannedInput) []plannedInput {
	seen := make(map[string]bool, len(inputs))
	out := make([]plannedInput, 0, len(inputs))
	for _, in := range inputs {
		if seen[in.Path] {
			log.Printf("[MBOX-IMPORT] input %q given more than once: imported once", in.Path)
			continue
		}
		seen[in.Path] = true
		out = append(out, in)
	}
	return out
}

// statusCount is one status of the inputs table.
type statusCount struct {
	Status string `json:"status"`
	Inputs int64  `json:"inputs"`
	Bytes  int64  `json:"bytes"`
}

// statusFailed is one failed input.
type statusFailed struct {
	Path     string `json:"path"`
	Reason   string `json:"reason"`
	Finished string `json:"finished"`
}

// statusRun is the last run of the tracking DB.
type statusRun struct {
	ID       int64  `json:"id"`
	Started  string `json:"started"`
	Finished string `json:"finished"`
	Version  string `json:"version"`
	ExitCode *int   `json:"exit_code"`
	Inputs   int64  `json:"inputs"`
	Read     int64  `json:"read"`
	Stored   int64  `json:"stored"`
	Present  int64  `json:"present"`
	Skipped  int64  `json:"skipped"`
	Rejected int64  `json:"rejected"`
	Errors   int64  `json:"errors"`
}

// statusReport is what -status prints.
type statusReport struct {
	DB      string         `json:"db"`
	Exists  bool           `json:"exists"`
	Inputs  []statusCount  `json:"inputs"`
	Failed  []statusFailed `json:"failed"`
	LastRun *statusRun     `json:"last_run"`
	LogRows int64          `json:"log_rows"`
}

// writeStatus prints what the tracking DB knows (-status), as text or as JSON (-json). It
// imports nothing and writes nothing to the database.
func writeStatus(w io.Writer, path string, asJSON bool) error {
	report := &statusReport{DB: path}
	if _, err := os.Stat(path); err == nil {
		report.Exists = true
		if err := readStatus(path, report); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return printStatus(w, report)
}

// readStatus fills report from the tracking DB, opened read-only.
func readStatus(path string, report *statusReport) error {
	db, err := sql.Open("sqlite3", trackStatusDSN(path))
	if err != nil {
		return err
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			log.Printf("[MBOX-IMPORT] tracking: close %q: %v", path, cerr)
		}
	}()
	if err := readStatusCounts(db, report); err != nil {
		return err
	}
	if err := readStatusFailed(db, report); err != nil {
		return err
	}
	if err := readStatusLastRun(db, report); err != nil {
		return err
	}
	return database.RetryableQueryRowScan(db, `SELECT COUNT(*) FROM log`, nil, &report.LogRows)
}

// readStatusCounts reads the number of inputs and their bytes per status.
func readStatusCounts(db *sql.DB, report *statusReport) error {
	rows, err := database.RetryableQuery(db,
		`SELECT status, COUNT(*), COALESCE(SUM(size), 0) FROM inputs GROUP BY status ORDER BY status`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c statusCount
		if err := rows.Scan(&c.Status, &c.Inputs, &c.Bytes); err != nil {
			return err
		}
		report.Inputs = append(report.Inputs, c)
	}
	return rows.Err()
}

// readStatusFailed reads the last 10 failed inputs.
func readStatusFailed(db *sql.DB, report *statusReport) error {
	rows, err := database.RetryableQuery(db,
		`SELECT path, COALESCE(reason, ''), COALESCE(finished, '') FROM inputs WHERE status = ?
			ORDER BY finished DESC, id DESC LIMIT 10`, trackFailed)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f statusFailed
		if err := rows.Scan(&f.Path, &f.Reason, &f.Finished); err != nil {
			return err
		}
		report.Failed = append(report.Failed, f)
	}
	return rows.Err()
}

// readStatusLastRun reads the newest run row.
func readStatusLastRun(db *sql.DB, report *statusReport) error {
	rows, err := database.RetryableQuery(db,
		`SELECT id, started, COALESCE(finished, ''), COALESCE(version, ''), exit_code,
			inputs, read, stored, present, skipped, rejected, errors
			FROM runs ORDER BY id DESC LIMIT 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var (
			r    statusRun
			code sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &r.Started, &r.Finished, &r.Version, &code, &r.Inputs,
			&r.Read, &r.Stored, &r.Present, &r.Skipped, &r.Rejected, &r.Errors); err != nil {
			return err
		}
		if code.Valid {
			c := int(code.Int64)
			r.ExitCode = &c
		}
		report.LastRun = &r
	}
	return rows.Err()
}

// printStatus writes the human-readable form of the report.
func printStatus(w io.Writer, report *statusReport) error {
	if _, err := fmt.Fprintf(w, "tracking db: %s\n", report.DB); err != nil {
		return err
	}
	if !report.Exists {
		_, err := fmt.Fprintln(w, "  no tracking database yet (nothing imported with it)")
		return err
	}
	if len(report.Inputs) == 0 {
		if _, err := fmt.Fprintln(w, "inputs: none"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(w, "inputs by status:"); err != nil {
			return err
		}
		for _, c := range report.Inputs {
			if _, err := fmt.Fprintf(w, "  %-12s %8d  %10s\n", c.Status, c.Inputs, trackBytes(c.Bytes)); err != nil {
				return err
			}
		}
	}
	for i, f := range report.Failed {
		if i == 0 {
			if _, err := fmt.Fprintln(w, "last failed inputs:"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "  %s reason=%q finished=%s\n", f.Path, f.Reason, f.Finished); err != nil {
			return err
		}
	}
	if r := report.LastRun; r != nil {
		exit := "-"
		if r.ExitCode != nil {
			exit = strconv.Itoa(*r.ExitCode)
		}
		finished := r.Finished
		if finished == "" {
			finished = "(unfinished)"
		}
		if _, err := fmt.Fprintf(w, "last run: id=%d started=%s finished=%s version=%s exit=%s inputs=%d read=%d stored=%d present=%d skipped=%d rejected=%d errors=%d\n",
			r.ID, r.Started, finished, r.Version, exit, r.Inputs, r.Read, r.Stored, r.Present,
			r.Skipped, r.Rejected, r.Errors); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "log rows: %d\n", report.LogRows)
	return err
}

// trackBytes formats a byte count for -status.
func trackBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", v/unit)
}
