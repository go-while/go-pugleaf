// Package processor in this file imports RockSolid Light legacy configurations
package processor

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	_ "github.com/mattn/go-sqlite3"
)

var error_empty_article = fmt.Errorf("empty article") // error for empty articles

// errLegacyDuplicate is returned by importLegacyArticle when the article is already known.
// The caller counts it as a skipped duplicate, not as a failure.
var errLegacyDuplicate = errors.New("duplicate article")

// ErrLegacyImportStopped is returned (wrapped) by ImportAllSQLiteDatabases when its stop
// channel was closed during the start delay or the import: no new file is started, the
// files in progress stop after their current article.
var ErrLegacyImportStopped = errors.New("legacy import stopped")

// legacyErrSummaryMax is the number of failed files the import error names; the
// rest are only counted (every failure is logged as it happens).
const legacyErrSummaryMax = 5

// LegacyImportStartDelay is the pause between validating the spool files and starting the
// article import (time to cancel when only the newsgroups were wanted). Tests set it to 0.
var LegacyImportStartDelay = 15 * time.Second

// LegacyImporter handles importing RockSolid Light legacy configurations
type LegacyImporter struct {
	//db        *database.Database
	spoolPath    string
	etcPath      string
	proc         *Processor
	imported     uint64
	skipped      uint64 // articles that failed to import (scan or process error)
	skippedEmpty uint64 // empty articles
	duplicates   uint64 // articles that were already known
	groupsDone   uint64 // number of groups done
	skippedFiles uint64 // number of skipped files
	mux          sync.Mutex
	// stop is the cancellation of the running ImportAllSQLiteDatabases (nil: none);
	// set before the import goroutines start, read by importSQLiteArticlesWith.
	stop <-chan struct{}
}

// MenuConfEntry represents a line from menu.conf
type MenuConfEntry struct {
	Name             string
	ShowInHeader     bool
	EnableLocalSpool bool
}

// GroupsEntry represents a line from groups.txt
type GroupsEntry struct {
	Name        string
	Description string
	IsHeader    bool
}

// LegacyArticle represents an article from the legacy SQLite database
type LegacyArticle struct {
	ID        int    `json:"id"`
	Newsgroup string `json:"newsgroup"`
	Number    string `json:"number"`
	MsgID     string `json:"msgid"`
	Date      string `json:"date"`
	Name      string `json:"name"`
	Subject   string `json:"subject"`
	//SearchSnippet string `json:"search_snippet"`
	Article string `json:"article"` // wireformat! article incl. full headers, body and final DOT+CRLF pair!
}

// LegacyThread represents a thread from the legacy SQLite database
type LegacyThread struct {
	ID      int    `json:"id"`
	Headers string `json:"headers"`
}

// NewLegacyImporter creates a new legacy importer. It returns an error instead of exiting
// when the processor (hostname, history) cannot be initialized.
func NewLegacyImporter(db *database.Database, etcPath, spoolPath string, useShortHashLen int) (*LegacyImporter, error) {
	// we dont need a specific provider here, just an empty backend config
	// to have access to the processor functions
	proc, err := NewProcessorE(db, nntp.NewPool(&nntp.BackendConfig{}), useShortHashLen)
	if err != nil {
		return nil, fmt.Errorf("legacy importer: %w", err)
	}
	importer := &LegacyImporter{
		spoolPath: spoolPath,
		etcPath:   etcPath,
		proc:      proc,
	}

	return importer, nil
}

// Close properly shuts down the legacy importer.
// Processor.Close waits for pending batches and closes the history writer;
// it does not close the shared database (main does that after db.WG.Wait()).
func (leg *LegacyImporter) Close() error {
	if leg.proc == nil {
		return nil
	}
	err := leg.proc.Close()
	leg.proc = nil
	return err
}

// ImportSections imports all sections from the legacy RockSolid Light installation
func (leg *LegacyImporter) ImportSections() error {
	log.Println("internal/legacy/main.go: Starting legacy section import...")

	// Parse menu.conf
	menuEntries, err := leg.parseMenuConf()
	if err != nil {
		return fmt.Errorf("internal/legacy/main.go: failed to parse menu.conf: %w", err)
	}

	log.Printf("internal/legacy/main.go: Found %d sections in menu.conf", len(menuEntries))

	// Import each section
	for i, entry := range menuEntries {
		if entry.Name == "spoolnews" {
			// Skip the internal spoolnews handler
			continue
		}

		log.Printf("Importing section: %s", entry.Name)

		// Create section record
		section := &models.Section{
			Name:             entry.Name,
			DisplayName:      entry.Name,
			Description:      entry.Name,
			ShowInHeader:     entry.ShowInHeader,
			EnableLocalSpool: entry.EnableLocalSpool,
			SortOrder:        i,
		}

		sectionID, err := leg.insertSection(section)
		if err != nil {
			log.Printf("internal/legacy/main.go: Warning: failed to insert section %s: %v", entry.Name, err)
			continue
		}

		// Import groups for this section
		err = leg.importSectionGroups(sectionID, entry.Name)
		if err != nil {
			log.Printf("internal/legacy/main.go: Warning: failed to import groups for section %s: %v", entry.Name, err)
			continue
		}
	}

	log.Println("internal/legacy/main.go: Legacy section import completed!")
	return nil
}

// parseMenuConf reads and parses the menu.conf file
func (leg *LegacyImporter) parseMenuConf() ([]MenuConfEntry, error) {
	menuPath := filepath.Join(leg.etcPath, "menu.conf")
	file, err := os.Open(menuPath)
	if err != nil {
		return nil, fmt.Errorf("internal/legacy/main.go: failed to open menu.conf: %w", err)
	}
	defer file.Close()

	var entries []MenuConfEntry
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse line: name:show_in_header:enable_local_spool
		parts := strings.Split(line, ":")
		if len(parts) != 3 {
			log.Printf("internal/legacy/main.go: Warning: invalid menu.conf line: %s", line)
			continue
		}

		showInHeader, err := strconv.ParseBool(parts[1])
		if err != nil {
			log.Printf("internal/legacy/main.go: Warning: invalid show_in_header value: %s", parts[1])
			showInHeader = false
		}

		enableLocalSpool, err := strconv.ParseBool(parts[2])
		if err != nil {
			log.Printf("internal/legacy/main.go: Warning: invalid enable_local_spool value: %s", parts[2])
			enableLocalSpool = false
		}

		entries = append(entries, MenuConfEntry{
			Name:             parts[0],
			ShowInHeader:     showInHeader,
			EnableLocalSpool: enableLocalSpool,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("internal/legacy/main.go: error reading menu.conf: %w", err)
	}

	return entries, nil
}

// importSectionGroups imports groups.txt for a specific section
func (leg *LegacyImporter) importSectionGroups(sectionID int, sectionName string) error {
	groupsPath := filepath.Join(leg.etcPath, sectionName, "groups.txt")

	// Check if groups.txt exists
	if _, err := os.Stat(groupsPath); os.IsNotExist(err) {
		log.Printf("internal/legacy/main.go: No groups.txt found for section %s, skipping", sectionName)
		return nil
	}

	file, err := os.Open(groupsPath)
	if err != nil {
		return fmt.Errorf("internal/legacy/main.go: failed to open groups.txt for section %s: %w", sectionName, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	sortOrder := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines
		if line == "" {
			continue
		}

		var entry GroupsEntry

		// Check if it's a category header (starts with ':')
		if strings.HasPrefix(line, ":") {
			entry = GroupsEntry{
				Name:        line,
				Description: strings.TrimPrefix(line, ":"),
				IsHeader:    true,
			}
		} else {
			// Parse newsgroup line: "group.name Description text"
			parts := strings.SplitN(line, " ", 2)
			if parts[0] == "" {
				log.Printf("internal/legacy/main.go: Warning: groups.txt empty name in line: %s", line)
				continue
			}
			entry = GroupsEntry{
				Name:        parts[0],
				Description: "",
				IsHeader:    false,
			}
			if len(parts) > 1 {
				entry.Description = parts[1]
			}
		}

		// Insert section group record
		sectionGroup := &models.SectionGroup{
			SectionID:        sectionID,
			NewsgroupName:    entry.Name,
			GroupDescription: entry.Description,
			SortOrder:        sortOrder,
			IsCategoryHeader: entry.IsHeader,
		}

		err = leg.insertSectionGroup(sectionGroup)
		if err != nil {
			log.Printf("internal/legacy/main.go: Warning: failed to insert section group %s: %v", entry.Name, err)
		}

		// If it's not a category header, also create the newsgroup in the main newsgroups table
		if !entry.IsHeader {
			err = leg.insertNewsgroupIfNotExists(entry.Name, entry.Description)
			if err != nil {
				log.Printf("internal/legacy/main.go: Warning: failed to insert newsgroup %s: %v", entry.Name, err)
			}
		}

		sortOrder++
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("internal/legacy/main.go: error reading groups.txt for section %s: %w", sectionName, err)
	}

	log.Printf("internal/legacy/main.go: Imported %d groups for section %s", sortOrder, sectionName)
	return nil
}

// insertSection inserts a section record and returns its ID
func (leg *LegacyImporter) insertSection(section *models.Section) (int, error) {
	// UpsertSectionID reads the id back by name: after an INSERT OR IGNORE that ignored
	// an existing section, LastInsertId would still hold the id of an earlier statement.
	id, err := leg.proc.DB.UpsertSectionID(section)
	if err != nil {
		return 0, err
	}

	return int(id), nil
}

// insertSectionGroup inserts a section group record
func (leg *LegacyImporter) insertSectionGroup(sectionGroup *models.SectionGroup) error {
	_, err := database.RetryableExec(leg.proc.DB.GetMainDB(),
		`INSERT OR IGNORE INTO section_groups (section_id, newsgroup_name, group_description, sort_order, is_category_header)
		 VALUES (?, ?, ?, ?, ?)`,
		sectionGroup.SectionID, sectionGroup.NewsgroupName, sectionGroup.GroupDescription,
		sectionGroup.SortOrder, sectionGroup.IsCategoryHeader,
	)
	return err
}

// parseArticleTextToSlice splits a wireformat article (CRLF or LF line ends) into lines.
// The final "." terminator line is removed and dot-stuffed lines ("..x") are unstuffed.
// The returned size counts one byte per line end (log only).
func parseArticleTextToSlice(articleText string) ([]string, int) {
	// Accept CRLF and LF: a "\r" left on the blank separator line would hide the body
	text := strings.ReplaceAll(articleText, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	size := 0
	// Remove the terminator (a body line "." is stuffed to "..", so a final "." is never content).
	// If the last non-empty line is ".", it and every empty element after it (line ends stored
	// after the terminator) are dropped. Without a terminator only the one empty element left
	// by the final line end is dropped, so a real trailing blank body line is kept.
	last := len(lines) - 1
	for last >= 0 && lines[last] == "" {
		last--
	}
	if last >= 0 && lines[last] == "." {
		lines = lines[:last]
	} else if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// Undo dot-stuffing only after the terminator is gone
	for i, line := range lines {
		if strings.HasPrefix(line, "..") {
			lines[i] = line[1:]
		}
	}
	for _, line := range lines {
		size += len(line) + 1 // +1 for the newline character
	}
	return lines, size
}

// importLegacyArticle converts and imports a single legacy article
func (leg *LegacyImporter) importLegacyArticle(legacyArticle *LegacyArticle) error {
	if legacyArticle == nil {
		return fmt.Errorf("legacy article is nil")
	}
	if legacyArticle.Article == "\n" || legacyArticle.Article == "\r\n" || strings.TrimSpace(legacyArticle.Article) == "" {
		return error_empty_article
	}
	// legacyArticle.Article includes headers, body and final DOT+CRLF pair
	lines, size := parseArticleTextToSlice(legacyArticle.Article)
	art, err := nntp.ParseLegacyArticleLines(legacyArticle.MsgID, lines, true) // split into headers and body
	if err != nil {
		return fmt.Errorf("failed to parse article '%s' (%d bytes) to lines %d size %d: %w", legacyArticle.MsgID, len(legacyArticle.Article), len(lines), size, err)
	}
	legacyArticle.Article = ""

	//log.Printf("importLegacyArticle: Parsed article '%s' (%d bytes) to %d lines, size %d (ng: '%s') ==> processArticle", legacyArticle.MsgID, len(legacyArticle.Article), len(lines), size, legacyArticle.Newsgroup)
	//log.Printf("legacyArticle.Newsgroup: '%s' %s", legacyArticle.MsgID, legacyArticle.Newsgroup)
	bulkmode := true // we are importing legacy articles in bulkmode

	// Time the processArticle call
	//processStart := time.Now()
	response, err := leg.proc.processArticle(art, legacyArticle.Newsgroup, bulkmode)
	//processElapsed := time.Since(processStart)
	if err != nil {
		return fmt.Errorf("failed to process article '%s': %w", legacyArticle.MsgID, err)
	}
	switch response {
	case history.CasePass:
	case history.CaseDupes:
		return fmt.Errorf("article '%s': %w", legacyArticle.MsgID, errLegacyDuplicate)
	default:
		return fmt.Errorf("failed to process article '%s': response 0x%X", legacyArticle.MsgID, response)
	}

	// Log slow articles (taking more than 100ms)
	/*
		if processElapsed > 100*time.Millisecond {
			log.Printf("[RSLIGHT-IMPORT] SLOW: article '%s' took %v to process", legacyArticle.MsgID, processElapsed)
		}
	*/
	/*
		if legacyArticle.MsgID == "<6682ED72.1369.dove-ads@bbs.virtualoak.net>" {
			fmt.Printf("BREAKPOINT in internal/processor/rslight.go:~L390\n")
			fmt.Printf("{art=%#v}\n", art)
			fmt.Printf("\n{leg=%#v}\n", legacyArticle)
			os.Exit(1)
		}
	*/
	return nil
}

// ImportAllSQLiteDatabases imports from all SQLite databases in the legacy directory.
// Closing stop (nil: never) during the start delay or the import ends it early with an
// error that wraps ErrLegacyImportStopped.
func (leg *LegacyImporter) ImportAllSQLiteDatabases(sqliteDir string, threads int, stop <-chan struct{}) error {
	log.Printf("[RSLIGHT-IMPORT] Starting from dir: '%s'", sqliteDir)
	leg.stop = stop

	// Find all .db3 files
	files, err := filepath.Glob(filepath.Join(sqliteDir, "*-articles.db3"))
	if err != nil {
		return fmt.Errorf("failed to find SQLite files: %w", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no SQLite database files found in %s", sqliteDir)
	}

	log.Printf("[RSLIGHT-IMPORT] Found %d SQLite database files", len(files))
	skipList := []string{}
	workFiles := []string{}
	for _, file := range files {
		filename := filepath.Base(file)

		// Validate filename to prevent SQL injection attacks
		if !isValidDatabaseFilename(filename) {
			log.Printf("[RSLIGHT-IMPORT] SECURITY WARNING: Skipping potentially malicious filename: %s", filename)
			leg.skippedFiles++
			skipList = append(skipList, filename)
			continue
		}
		if !strings.Contains(filename, "articles") {
			log.Printf("[RSLIGHT-IMPORT] Skipping unknown SQLite file: %s", file)
			leg.mux.Lock()
			leg.skippedFiles++
			leg.mux.Unlock()
			skipList = append(skipList, file)
			continue
		}
		newsgroup := strings.TrimSuffix(filepath.Base(filename), "-articles.db3")

		// Skip empty newsgroup names that could result from malformed filenames
		if newsgroup == "" || strings.TrimSpace(newsgroup) == "" {
			log.Printf("[RSLIGHT-IMPORT] Skipping file with empty/blank newsgroup name: %s", filename)
			leg.mux.Lock()
			leg.skippedFiles++
			leg.mux.Unlock()
			skipList = append(skipList, file)
			continue
		}

		anewsgroup, err := leg.QuickOpenToGetNewsgroup(file)
		if err != nil {
			log.Printf("[RSLIGHT-IMPORT] Warning: failed to get newsgroup from %s: %v", file, err)
			leg.skippedFiles++
			skipList = append(skipList, file)
			continue
		}
		if newsgroup != anewsgroup {
			log.Printf("[RSLIGHT-IMPORT] Info: newsgroup '%s' from file '%s' does not match expected newsgroup '%s'", anewsgroup, file, newsgroup)
		}
		if groupDB, err := leg.proc.DB.GetGroupDB(newsgroup); err != nil {
			log.Printf("[RSLIGHT-IMPORT] Warning: failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
			// Don't close on error, this is a different type of error
		} else {
			// Return the groupDB connection immediately - we were just testing if the group exists
			groupDB.Return()
			err = leg.insertNewsgroupIfNotExists(newsgroup, "") // Insert newsgroup if it doesn't exist
			if err != nil {
				log.Printf("internal/legacy/main.go: Warning: failed to insert newsgroup %s: %v", newsgroup, err)
			}
		}
		// Use the newsgroup for further processing
		workFiles = append(workFiles, file)
	}
	log.Printf("[RSLIGHT-IMPORT] Validated %d SQLite database files for import", len(workFiles))
	if len(workFiles) == 0 {
		for _, skip := range skipList {
			log.Printf("[RSLIGHT-IMPORT] Skipped file: %s", skip)
		}
		return fmt.Errorf("all %d SQLite database files in %s were skipped", len(files), sqliteDir)
	}
	if err := waitLegacyStartDelay(LegacyImportStartDelay, stop); err != nil {
		return err
	}
	log.Printf("[RSLIGHT-IMPORT] Starting import of %d SQLite database files with %d threads", len(workFiles), threads)
	failed, importErr := importLegacyFiles(workFiles, threads, stop, leg.ImportSQLiteArticles)
	log.Printf("[RSLIGHT-IMPORT] internal wg.Wait() released, %d of %d files failed", failed, len(workFiles))
	leg.mux.Lock()
	defer leg.mux.Unlock()
	for _, skip := range skipList {
		log.Printf("[RSLIGHT-IMPORT] Skipped file: %s", skip)
	}
	thread_counters := leg.proc.ThreadCounter.GetResetAll() // Reset thread counter after import
	log.Printf("[RSLIGHT-IMPORT] Group Thread counters: %v", thread_counters)
	legacy_counter := leg.proc.LegacyCounter.GetResetAll() // Reset legacy counter after import
	log.Printf("[RSLIGHT-IMPORT] Legacy counters: %v", legacy_counter)
	log.Printf("[RSLIGHT-IMPORT] Total Imported: %d articles, Failed: %d articles, Empty: %d, Duplicates: %d, GroupsDone: %d, FailedFiles: %d, SkippedFiles: %d  / %d",
		leg.imported, leg.skipped, leg.skippedEmpty, leg.duplicates, leg.groupsDone, failed, leg.skippedFiles, len(files))
	if importErr != nil {
		if failed == 0 { // only stopped
			return importErr
		}
		return fmt.Errorf("%d of %d SQLite database files failed: %w", failed, len(workFiles), importErr)
	}
	return nil
}

// waitLegacyStartDelay waits delay (≤ 0: not at all) before the article import; a
// closed stop ends the wait with an error that wraps ErrLegacyImportStopped.
func waitLegacyStartDelay(delay time.Duration, stop <-chan struct{}) error {
	if delay <= 0 {
		return nil
	}
	log.Printf("[RSLIGHT-IMPORT] Sleeping for %v before import. You can cancel here with Ctrl+C if you only wanted to get the newsgroups added.", delay)
	log.Printf("[RSLIGHT-IMPORT] Import of legacy articles starts in %v!", delay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stop:
		log.Printf("[RSLIGHT-IMPORT] Stopped during the start delay: no article was imported")
		return fmt.Errorf("before the article import: %w", ErrLegacyImportStopped)
	case <-timer.C:
		return nil
	}
}

// importLegacyFiles runs importFn for every file, at most threads files at a time.
// Every worker keeps its own error; the number of failed files is returned with an
// error that names the first legacyErrSummaryMax of them in file order
// (summarizeLegacyErrs). Once stop is closed no new file is started; the error then
// also wraps ErrLegacyImportStopped (a file that returned it is not counted as failed).
func importLegacyFiles(files []string, threads int, stop <-chan struct{}, importFn func(file string) error) (int, error) {
	if threads < 1 {
		threads = 1
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		errs    = make([]error, len(files)) // by file index: the summary keeps file order
		stopped atomic.Bool
	)
	slots := make(chan struct{}, threads) // limits concurrency
launch:
	for i, file := range files {
		select {
		case <-stop:
			stopped.Store(true)
			break launch
		case slots <- struct{}{}: // get slot
		}
		// both cases of the select above can be ready: check stop again with the slot
		// held, so no file starts after the stop was seen
		select {
		case <-stop:
			<-slots
			stopped.Store(true)
			break launch
		default:
		}
		wg.Add(1)
		// for every database file we want to import run a dedicated go routine
		go func(i int, file string) {
			defer func() {
				<-slots // return slot
				wg.Done()
			}()
			err := importFn(file)
			switch {
			case err == nil:
			case errors.Is(err, ErrLegacyImportStopped):
				stopped.Store(true)
			default:
				log.Printf("[RSLIGHT-IMPORT] Warning: rslight-import failed file '%s': %v", file, err)
				mu.Lock()
				errs[i] = fmt.Errorf("file '%s': %w", file, err)
				mu.Unlock()
			}
		}(i, file)
	}
	log.Printf("[RSLIGHT-IMPORT] Importing now! wait on internal wg.Wait()")
	wg.Wait()
	var failed []error
	for _, err := range errs {
		if err != nil {
			failed = append(failed, err)
		}
	}
	err := summarizeLegacyErrs(failed)
	if stopped.Load() {
		log.Printf("[RSLIGHT-IMPORT] Import stopped: no further file was started")
		err = errors.Join(ErrLegacyImportStopped, err)
	}
	return len(failed), err
}

// summarizeLegacyErrs joins at most legacyErrSummaryMax errors and adds a line with
// the number of the others (nil for none).
func summarizeLegacyErrs(errs []error) error {
	if len(errs) <= legacyErrSummaryMax {
		return errors.Join(errs...)
	}
	shown := make([]error, 0, legacyErrSummaryMax+1)
	shown = append(shown, errs[:legacyErrSummaryMax]...)
	shown = append(shown, fmt.Errorf("... and %d more failed files (logged above)", len(errs)-legacyErrSummaryMax))
	return errors.Join(shown...)
}

// GetSectionsSummary returns a summary of imported sections
func (leg *LegacyImporter) GetSectionsSummary() error {
	rows, err := database.RetryableQuery(leg.proc.DB.GetMainDB(), `
		SELECT s.name, s.display_name, s.show_in_header, COUNT(sg.id) as group_count
		FROM sections s
		LEFT JOIN section_groups sg ON s.id = sg.section_id
		GROUP BY s.id, s.name, s.display_name, s.show_in_header
		ORDER BY s.sort_order
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	fmt.Println("\n=== Imported Sections Summary ===")
	for rows.Next() {
		var name, displayName string
		var showInHeader bool
		var groupCount int

		if err := rows.Scan(&name, &displayName, &showInHeader, &groupCount); err != nil {
			return err
		}

		headerStatus := "No"
		if showInHeader {
			headerStatus = "Yes"
		}

		fmt.Printf("Section: %-12s | Display: %-15s | Header: %-3s | Groups: %d\n",
			name, displayName, headerStatus, groupCount)
	}

	// Also show the total count of newsgroups created in the main database
	newsgroupCount := leg.proc.DB.MainDBGetAllNewsgroupsCount()
	fmt.Printf("\n=== Main Database Summary ===\n")
	fmt.Printf("Total newsgroups in main database: %d\n", newsgroupCount)

	return nil
}

// validFilenameRegex validates database filenames to prevent injection attacks
// Only allows alphanumeric characters, dots, hyphens, and underscores
//var validFilenameRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]+\.db3$`)

// isValidDatabaseFilename validates that a filename is safe to process
// and follows expected patterns for database files
func isValidDatabaseFilename(filename string) bool {
	// Check against regex for basic character validation
	/*
		if !validFilenameRegex.MatchString(filename) {
			return false
		}
	*/

	// Additional checks for suspicious patterns
	suspicious := []string{
		" ", "(", ")", "'", "\"", ";", "--", "/*", "*/", "0x", "'", "%",
	}

	upperFilename := strings.ToUpper(filename)
	for _, pattern := range suspicious {
		if strings.Contains(upperFilename, strings.ToUpper(pattern)) {
			return false
		}
	}

	// Check for excessive length (potential buffer overflow)
	if len(filename) > 255 {
		return false
	}

	return true
}

// insertNewsgroupIfNotExists inserts a newsgroup into the main newsgroups table if it doesn't already exist
func (leg *LegacyImporter) insertNewsgroupIfNotExists(name, description string) error {
	if name == "" {
		return fmt.Errorf("error in insertNewsgroupIfNotExists: newsgroup name cannot be empty.. supplied description: '%s'", description)
	}
	// Check if newsgroup already exists
	_, err := leg.proc.DB.MainDBGetNewsgroup(name)
	if err == nil {
		// Newsgroup already exists, skip insertion
		return nil
	}

	// Create newsgroup with default values
	newsgroup := &models.Newsgroup{
		Name:         name,
		Description:  description,
		Active:       true,
		LastArticle:  0,
		MessageCount: 0,
		ExpiryDays:   0, // No expiry by default
		MaxArticles:  0, // No limit by default
		HighWater:    0,
		LowWater:     1,
		Status:       "y", // Allow posting
		CreatedAt:    time.Now(),
		// Note: UpdatedAt will be set only when articles are processed via batch
	}

	return leg.proc.DB.InsertNewsgroup(newsgroup)
}
