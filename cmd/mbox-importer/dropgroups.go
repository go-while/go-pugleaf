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

// dropChunk is the most group names one IN (...) statement of the delete gets.
const dropChunk = 500

// dropSkip is a listed group that is not deleted, and why.
type dropSkip struct {
	group, reason string
}

// dropResult is the outcome of one -drop-empty-groups run (tests read it as lastDrop).
type dropResult struct {
	listed      int        // distinct names in the list
	deletable   []string   // passed every check (with -dry-run: would be deleted)
	deleted     []string   // newsgroups row gone after the delete
	skipped     []dropSkip // not deleted, with the reason
	checkErrors int        // listed groups whose check failed (skipped as well)
	fileErrors  int        // group database files or directories that could not be removed
}

// lastDrop is the result of the last -drop-empty-groups run of runWith (tests).
var lastDrop *dropResult

// runDropEmptyGroups runs -drop-empty-groups and returns the exit code: 0 done (skipped
// groups included), 1 on an error (list, database, a check that failed, a live import run,
// a file that could not be removed).
func runDropEmptyGroups(listPath, dataDir, trackPath string, dryRun bool) int {
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
	if err := checkNoLiveImport(trackPath); err != nil {
		log.Printf("[MBOX-IMPORT] -drop-empty-groups: %v", err)
		return 1
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

// checkNoLiveImport refuses the cleanup while a run of the tracking database is still going
// on this host. Without a tracking database (none yet, or -trackdb off) nothing can be
// checked: the -h text says not to run it next to an importer.
func checkNoLiveImport(trackPath string) error {
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
	t := &tracker{db: db, path: trackPath} // runID 0: no row of this process
	live, foreign, err := t.liveRuns()
	if err != nil {
		return fmt.Errorf("tracking database %q: %w", trackPath, err)
	}
	for id, host := range foreign {
		log.Printf("[MBOX-IMPORT] drop-empty-groups: run %d of host %q is not finished in the tracking database (not checked: another host)", id, host)
	}
	if len(live) > 0 {
		ids := make([]string, 0, len(live))
		for id := range live {
			ids = append(ids, fmt.Sprint(id))
		}
		return fmt.Errorf("import run(s) %s of the tracking database %q still running: stop them first", strings.Join(ids, ","), trackPath)
	}
	return nil
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

// dropEmptyGroups checks every name and, unless dryRun, deletes the ones that passed: they
// are set inactive and deleted with BulkDeleteNewsgroups (the dependent rows of the web
// admin's bulk delete), then the files of every group whose row is really gone are removed.
// The checks only read (the main database and the group databases are opened read-only).
func dropEmptyGroups(names []string, dataDir, mainDB string, dryRun bool) (*dropResult, error) {
	res := &dropResult{listed: len(names)}
	ro, err := sql.Open("sqlite3", roDSN(mainDB))
	if err != nil {
		return res, fmt.Errorf("open %s: %w", mainDB, err)
	}
	for _, name := range names {
		reason, err := dropCheck(ro, dataDir, name)
		if err != nil {
			res.checkErrors++
			res.skipped = append(res.skipped, dropSkip{name, "check failed: " + err.Error()})
			continue
		}
		if reason != "" {
			res.skipped = append(res.skipped, dropSkip{name, reason})
			continue
		}
		res.deletable = append(res.deletable, name)
	}
	if err := ro.Close(); err != nil {
		return res, fmt.Errorf("close %s: %w", mainDB, err)
	}
	if dryRun || len(res.deletable) == 0 {
		return res, nil
	}

	cfg := database.DefaultDBConfig()
	cfg.DataDir = dataDir
	cfg.BackupDir = filepath.Join(dataDir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		return res, fmt.Errorf("open database: %w", err)
	}
	deleteErr := dropRows(db, res)
	if !shutdown(nil, db) && deleteErr == nil {
		deleteErr = errors.New("database shutdown failed")
	}
	// the files go only after the rows are gone (res.deleted) and the database is closed
	for _, name := range res.deleted {
		res.fileErrors += removeGroupDBFiles(dataDir, name)
	}
	return res, deleteErr
}

// dropCheck returns "" when group may be deleted, else the reason it is skipped. An error is
// a check that could not be made (the group is skipped with it).
func dropCheck(ro *sql.DB, dataDir, group string) (string, error) {
	var count int64
	var active bool
	err := database.RetryableQueryRowScan(ro, `SELECT COALESCE(message_count, 0), active FROM newsgroups WHERE name = ?`,
		[]interface{}{group}, &count, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return "no newsgroups row", nil
	}
	if err != nil {
		return "", fmt.Errorf("read newsgroups row: %w", err)
	}
	if count != 0 {
		return fmt.Sprintf("message_count=%d (active=%t)", count, active), nil
	}
	_, file := groupDBFiles(dataDir, group)
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return "", nil // no group database: nothing in it
	} else if err != nil {
		return "", fmt.Errorf("group database %s: %w", file, err)
	}
	has, err := groupDBHasArticles(file)
	if err != nil {
		return "", fmt.Errorf("group database %s: %w", file, err)
	}
	if has {
		return fmt.Sprintf("group database has articles (message_count=0, active=%t)", active), nil
	}
	return "", nil
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

// dropRows sets the deletable groups inactive and deletes them (BulkDeleteNewsgroups only
// deletes inactive ones), in chunks of dropChunk, then puts every group whose row is gone
// into res.deleted. A group whose row is still there is skipped (it stays inactive).
func dropRows(db *database.Database, res *dropResult) error {
	for start := 0; start < len(res.deletable); start += dropChunk {
		chunk := res.deletable[start:min(start+dropChunk, len(res.deletable))]
		if _, err := db.BulkUpdateNewsgroupActive(chunk, false); err != nil {
			return fmt.Errorf("set %d groups inactive: %w", len(chunk), err)
		}
		n, err := db.BulkDeleteNewsgroups(chunk)
		if err != nil {
			return fmt.Errorf("delete %d groups (set inactive already): %w", len(chunk), err)
		}
		log.Printf("[MBOX-IMPORT] drop-empty-groups: deleted %d of %d newsgroups rows", n, len(chunk))
	}
	for _, name := range res.deletable {
		_, err := db.MainDBGetNewsgroup(name)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res.deleted = append(res.deleted, name)
		case err != nil:
			res.skipped = append(res.skipped, dropSkip{name, fmt.Sprintf("row check after the delete failed (files kept): %v", err)})
		default:
			res.skipped = append(res.skipped, dropSkip{name, "row still there after the delete (now inactive, files kept)"})
		}
	}
	return nil
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
	} else {
		for _, g := range res.deleted {
			log.Printf("[MBOX-IMPORT] drop-empty-groups: deleted group=%q", g)
		}
	}
	log.Printf("[MBOX-IMPORT] drop-empty-groups done: listed=%d empty=%d deleted=%d skipped=%d check-errors=%d file-errors=%d dry-run=%t",
		res.listed, len(res.deletable), len(res.deleted), len(res.skipped), res.checkErrors, res.fileErrors, dryRun)
}
