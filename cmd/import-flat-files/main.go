package main

import (
	"bufio"
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
}

func NewDBManager() *DBManager {
	return &DBManager{
		dbConnections: make(map[string]*sql.DB),
	}
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

	// Create all 65536 tables (0000-ffff) if they don't exist
	for i := 0; i < 65536; i++ {
		tableName := fmt.Sprintf("articles_%04x", i)
		createTableSQL := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				messageid_hash CHAR(58) PRIMARY KEY,
				head TEXT NOT NULL,
				body TEXT NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_%s_hash ON %s(messageid_hash);
		`, tableName, tableName, tableName)

		if _, err := database.RetryableExec(db, createTableSQL); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to create table %s: %w", tableName, err)
		}
	}

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
func getTableName(article *Article) string {
	if len(article.MessageIDHash) < 3 {
		return "articles_" + article.Dir3 + "000" // fallback
	}
	return fmt.Sprintf("articles_%s%s", article.Dir3, article.MessageIDHash[0:3])
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

	processBatch := func() error {
		if len(batch) == 0 {
			return nil
		}

		if *dryRun {
			atomic.AddInt64(&totalProcessed, int64(len(batch)))
			batch = batch[:0]
			return nil
		}

		tx, err := currentDB.Begin()
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()

		// Group articles by table for more efficient batching
		articlesByTable := make(map[string][]*Article)
		for _, article := range batch {
			tableName := getTableName(article)
			articlesByTable[tableName] = append(articlesByTable[tableName], article)
		}

		// Process each table separately
		for tableName, tableArticles := range articlesByTable {
			query := fmt.Sprintf("INSERT INTO %s (messageid_hash, head, body) VALUES (?, ?, ?)", tableName)
			stmt, err := tx.Prepare(query)
			if err != nil {
				return fmt.Errorf("failed to prepare statement for table %s: %w", tableName, err)
			}

			for _, article := range tableArticles {
				storedHash := getStoredHash(article)
				if _, err := stmt.Exec(storedHash, article.Head, article.Body); err != nil {
					log.Printf("Worker %d: Error inserting article %s into table %s: %v", id, storedHash, tableName, err)
					atomic.AddInt64(&totalErrors, 1)
					continue
				}
			}
			stmt.Close()
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		atomic.AddInt64(&totalProcessed, int64(len(batch)))
		batch = batch[:0]
		return nil
	}

	for article := range articles {
		// Determine which database this article belongs to from directory structure
		if len(article.MessageIDHash) < 32 { // should be at least md5 or longer
			log.Printf("Worker %d: Invalid hash length for %s", id, article.MessageIDHash)
			atomic.AddInt64(&totalErrors, 1)
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
			if err := processBatch(); err != nil {
				log.Printf("Worker %d: Error processing batch: %v", id, err)
				atomic.AddInt64(&totalErrors, int64(len(batch)))
				batch = batch[:0]
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
			if err := processBatch(); err != nil {
				log.Printf("Worker %d: Error processing batch: %v", id, err)
				atomic.AddInt64(&totalErrors, int64(len(batch)))
				batch = batch[:0]
			}
		}
	}

	// Process final batch
	if err := processBatch(); err != nil {
		log.Printf("Worker %d: Error processing final batch: %v", id, err)
		atomic.AddInt64(&totalErrors, int64(len(batch)))
	}

	log.Printf("Worker %d finished", id)
}

func readFile(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var builder strings.Builder
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		builder.WriteString(scanner.Text())
		builder.WriteByte('\n')
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return builder.String(), nil
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
