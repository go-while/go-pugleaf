// Package processor in this file imports RockSolid Light legacy configurations
package processor

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	_ "github.com/mattn/go-sqlite3"
)

var error_empty_article = fmt.Errorf("empty article") // error for empty articles

// LegacyImporter handles importing RockSolid Light legacy configurations
type LegacyImporter struct {
	//db        *database.Database
	spoolPath    string
	etcPath      string
	proc         *Processor
	imported     uint64
	skipped      uint64
	groupsDone   uint64 // number of groups done
	skippedFiles uint64 // number of skipped files
	mux          sync.Mutex
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

// NewLegacyImporter creates a new legacy importer
func NewLegacyImporter(db *database.Database, etcPath, spoolPath string, useShortHashLen int) *LegacyImporter {
	//defaultConfig := config.DefaultProviders[0]
	// we dont need a specific provider here, just empty config pointer..
	//  to have access to the functions
	importer := &LegacyImporter{
		spoolPath: spoolPath,
		etcPath:   etcPath,
		proc:      NewProcessor(db, nntp.NewPool(&nntp.BackendConfig{}), useShortHashLen), // Create a new processor with an empty backend config
	}

	return importer
}

// Close properly shuts down the legacy importer
// Note: We don't close the processor as it may share database connections with the main application
func (leg *LegacyImporter) Close() error {
	// Don't close the processor as it shares database resources with the main application
	// The processor will be closed when the main application shuts down
	leg.proc = nil // Just clear the reference
	return nil
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
	result, err := leg.proc.DB.GetMainDB().Exec(
		`INSERT OR IGNORE INTO sections (name, display_name, description, show_in_header, enable_local_spool, sort_order)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		section.Name, section.DisplayName, section.Description,
		section.ShowInHeader, section.EnableLocalSpool, section.SortOrder,
	)
	if err != nil {
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	return int(id), nil
}

// insertSectionGroup inserts a section group record
func (leg *LegacyImporter) insertSectionGroup(sectionGroup *models.SectionGroup) error {
	_, err := leg.proc.DB.GetMainDB().Exec(
		`INSERT OR IGNORE INTO section_groups (section_id, newsgroup_name, group_description, sort_order, is_category_header)
		 VALUES (?, ?, ?, ?, ?)`,
		sectionGroup.SectionID, sectionGroup.NewsgroupName, sectionGroup.GroupDescription,
		sectionGroup.SortOrder, sectionGroup.IsCategoryHeader,
	)
	return err
}

func parseArticleTextToSlice(articleText string) ([]string, int) {
	// Split the article text into lines
	lines := strings.Split(articleText, "\n")
	size := 0
	// Remove the last line if it's just a dot (.)
	if len(lines) > 0 && lines[len(lines)-1] == "." {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 && lines[len(lines)-1] == "." {
		lines = lines[:len(lines)-1]
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
	art, err := nntp.ParseLegacyArticleLines(legacyArticle.MsgID, lines) // split into headers and body
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
	if err != nil || response != history.CasePass {
		return fmt.Errorf("failed to process article '%s': %w", legacyArticle.MsgID, err)
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

// ImportAllSQLiteDatabases imports from all SQLite databases in the legacy directory
func (leg *LegacyImporter) ImportAllSQLiteDatabases(sqliteDir string, threads int) error {
	log.Printf("[RSLIGHT-IMPORT] Starting from dir: '%s'", sqliteDir)

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
	wg := &sync.WaitGroup{}
	parChan := make(chan struct{}, threads) // Limit concurrency HARDCODED
	errChan := make(chan error, len(files)) // Channel to capture errors
	for i := 0; i < cap(parChan); i++ {
		parChan <- struct{}{}
	}
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
		if groupDBs, err := leg.proc.DB.GetGroupDBs(newsgroup); err != nil {
			log.Printf("[RSLIGHT-IMPORT] Warning: failed to get group DBs for newsgroup '%s': %v", newsgroup, err)
			// Don't close on error, this is a different type of error
		} else {
			// Return the groupDBs connection immediately - we were just testing if the group exists
			groupDBs.Return(leg.proc.DB)
			err = leg.insertNewsgroupIfNotExists(newsgroup, "") // Insert newsgroup if it doesn't exist
			if err != nil {
				log.Printf("internal/legacy/main.go: Warning: failed to insert newsgroup %s: %v", newsgroup, err)
			}
		}
		// Use the newsgroup for further processing
		workFiles = append(workFiles, file)
	}
	log.Printf("[RSLIGHT-IMPORT] Validated %d SQLite database files for import", len(workFiles))
	log.Printf("[RSLIGHT-IMPORT] Sleeping for 15s before import. You can cancel here with Ctrl+C if you only wanted to get the newsgroups added.")
	log.Printf("[RSLIGHT-IMPORT] Import of legacy articles starts in 15s!")
	time.Sleep(15 * time.Second)
	log.Printf("[RSLIGHT-IMPORT] Starting import of %d SQLite database files with %d threads", len(workFiles), threads)
	for _, file := range workFiles {
		//filename := filepath.Base(file)
		// Determine the type of database based on filename
		<-parChan // get slot
		wg.Add(1)
		// for every database file we want to import run a dedicated go routine
		go func(file string) {
			//log.Printf("[RSLIGHT-IMPORT] work: '%s'", filename)
			//start := time.Now()
			defer func() {
				parChan <- struct{}{} // return slot
				wg.Done()
			}()
			err = leg.ImportSQLiteArticles(file)
			if err != nil {
				errChan <- err
				log.Printf("Warning: rslight-import failed file '%s': %v", file, err)
				return
			}
			leg.mux.Lock()
			leg.groupsDone++
			leg.mux.Unlock()
			errChan <- nil // No error
			//log.Printf("[RSLIGHT-IMPORT] done file '%s' (took %v) len(parChan)=%d/%d", file, time.Since(start), len(parChan), cap(parChan))
		}(file)
	}
	log.Printf("[RSLIGHT-IMPORT] Importing now! wait on internal wg.Wait()")
	wg.Wait()
	log.Printf("[RSLIGHT-IMPORT] internal wg.Wait() released, checking errors")
	close(errChan)
	errs := 0
	for err := range errChan {
		if err != nil {
			log.Printf("Error (#%d) during import: %v", errs, err)
			errs++
		}
	}
	leg.mux.Lock()
	defer leg.mux.Unlock()
	for _, skip := range skipList {
		log.Printf("[RSLIGHT-IMPORT] Skipped file: %s", skip)
	}
	thread_counters := leg.proc.ThreadCounter.GetResetAll() // Reset thread counter after import
	log.Printf("[RSLIGHT-IMPORT] Group Thread counters: %v", thread_counters)
	legacy_counter := leg.proc.LegacyCounter.GetResetAll() // Reset legacy counter after import
	log.Printf("[RSLIGHT-IMPORT] Legacy counters: %v", legacy_counter)
	log.Printf("[RSLIGHT-IMPORT] Total Imported: %d articles, Skipped: %d articles, GroupsDone: %d, SkippedFiles: %d  / %d", leg.imported, leg.skipped, leg.groupsDone, leg.skippedFiles, len(files))
	return nil
}

// GetSectionsSummary returns a summary of imported sections
func (leg *LegacyImporter) GetSectionsSummary() error {
	rows, err := leg.proc.DB.GetMainDB().Query(`
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
	newsgroupCount := leg.proc.DB.MainDBGetNewsgroupsCount()
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
