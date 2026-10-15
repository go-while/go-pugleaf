package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3"
)

const (
	// Database settings optimized for bulk imports
	PRAGMA_SETTINGS = `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = OFF;
		PRAGMA cache_size = -64000;
		PRAGMA temp_store = memory;
		PRAGMA mmap_size = 67108864;
	`

	// Batch size for transactions (balance between memory and performance)
	BATCH_SIZE = 10000

	// Worker count for parallel processing
	DEFAULT_WORKERS = 32
)

var (
	headPath = flag.String("head", "/mnt/xfshead", "Path to head files directory")
	bodyPath = flag.String("body", "/mnt/xfsbody", "Path to body files directory")
	dbPath   = flag.String("db", "./imported_articles", "Path for SQLite database files")
	workers  = flag.Int("workers", DEFAULT_WORKERS, "Number of worker goroutines")
	//resume   = flag.Bool("resume", false, "Resume from where we left off")
	dryRun  = flag.Bool("dry-run", false, "Don't actually write to database, just scan files")
	verbose = flag.Bool("verbose", false, "Verbose logging")
	update  = flag.Bool("update", false, "Update mode: only import missing articles")

	// Global counters
	totalProcessed int64
	totalErrors    int64
	totalSkipped   int64
	totalExisting  int64
	startTime      time.Time
)

// Article represents a single usenet article
type Article struct {
	MessageIDHash string
	Head          string
	Body          string
	HeadFile      string
	BodyFile      string
	Dir1          string // first directory level (for database routing)
	Dir2          string // second directory level (for database routing)
	Dir3          string // third directory level (for table routing)
}

// DBManager handles database connections and operations
type DBManager struct {
	dbConnections map[string]*sql.DB
	mutex         sync.RWMutex
	// createdTables holds "<dbName>/<tableName>" keys of tables known to exist.
	// Tables are created on first use instead of all 65,536 up front.
	createdTables sync.Map
}

func NewDBManager() *DBManager {
	return &DBManager{
		dbConnections: make(map[string]*sql.DB),
	}
}

// ensureTable creates the article table on first use (CREATE TABLE IF NOT EXISTS).
// The primary key already indexes messageid_hash, so no extra index is created.
// It runs outside any transaction: callers create every table of a batch before Begin.
func (dm *DBManager) ensureTable(db *sql.DB, dbName, tableName string) error {
	key := dbName + "/" + tableName
	if _, ok := dm.createdTables.Load(key); ok {
		return nil
	}
	if !validTableName(tableName) {
		return fmt.Errorf("invalid table name %q", tableName)
	}
	createTableSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			messageid_hash CHAR(58) PRIMARY KEY,
			head TEXT NOT NULL,
			body TEXT NOT NULL
		)`, tableName)
	if _, err := database.RetryableExec(db, createTableSQL); err != nil {
		return fmt.Errorf("failed to create table %s: %w", tableName, err)
	}
	dm.createdTables.Store(key, struct{}{})
	return nil
}

func (dm *DBManager) GetDB(dbName string) (*sql.DB, error) {
	dm.mutex.RLock()
	if db, exists := dm.dbConnections[dbName]; exists {
		dm.mutex.RUnlock()
		return db, nil
	}
	dm.mutex.RUnlock()

	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	// Double-check after acquiring write lock
	if db, exists := dm.dbConnections[dbName]; exists {
		return db, nil
	}

	dbFile := filepath.Join(*dbPath, dbName+".db")

	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(dbFile), 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open database %s: %w", dbFile, err)
	}

	// Configure database for performance
	if _, err := database.RetryableExec(db, PRAGMA_SETTINGS); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set PRAGMA settings: %w", err)
	}

	// Tables (articles_0000-ffff) are created on first use by ensureTable.

	dm.dbConnections[dbName] = db
	log.Printf("Opened database: %s", dbFile)
	return db, nil
}

// getDBName returns the database name from directory structure
// Uses dir1 + dir2 to determine database (00-ff)
func getDBName(article *Article) string {
	return article.Dir1 + article.Dir2
}

// getTableName returns the table name for a given article
// Uses dir3 + first 3 chars of hash to determine table (0000-ffff)
// The result is lower-cased (SQLite table names are case-insensitive anyway).
func getTableName(article *Article) string {
	if len(article.MessageIDHash) < 3 {
		return "articles_" + strings.ToLower(article.Dir3) + "000" // fallback
	}
	return strings.ToLower(fmt.Sprintf("articles_%s%s", article.Dir3, article.MessageIDHash[0:3]))
}

// minHashLen is the shortest accepted file name hash (md5 hex or longer).
const minHashLen = 32

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return len(s) > 0
}

// validTableName reports whether name is "articles_" followed by exactly 4 lower-case hex digits.
func validTableName(name string) bool {
	rest, ok := strings.CutPrefix(name, "articles_")
	return ok && len(rest) == 4 && isHexString(rest) && rest == strings.ToLower(rest)
}

// validateArticle checks the routing parts before an article is read or batched:
// Dir1-Dir3 must be one hex digit each, and the hash must be at least minHashLen
// characters long and start with 3 hex digits (the table routing part). A file that
// fails this is skipped on its own and never reaches a batch.
func validateArticle(article *Article) error {
	if len(article.MessageIDHash) < minHashLen { // should be at least md5 or longer
		return fmt.Errorf("invalid hash length %d (< %d)", len(article.MessageIDHash), minHashLen)
	}
	if !isHexString(article.MessageIDHash[:3]) {
		return fmt.Errorf("hash does not start with 3 hex digits")
	}
	for _, d := range []string{article.Dir1, article.Dir2, article.Dir3} {
		if len(d) != 1 || !isHexString(d) {
			return fmt.Errorf("invalid directory component %q (want one hex digit)", d)
		}
	}
	return nil
}

// getStoredHash returns the hash to store in database (without routing chars)
// Removes the first 3 characters used for table routing
func getStoredHash(article *Article) string {
	if len(article.MessageIDHash) < 3 {
		return article.MessageIDHash
	}
	return article.MessageIDHash[3:] // remove first 3 chars used for table routing
}

// ArticleExists checks if an article already exists in the database
func (dm *DBManager) ArticleExists(article *Article) (bool, error) {
	dbName := getDBName(article)
	db, err := dm.GetDB(dbName)
	if err != nil {
		return false, err
	}

	tableName := getTableName(article)
	if err := dm.ensureTable(db, dbName, tableName); err != nil {
		return false, err
	}
	storedHash := getStoredHash(article)
	var exists bool
	query := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE messageid_hash = ? LIMIT 1)", tableName)
	err = database.RetryableQueryRowScan(db, query, []interface{}{storedHash}, &exists)
	if err != nil {
		return false, err
	}

	return exists, nil
}

func (dm *DBManager) Close() {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	for dbName, db := range dm.dbConnections {
		if err := db.Close(); err != nil {
			log.Printf("Error closing database %s: %v", dbName, err)
		}
	}
}

// FileScanner scans the file system for article files
type FileScanner struct {
	headBasePath string
	bodyBasePath string
}

func NewFileScanner(headPath, bodyPath string) *FileScanner {
	return &FileScanner{
		headBasePath: headPath,
		bodyBasePath: bodyPath,
	}
}

// ScanFiles returns a channel of Article structs representing file pairs
func (fs *FileScanner) ScanFiles() <-chan *Article {
	articles := make(chan *Article, 1000)

	go func() {
		defer close(articles)

		// Walk through the head directory structure: /0-f/0-f/0-f/
		for dir1 := 0; dir1 < 16; dir1++ {
			for dir2 := 0; dir2 < 16; dir2++ {
				for dir3 := 0; dir3 < 16; dir3++ {
					headDir := filepath.Join(fs.headBasePath,
						fmt.Sprintf("%x", dir1),
						fmt.Sprintf("%x", dir2),
						fmt.Sprintf("%x", dir3))

					bodyDir := filepath.Join(fs.bodyBasePath,
						fmt.Sprintf("%x", dir1),
						fmt.Sprintf("%x", dir2),
						fmt.Sprintf("%x", dir3))

					if err := fs.scanDirectory(headDir, bodyDir, articles); err != nil {
						log.Printf("Error scanning directory %s: %v", headDir, err)
						atomic.AddInt64(&totalErrors, 1)
					}
				}
			}
		}
	}()

	return articles
}

func (fs *FileScanner) scanDirectory(headDir, bodyDir string, articles chan<- *Article) error {
	// Check if directories exist
	if _, err := os.Stat(headDir); os.IsNotExist(err) {
		if *verbose {
			log.Printf("Head directory doesn't exist: %s", headDir)
		}
		return nil
	}
	if _, err := os.Stat(bodyDir); os.IsNotExist(err) {
		if *verbose {
			log.Printf("Body directory doesn't exist: %s", bodyDir)
		}
		return nil
	}

	// Read head files
	headFiles, err := os.ReadDir(headDir)
	if err != nil {
		return fmt.Errorf("failed to read head directory %s: %w", headDir, err)
	}

	for _, headFile := range headFiles {
		if headFile.IsDir() || !strings.HasSuffix(headFile.Name(), ".head") {
			continue
		}

		// Extract hash from filename (remove .head extension)
		hash := strings.TrimSuffix(headFile.Name(), ".head")
		bodyFileName := hash + ".body"

		headFilePath := filepath.Join(headDir, headFile.Name())
		bodyFilePath := filepath.Join(bodyDir, bodyFileName)

		// Check if corresponding body file exists
		if _, err := os.Stat(bodyFilePath); os.IsNotExist(err) {
			if *verbose {
				log.Printf("Missing body file for %s: %s", headFilePath, bodyFilePath)
			}
			atomic.AddInt64(&totalSkipped, 1)
			continue
		}

		// Extract directory path components for database/table routing
		// headDir format: /path/to/base/[dir1]/[dir2]/[dir3]
		pathParts := strings.Split(headDir, string(filepath.Separator))
		if len(pathParts) < 3 {
			if *verbose {
				log.Printf("Invalid directory structure: %s", headDir)
			}
			atomic.AddInt64(&totalSkipped, 1)
			continue
		}

		// Get the last 3 directory components
		dir1 := pathParts[len(pathParts)-3] // first dir level
		dir2 := pathParts[len(pathParts)-2] // second dir level
		dir3 := pathParts[len(pathParts)-1] // third dir level

		article := &Article{
			MessageIDHash: hash,
			HeadFile:      headFilePath,
			BodyFile:      bodyFilePath,
			Dir1:          dir1,
			Dir2:          dir2,
			Dir3:          dir3,
		}

		articles <- article
	}

	return nil
}

// Worker processes articles from the channel
func worker(id int, articles <-chan *Article, dbManager *DBManager, wg *sync.WaitGroup) {
	defer wg.Done()

	batch := make([]*Article, 0, BATCH_SIZE)
	var currentDB *sql.DB
	var currentDBName string

	// processBatch writes the batch to currentDB and always empties it. It updates the
	// counters itself: a row that fails (or a table whose statement cannot be prepared)
	// only counts that row/table as errors; the rest of the batch is still committed.
	// A failed Begin or Commit counts the whole batch as errors.
	processBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		defer func() { batch = batch[:0] }()

		if *dryRun {
			atomic.AddInt64(&totalProcessed, int64(len(batch)))
			return nil
		}

		// Group articles by table for more efficient batching
		articlesByTable := make(map[string][]*Article)
		for _, article := range batch {
			tableName := getTableName(article)
			articlesByTable[tableName] = append(articlesByTable[tableName], article)
		}

		var inserted, duplicates, rowErrors int64

		// Create missing tables before the transaction: a CREATE on the pool while
		// this transaction holds the write lock would wait for itself.
		for tableName, tableArticles := range articlesByTable {
			if err := dbManager.ensureTable(currentDB, currentDBName, tableName); err != nil {
				log.Printf("Worker %d: %v (skipping %d articles)", id, err, len(tableArticles))
				rowErrors += int64(len(tableArticles))
				delete(articlesByTable, tableName)
			}
		}

		tx, err := currentDB.Begin()
		if err != nil {
			atomic.AddInt64(&totalErrors, int64(len(batch)))
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

		// Process each table separately
		for tableName, tableArticles := range articlesByTable {
			query := fmt.Sprintf("INSERT OR IGNORE INTO %s (messageid_hash, head, body) VALUES (?, ?, ?)", tableName)
			stmt, err := tx.Prepare(query)
			if err != nil {
				log.Printf("Worker %d: failed to prepare statement for table %s: %v (skipping %d articles)", id, tableName, err, len(tableArticles))
				rowErrors += int64(len(tableArticles))
				continue
			}

			for _, article := range tableArticles {
				storedHash := getStoredHash(article)
				res, err := stmt.Exec(storedHash, article.Head, article.Body)
				if err != nil {
					log.Printf("Worker %d: Error inserting article %s into table %s: %v", id, storedHash, tableName, err)
					rowErrors++
					continue
				}
				if n, err := res.RowsAffected(); err == nil && n == 0 {
					duplicates++ // already stored (INSERT OR IGNORE)
					continue
				}
				inserted++
			}
			if err := stmt.Close(); err != nil {
				log.Printf("Worker %d: failed to close statement for table %s: %v", id, tableName, err)
			}
		}

		if err := tx.Commit(); err != nil {
			atomic.AddInt64(&totalErrors, int64(len(batch)))
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		atomic.AddInt64(&totalProcessed, inserted)
		atomic.AddInt64(&totalExisting, duplicates)
		atomic.AddInt64(&totalErrors, rowErrors)
		return nil
	}

	for article := range articles {
		// Validate the routing parts first: a bad file name is skipped on its own and
		// must never reach (and fail) a batch.
		if err := validateArticle(article); err != nil {
			log.Printf("Worker %d: skipping %s: %v", id, article.HeadFile, err)
			atomic.AddInt64(&totalSkipped, 1)
			continue
		}

		dbName := getDBName(article)

		// In update mode, check if article already exists
		if *update {
			exists, err := dbManager.ArticleExists(article)
			if err != nil {
				log.Printf("Worker %d: Error checking if article exists %s: %v", id, article.MessageIDHash, err)
				atomic.AddInt64(&totalErrors, 1)
				continue
			}
			if exists {
				atomic.AddInt64(&totalExisting, 1)
				if *verbose {
					log.Printf("Worker %d: Article already exists, skipping: %s", id, article.MessageIDHash)
				}
				continue
			}
		}

		// Read head file
		headContent, err := readFile(article.HeadFile)
		if err != nil {
			log.Printf("Worker %d: Error reading head file %s: %v", id, article.HeadFile, err)
			atomic.AddInt64(&totalErrors, 1)
			continue
		}

		// Read body file
		bodyContent, err := readFile(article.BodyFile)
		if err != nil {
			log.Printf("Worker %d: Error reading body file %s: %v", id, article.BodyFile, err)
			atomic.AddInt64(&totalErrors, 1)
			continue
		}

		article.Head = headContent
		article.Body = bodyContent

		// Switch database if needed
		if dbName != currentDBName {
			// Process any pending batch for the previous database
			if err := processBatch(); err != nil { // counts its own errors and empties the batch
				log.Printf("Worker %d: Error processing batch: %v", id, err)
			}

			// Get new database connection
			currentDB, err = dbManager.GetDB(dbName)
			if err != nil {
				log.Printf("Worker %d: Error getting database %s: %v", id, dbName, err)
				atomic.AddInt64(&totalErrors, 1)
				continue
			}
			currentDBName = dbName
		}

		// Add to batch
		batch = append(batch, article)

		// Process batch if full
		if len(batch) >= BATCH_SIZE {
			if err := processBatch(); err != nil { // counts its own errors and empties the batch
				log.Printf("Worker %d: Error processing batch: %v", id, err)
			}
		}
	}

	// Process final batch
	if err := processBatch(); err != nil { // counts its own errors
		log.Printf("Worker %d: Error processing final batch: %v", id, err)
	}

	log.Printf("Worker %d finished", id)
}

// readFile returns the file content verbatim. It does not use a line scanner, so
// lines longer than bufio's 64 KiB default no longer fail the article, and line
// endings are stored as they are in the file.
func readFile(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Stats reporter
func statsReporter() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		<-ticker.C
		processed := atomic.LoadInt64(&totalProcessed)
		errors := atomic.LoadInt64(&totalErrors)
		skipped := atomic.LoadInt64(&totalSkipped)
		existing := atomic.LoadInt64(&totalExisting)
		elapsed := time.Since(startTime)

		rate := float64(processed) / elapsed.Seconds()

		if *update {
			log.Printf("Progress: Processed=%d, Errors=%d, Skipped=%d, Existing=%d, Rate=%.1f/sec, Elapsed=%v",
				processed, errors, skipped, existing, rate, elapsed.Truncate(time.Second))
		} else {
			log.Printf("Progress: Processed=%d, Errors=%d, Skipped=%d, Rate=%.1f/sec, Elapsed=%v",
				processed, errors, skipped, rate, elapsed.Truncate(time.Second))
		}

		// Memory stats
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		log.Printf("Memory: Alloc=%d KB, Sys=%d KB, NumGC=%d",
			m.Alloc/1024, m.Sys/1024, m.NumGC)
	}
}

func main() {
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	log.Printf("Starting flat file import tool")
	log.Printf("Head path: %s", *headPath)
	log.Printf("Body path: %s", *bodyPath)
	log.Printf("DB path: %s", *dbPath)
	log.Printf("Workers: %d", *workers)
	log.Printf("Batch size: %d", BATCH_SIZE)
	log.Printf("Dry run: %t", *dryRun)
	log.Printf("Update mode: %t", *update)

	// Validate paths
	if _, err := os.Stat(*headPath); os.IsNotExist(err) {
		log.Fatalf("Head path does not exist: %s", *headPath)
	}
	if _, err := os.Stat(*bodyPath); os.IsNotExist(err) {
		log.Fatalf("Body path does not exist: %s", *bodyPath)
	}

	// Create output directory
	if !*dryRun {
		if err := os.MkdirAll(*dbPath, 0755); err != nil {
			log.Fatalf("Failed to create output directory: %v", err)
		}
	}

	startTime = time.Now()

	// Start stats reporter
	go statsReporter()

	// Create database manager
	dbManager := NewDBManager()
	defer dbManager.Close()

	// Create file scanner
	scanner := NewFileScanner(*headPath, *bodyPath)
	articles := scanner.ScanFiles()

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go worker(i, articles, dbManager, &wg)
	}

	// Wait for all workers to complete
	wg.Wait()

	elapsed := time.Since(startTime)
	processed := atomic.LoadInt64(&totalProcessed)
	errors := atomic.LoadInt64(&totalErrors)
	skipped := atomic.LoadInt64(&totalSkipped)
	existing := atomic.LoadInt64(&totalExisting)

	log.Printf("Import completed!")
	log.Printf("Total processed: %d", processed)
	log.Printf("Total errors: %d", errors)
	log.Printf("Total skipped: %d", skipped)
	if *update {
		log.Printf("Total existing (skipped in update mode): %d", existing)
	}
	log.Printf("Total time: %v", elapsed.Truncate(time.Second))
	if elapsed.Seconds() > 0 {
		log.Printf("Average rate: %.1f articles/sec", float64(processed)/elapsed.Seconds())
	}

	// Final memory stats
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	log.Printf("Final memory: Alloc=%d KB, Sys=%d KB", m.Alloc/1024, m.Sys/1024)
}
