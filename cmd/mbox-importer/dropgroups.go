package main

// dropgroups.go is the cleanup mode -drop-empty-groups FILE (plan importer-empty-groups):
// before the importer checked an article ahead of creating its group, a run left a group
// behind for every article the processor then refused (control messages, broken headers,
// "From " lines glued onto Newsgroups). This mode deletes such groups again: only the names
// of FILE, and only when the newsgroups row says 0 articles and the group database holds
// none. It must not run while an importer (or anything else) writes to the same -data.

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-while/go-pugleaf/internal/database"
)

// dropChunk is the most group names one IN (...) statement of the delete gets (a variable
// only so the tests can split a short list into several chunks).
var dropChunk = 500

// dropSkip is a listed group that is not deleted, and why.
type dropSkip struct {
	group, reason string
}

// dropResult is the outcome of one -drop-empty-groups run (tests read it as lastDrop).
type dropResult struct {
	listed      int             // distinct names in the list
	deletable   []string        // passed every check (with -dry-run: would be deleted)
	wasActive   map[string]bool // active value of each deletable group as the check read it
	deleted     []string        // newsgroups row gone after the delete of its chunk
	fileOnly    []string        // no newsgroups row, but a group database without articles: files only
	filesGone   []string        // fileOnly groups whose files were removed
	skipped     []dropSkip      // not deleted, with the reason
	checkErrors int             // listed groups whose check failed (skipped as well)
	fileErrors  int             // group database files or directories that could not be removed
}

// lastDrop is the result of the last -drop-empty-groups run of runWith (tests).
var lastDrop *dropResult

// runDropEmptyGroups runs -drop-empty-groups and returns the exit code: 0 done (skipped
// groups included), 1 on an error (list, database, a check that failed, an unfinished
// import run, a delete that failed, a file that could not be removed).
func runDropEmptyGroups(listPath, dataDir, trackPath string, dryRun, forceStale bool) int {
	lastDrop = nil
	names, err := readGroupList(listPath)
	if err != nil {
		log.Printf("[MBOX-IMPORT] -drop-empty-groups: %v", err)
		return 1
	}
	mainDB := filepath.Join(dataDir, "cfg", "pugleaf.sq3")
	if _, err := os.Stat(mainDB); err != nil {
		// never create a database here: a wrong -data must not end in a fresh one
		log.Printf("[MBOX-IMPORT] -drop-empty-groups: no main database at %q: %v", mainDB, err)
		return 1
	}
	if err := checkNoImportRuns(trackPath, forceStale); err != nil {
		if !dryRun {
			log.Printf("[MBOX-IMPORT] -drop-empty-groups: %v", err)
			return 1
		}
		log.Printf("[MBOX-IMPORT] drop-empty-groups: dry run goes on (it changes nothing), a real run would refuse: %v", err)
	}
	log.Printf("[MBOX-IMPORT] drop-empty-groups: list=%q groups=%d data=%q dry-run=%t", listPath, len(names), dataDir, dryRun)
	res, err := dropEmptyGroups(names, dataDir, mainDB, dryRun)
	lastDrop = res
	if res != nil {
		logDropResult(res, dryRun)
	}
	if err != nil {
		log.Printf("[MBOX-IMPORT] -drop-empty-groups: %v", err)
		return 1
	}
	if res.checkErrors > 0 || res.fileErrors > 0 {
		return 1
	}
	return 0
}

// readGroupList reads one group name per line; blank lines and lines starting with '#' are
// left out, a name listed twice counts once.
func readGroupList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read only
	seen := make(map[string]bool)
	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name := strings.TrimSpace(sc.Text())
		if name == "" || strings.HasPrefix(name, "#") || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return names, nil
}

// checkNoImportRuns refuses the cleanup while the tracking database has an unfinished run.
// A run whose process is still alive on this host is always refused. Any other unfinished
// run (of another host, which cannot be checked from here, or of a process of this host
// that is gone, e.g. killed or crashed) is refused too unless forceStale says these runs
// are known to be dead. Without a tracking database (none yet, or -trackdb off) nothing can
// be checked: the -h text says not to run the cleanup next to an importer.
func checkNoImportRuns(trackPath string, forceStale bool) error {
	if trackPath == "" {
		log.Printf("[MBOX-IMPORT] drop-empty-groups: -trackdb off: cannot check for a running import")
		return nil
	}
	if _, err := os.Stat(trackPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("tracking database %q: %w", trackPath, err)
	}
	db, err := sql.Open("sqlite3", trackStatusDSN(trackPath))
	if err != nil {
		return fmt.Errorf("tracking database %q: %w", trackPath, err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			log.Printf("[MBOX-IMPORT] drop-empty-groups: close tracking database %q: %v", trackPath, cerr)
		}
	}()
	live, stale, err := unfinishedRuns(db)
	if err != nil {
		return fmt.Errorf("tracking database %q: %w", trackPath, err)
	}
	if len(live) > 0 {
		return fmt.Errorf("import run(s) %s of the tracking database %q still running on this host: stop them first",
			strings.Join(live, ", "), trackPath)
	}
	if len(stale) == 0 {
		return nil
	}
	if !forceStale {
		return fmt.Errorf("unfinished import run(s) %s in the tracking database %q (another host, or a process that is gone): "+
			"stop them first, or, when they are known to be dead (crashed), run again with -force-stale-runs",
			strings.Join(stale, ", "), trackPath)
	}
	log.Printf("[MBOX-IMPORT] drop-empty-groups: -force-stale-runs: unfinished run(s) %s taken as dead", strings.Join(stale, ", "))
	return nil
}

// unfinishedRuns reads the runs rows without a finished time: live are the ones whose
// process still runs on this host (trackProcAlive), stale all others. Each entry reads
// "id (host pid)".
func unfinishedRuns(db *sql.DB) (live, stale []string, err error) {
	rows, err := database.RetryableQuery(db,
		`SELECT id, COALESCE(host, ''), COALESCE(pid, 0), COALESCE(boot_id, ''), COALESCE(pstart, 0)
			FROM runs WHERE finished IS NULL ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("read runs: %w", err)
	}
	defer rows.Close()
	self := trackSelf()
	for rows.Next() {
		var row trackProc
		var id int64
		if err := rows.Scan(&id, &row.host, &row.pid, &row.bootID, &row.pstart); err != nil {
			return nil, nil, fmt.Errorf("read runs: %w", err)
		}
		desc := fmt.Sprintf("%d (host %q pid %d)", id, row.host, row.pid)
		if trackProcAlive(row, self) {
			live = append(live, desc)
		} else {
			stale = append(stale, desc)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read runs: %w", err)
	}
	return live, stale, nil
}

// groupDBFiles returns the directory and the database file of group, the layout GetGroupDB
// creates: <data>/db/<md5 of the name>/<sanitized name>.db.
func groupDBFiles(dataDir, group string) (dir, file string) {
	dir = filepath.Join(dataDir, "db", database.MD5Hash(group))
	return dir, filepath.Join(dir, database.SanitizeGroupName(group)+".db")
}

// roDSN is a read-only DSN for the mattn driver (escaped like trackDSN).
func roDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=ro&_busy_timeout=10000"
}

// dropVerdict is what dropCheck decided for one listed group.
type dropVerdict int

const (
	dropSkipIt   dropVerdict = iota // leave it alone (reason says why)
	dropDelete                      // row with 0 articles, no article in the group database
	dropFileOnly                    // no row, a group database without articles: remove its files
)

// dropEmptyGroups checks every name and, unless dryRun, deletes the ones that passed, chunk by
// chunk: set inactive, deleted with BulkDeleteNewsgroups (the dependent rows of the web
// admin's bulk delete), the rows checked gone. A chunk whose delete fails gets its groups'
// earlier active values back, and the next chunks still run. Then the files of every group
// whose row is really gone, and of every file-only group, are removed. The checks only read
// (the main database and the group databases are opened read-only).
func dropEmptyGroups(names []string, dataDir, mainDB string, dryRun bool) (*dropResult, error) {
	res := &dropResult{listed: len(names), wasActive: make(map[string]bool)}
	ro, err := sql.Open("sqlite3", roDSN(mainDB))
	if err != nil {
		return res, fmt.Errorf("open %s: %w", mainDB, err)
	}
	for _, name := range names {
		verdict, reason, active, err := dropCheck(ro, dataDir, name)
		switch {
		case err != nil:
			res.checkErrors++
			res.skipped = append(res.skipped, dropSkip{name, "check failed: " + err.Error()})
		case verdict == dropDelete:
			res.deletable = append(res.deletable, name)
			res.wasActive[name] = active
		case verdict == dropFileOnly:
			res.fileOnly = append(res.fileOnly, name)
		default:
			res.skipped = append(res.skipped, dropSkip{name, reason})
		}
	}
	if err := ro.Close(); err != nil {
		return res, fmt.Errorf("close %s: %w", mainDB, err)
	}
	if dryRun {
		return res, nil
	}

	var deleteErr error
	if len(res.deletable) > 0 {
		cfg := database.DefaultDBConfig()
		cfg.DataDir = dataDir
		cfg.BackupDir = filepath.Join(dataDir, "backups")
		db, err := database.OpenDatabase(cfg)
		if err != nil {
			return res, fmt.Errorf("open database: %w", err)
		}
		deleteErr = dropRows(db, res)
		if !shutdown(nil, db) {
			deleteErr = errors.Join(deleteErr, errors.New("database shutdown failed"))
		}
	}
	// the files go only after the rows are gone (res.deleted) and the database is closed;
	// also when a later chunk failed, so no deleted group keeps orphan files
	for _, name := range res.deleted {
		res.fileErrors += removeGroupDBFiles(dataDir, name)
	}
	for _, name := range res.fileOnly {
		if n := removeGroupDBFiles(dataDir, name); n > 0 {
			res.fileErrors += n
			continue
		}
		res.filesGone = append(res.filesGone, name)
	}
	return res, deleteErr
}

// dropCheck decides for one listed group: dropDelete when its row says 0 articles (active is
// the row's active value) and its group database has none, dropFileOnly when it has no row
// but a group database without articles (a group whose row an earlier cleanup deleted before
// it could remove the files), else dropSkipIt with the reason. A group database with an
// article is never touched. An error is a check that could not be made.
func dropCheck(ro *sql.DB, dataDir, group string) (verdict dropVerdict, reason string, active bool, err error) {
	var count int64
	err = database.RetryableQueryRowScan(ro, `SELECT COALESCE(message_count, 0), active FROM newsgroups WHERE name = ?`,
		[]interface{}{group}, &count, &active)
	noRow := errors.Is(err, sql.ErrNoRows)
	if err != nil && !noRow {
		return dropSkipIt, "", false, fmt.Errorf("read newsgroups row: %w", err)
	}
	if !noRow && count != 0 {
		return dropSkipIt, fmt.Sprintf("message_count=%d (active=%t)", count, active), active, nil
	}
	_, file := groupDBFiles(dataDir, group)
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		if noRow {
			return dropSkipIt, "no newsgroups row", false, nil
		}
		return dropDelete, "", active, nil // no group database: nothing in it
	} else if err != nil {
		return dropSkipIt, "", false, fmt.Errorf("group database %s: %w", file, err)
	}
	has, err := groupDBHasArticles(file)
	if err != nil {
		return dropSkipIt, "", false, fmt.Errorf("group database %s: %w", file, err)
	}
	switch {
	case has && noRow:
		return dropSkipIt, "no newsgroups row, group database has articles (kept)", false, nil
	case has:
		return dropSkipIt, fmt.Sprintf("group database has articles (message_count=0, active=%t)", active), active, nil
	case noRow:
		return dropFileOnly, "", false, nil
	}
	return dropDelete, "", active, nil
}

// groupDBHasArticles opens a group database read-only and reports whether its articles
// table has a row.
func groupDBHasArticles(file string) (bool, error) {
	gdb, err := sql.Open("sqlite3", roDSN(file))
	if err != nil {
		return false, err
	}
	var has bool
	err = database.RetryableQueryRowScan(gdb, `SELECT EXISTS (SELECT 1 FROM articles LIMIT 1)`, nil, &has)
	if cerr := gdb.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return has, err
}

// dropRows deletes the deletable groups in chunks of dropChunk: set inactive
// (BulkDeleteNewsgroups only deletes inactive groups), delete, then every group of the chunk
// whose row is gone goes into res.deleted right away. When the delete of a chunk fails (its
// transaction normally rolled back), the groups of the chunk that were active are set active
// again and the rows are checked all the same (a row that is gone anyway still counts as
// deleted); the remaining chunks still run. The errors of all chunks are returned together.
func dropRows(db *database.Database, res *dropResult) error {
	var errs []error
	for start := 0; start < len(res.deletable); start += dropChunk {
		chunk := res.deletable[start:min(start+dropChunk, len(res.deletable))]
		if _, err := db.BulkUpdateNewsgroupActive(chunk, false); err != nil {
			// one transaction: nothing changed
			errs = append(errs, fmt.Errorf("set %d groups inactive: %w", len(chunk), err))
			dropSkipChunk(res, chunk, fmt.Sprintf("set inactive failed (unchanged): %v", err))
			continue
		}
		stillThere := "row still there after the delete (now inactive, files kept)"
		n, err := db.BulkDeleteNewsgroups(chunk)
		if err != nil {
			errs = append(errs, fmt.Errorf("delete %d groups: %w", len(chunk), err))
			stillThere = fmt.Sprintf("delete failed (%v): %s, files kept", err, dropRestoreActive(db, res, chunk))
		} else {
			log.Printf("[MBOX-IMPORT] drop-empty-groups: deleted %d of %d newsgroups rows", n, len(chunk))
		}
		for _, name := range chunk {
			_, err := db.MainDBGetNewsgroup(name)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				res.deleted = append(res.deleted, name)
			case err != nil:
				res.skipped = append(res.skipped, dropSkip{name, fmt.Sprintf("row check after the delete failed (files kept): %v", err)})
			default:
				res.skipped = append(res.skipped, dropSkip{name, stillThere})
			}
		}
	}
	return errors.Join(errs...)
}

// dropRestoreActive sets the groups of chunk that were active before the cleanup active
// again (the inactive ones stay as they were) and says how that went.
func dropRestoreActive(db *database.Database, res *dropResult, chunk []string) string {
	var wasActive []string
	for _, name := range chunk {
		if res.wasActive[name] {
			wasActive = append(wasActive, name)
		}
	}
	if len(wasActive) == 0 {
		return "none was active before"
	}
	if _, err := db.BulkUpdateNewsgroupActive(wasActive, true); err != nil {
		log.Printf("[MBOX-IMPORT] drop-empty-groups: restoring active=1 of %d groups failed, they stay inactive: %v", len(wasActive), err)
		return fmt.Sprintf("restoring active=1 of %d groups failed, they stay inactive: %v", len(wasActive), err)
	}
	return fmt.Sprintf("active=1 restored for %d groups", len(wasActive))
}

// dropSkipChunk reports every group of a chunk that was not deleted.
func dropSkipChunk(res *dropResult, chunk []string, reason string) {
	for _, name := range chunk {
		res.skipped = append(res.skipped, dropSkip{name, reason})
	}
}

// removeGroupDBFiles removes the database files of group (.db, -wal, -shm) and its md5
// directory when that is empty then. It returns the number of removals that failed.
func removeGroupDBFiles(dataDir, group string) int {
	dir, file := groupDBFiles(dataDir, group)
	failed := 0
	for _, p := range []string{file, file + "-wal", file + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[MBOX-IMPORT] drop-empty-groups: group=%q: remove %s: %v", group, p, err)
			failed++
		}
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		log.Printf("[MBOX-IMPORT] drop-empty-groups: group=%q: read %s: %v", group, dir, err)
		failed++
	case len(entries) == 0:
		if err := os.Remove(dir); err != nil {
			log.Printf("[MBOX-IMPORT] drop-empty-groups: group=%q: remove %s: %v", group, dir, err)
			failed++
		}
	}
	return failed
}

// logDropResult logs a line per listed group and the summary line.
func logDropResult(res *dropResult, dryRun bool) {
	for _, s := range res.skipped {
		log.Printf("[MBOX-IMPORT] drop-empty-groups: skipped group=%q: %s", s.group, s.reason)
	}
	if dryRun {
		for _, g := range res.deletable {
			log.Printf("[MBOX-IMPORT] drop-empty-groups: would delete group=%q", g)
		}
		for _, g := range res.fileOnly {
			log.Printf("[MBOX-IMPORT] drop-empty-groups: would remove the files of group=%q (no newsgroups row, no article in its group database)", g)
		}
	} else {
		for _, g := range res.deleted {
			log.Printf("[MBOX-IMPORT] drop-empty-groups: deleted group=%q", g)
		}
		for _, g := range res.filesGone {
			log.Printf("[MBOX-IMPORT] drop-empty-groups: removed the files of group=%q (no newsgroups row, file-only cleanup)", g)
		}
	}
	log.Printf("[MBOX-IMPORT] drop-empty-groups done: listed=%d empty=%d deleted=%d file-only=%d files-removed=%d skipped=%d check-errors=%d file-errors=%d dry-run=%t",
		res.listed, len(res.deletable), len(res.deleted), len(res.fileOnly), len(res.filesGone), len(res.skipped), res.checkErrors, res.fileErrors, dryRun)
}
