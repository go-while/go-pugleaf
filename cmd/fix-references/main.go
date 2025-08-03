package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// multiLineHeaderToStringSpaced joins multi-line headers with spaces (for RFC-compliant header unfolding)
func multiLineHeaderToStringSpaced(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) == 1 {
		return vals[0] // Fast path for single-line headers
	}
	var sb strings.Builder
	for i, line := range vals {
		// Trim each line and add spaces between them
		line = strings.TrimSpace(line)
		if line == "" {
			continue // Skip empty lines
		}
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// getHeaderFirst returns the first value for a header, or "" if not present
func getHeaderFirst(headers map[string][]string, key string) string {
	if vals, ok := headers[key]; ok && len(vals) > 0 {
		// For headers that can be folded across multiple lines (like References),
		// we need to join with spaces instead of newlines to properly unfold them
		if key == "references" || key == "References" || key == "in-reply-to" || key == "In-Reply-To" {
			return multiLineHeaderToStringSpaced(vals)
		}
		// For other headers, just return first value
		if len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

// parseRawHeaders parses raw header string (with \n line breaks) into a map
func parseRawHeaders(rawHeaders string) map[string][]string {
	headers := make(map[string][]string)

	lines := strings.Split(rawHeaders, "\n")
	var currentHeader string
	var currentValue []string

	for _, line := range lines {
		// Check if this is a continuation line (starts with space or tab)
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			// This is a continuation of the previous header
			if currentHeader != "" {
				currentValue = append(currentValue, line)
			}
		} else {
			// This is a new header or end of headers
			if currentHeader != "" {
				// Store the previous header
				headers[strings.ToLower(currentHeader)] = currentValue
			}

			// Parse the new header
			if strings.Contains(line, ":") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					currentHeader = strings.TrimSpace(parts[0])
					currentValue = []string{strings.TrimSpace(parts[1])}
				}
			} else {
				// No colon found, reset
				currentHeader = ""
				currentValue = nil
			}
		}
	}

	// Don't forget the last header
	if currentHeader != "" {
		headers[strings.ToLower(currentHeader)] = currentValue
	}

	return headers
}

func main() {
	var (
		dbPath         = flag.String("db", "data", "Data Path to main data directory (required)")
		newsgroup      = flag.String("group", "$all", "Newsgroup name to fix (required) (\\$all to fix all)")
		verbose        = flag.Bool("v", false, "Verbose output")
		dryRun         = flag.Bool("dry-run", false, "Show what would be fixed without making changes")
		limit          = flag.Int("limit", 0, "Limit number of articles to process (0 = no limit)")
		rebuildThreads = flag.Bool("rebuild-threads", false, "Rebuild thread relationships after fixing references (default: false)")
		batchSize      = flag.Int("batch-size", 10000, "Number of articles to process in each batch (for large datasets)")
	)
	flag.Parse()

	if *dbPath == "" || *newsgroup == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s -db <database-path> -group <newsgroup-name> [options]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExample:\n")
		fmt.Fprintf(os.Stderr, "  %s -db /path/to/databases -group comp.lang.go\n", os.Args[0])
		os.Exit(1)
	}

	// Validate database path
	if _, err := os.Stat(*dbPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: Database path '%s' does not exist\n", *dbPath)
		os.Exit(1)
	}

	// Initialize configuration
	mainConfig := config.NewDefaultConfig()
	log.Printf("go-pugleaf References Header Fix Tool (version: %s)", mainConfig.AppVersion)

	// Initialize database connection
	db, err := database.OpenDatabase(nil)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Shutdown()

	// Apply migrations
	if err := db.Migrate(); err != nil {
		log.Fatalf("Failed to apply database migrations: %v", err)
	}

	var newsgroups []*models.Newsgroup
	if *newsgroup != "$all" && *newsgroup != "" {
		newsgroups = append(newsgroups, &models.Newsgroup{
			Name: *newsgroup,
		})
	} else {
		newsgroups, err = db.MainDBGetAllNewsgroups()
		if err != nil {
			log.Fatalf("failed to get newsgroups from database: %v", err)
		}
	}

	fmt.Printf("🔧 Starting References Header Fix Tool for go-pugleaf\n")
	fmt.Printf("====================================================\n")
	fmt.Printf("📂 Data Path: %s\n", *dbPath)
	fmt.Printf("📊 Newsgroups: %d\n", len(newsgroups))
	fmt.Printf("🔍 Dry Run: %v\n", *dryRun)
	fmt.Printf("📝 Verbose: %v\n", *verbose)
	fmt.Printf("🧵 Rebuild Threads: %v\n", *rebuildThreads)
	if *limit > 0 {
		fmt.Printf("🔢 Limit: %d articles\n", *limit)
	}
	fmt.Printf("\n")

	var totalProcessed, totalFixed int

	for i, newsgroup := range newsgroups {
		fmt.Printf("🔍 [%d/%d] Processing newsgroup: %s\n", i+1, len(newsgroups), newsgroup.Name)

		groupDB, err := db.GetGroupDBs(newsgroup.Name)
		if err != nil {
			fmt.Printf("   ❌ Failed to get group database: %v\n", err)
			continue
		}

		processed, fixed, err := fixReferencesInNewsgroup(groupDB, *dryRun, *verbose, *limit, *batchSize)
		if err != nil {
			fmt.Printf("   ❌ Failed to fix references: %v\n", err)
			groupDB.Return(db)
			continue
		}

		totalProcessed += processed
		totalFixed += fixed

		// Rebuild threads if requested
		if *rebuildThreads && !*dryRun {
			fmt.Printf("   🧵 Rebuilding thread relationships...\n")
			threadsRebuilt, err := rebuildThreadsInNewsgroup(groupDB, *verbose, *batchSize)
			if err != nil {
				fmt.Printf("   ❌ Failed to rebuild threads: %v\n", err)
			} else {
				fmt.Printf("   ✅ Rebuilt %d thread relationships\n", threadsRebuilt)
			}
		}

		groupDB.Return(db)

		if processed > 0 {
			if fixed > 0 {
				fmt.Printf("   📊 Processed: %d articles, Fixed: %d references\n", processed, fixed)
			} else {
				fmt.Printf("   ✅ Processed: %d articles, no broken references found\n", processed)
			}
		} else {
			fmt.Printf("   📭 No articles to process\n")
		}
	}

	fmt.Printf("\n")
	fmt.Printf("====================================================\n")
	fmt.Printf("REFERENCES HEADER FIX SUMMARY\n")
	fmt.Printf("====================================================\n")
	fmt.Printf("Total articles processed: %d\n", totalProcessed)
	fmt.Printf("Total references fixed: %d\n", totalFixed)
	if *dryRun {
		fmt.Printf("\n🔍 DRY RUN - No changes were made\n")
		fmt.Printf("Run without -dry-run to apply fixes\n")
	} else if totalFixed > 0 {
		fmt.Printf("\n✅ References headers have been fixed\n")
		fmt.Printf("💡 Consider rebuilding thread indexes for better performance\n")
	} else {
		fmt.Printf("\n✅ No broken references found - all headers are correct\n")
	}
}

func fixReferencesInNewsgroup(groupDB *database.GroupDBs, dryRun, verbose bool, limit, batchSize int) (int, int, error) {
	// Get total count for progress tracking
	var totalCount int
	countQuery := "SELECT COUNT(*) FROM articles WHERE headers_json IS NOT NULL AND headers_json != ''"
	if limit > 0 {
		countQuery += fmt.Sprintf(" AND article_num <= (SELECT MIN(article_num) + %d - 1 FROM articles WHERE headers_json IS NOT NULL AND headers_json != '')", limit)
	}

	err := groupDB.DB.QueryRow(countQuery).Scan(&totalCount)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get article count: %w", err)
	}

	if limit > 0 && limit < totalCount {
		totalCount = limit
	}

	if verbose {
		fmt.Printf("   📊 Processing %d articles in batches of %d\n", totalCount, batchSize)
	}

	var totalProcessed, totalFixed int
	offset := 0

	for offset < totalCount {
		currentBatchSize := batchSize
		if offset+batchSize > totalCount {
			currentBatchSize = totalCount - offset
		}

		processed, fixed, err := processBatch(groupDB, dryRun, verbose, offset, currentBatchSize)
		if err != nil {
			return totalProcessed, totalFixed, fmt.Errorf("failed to process batch at offset %d: %w", offset, err)
		}

		totalProcessed += processed
		totalFixed += fixed
		offset += currentBatchSize

		if verbose {
			fmt.Printf("   📊 Progress: %d/%d articles processed (%d%%), %d fixed so far\n",
				totalProcessed, totalCount, (totalProcessed*100)/totalCount, totalFixed)
		}
	}

	return totalProcessed, totalFixed, nil
}

func processBatch(groupDB *database.GroupDBs, dryRun, verbose bool, offset, batchSize int) (int, int, error) {
	// Query articles with potentially broken references
	query := `
		SELECT article_num, message_id, "references", headers_json
		FROM articles
		WHERE headers_json IS NOT NULL AND headers_json != ''
		ORDER BY article_num
		LIMIT ? OFFSET ?
	`

	rows, err := groupDB.DB.Query(query, batchSize, offset)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to query articles: %w", err)
	}
	defer rows.Close()

	var processed, fixed int
	var tx *sql.Tx

	if !dryRun {
		tx, err = groupDB.DB.Begin()
		if err != nil {
			return 0, 0, fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()
	}

	for rows.Next() {
		var articleNum int64
		var messageID, storedReferences, headersJSON string

		err := rows.Scan(&articleNum, &messageID, &storedReferences, &headersJSON)
		if err != nil {
			if verbose {
				fmt.Printf("   ⚠️  Error scanning article %d: %v\n", articleNum, err)
			}
			continue
		}
		log.Printf("processing: ng:'%s' msgId='%s' (%d)", groupDB.Newsgroup, messageID, articleNum)
		processed++

		// Parse the raw headers string (not JSON, despite the field name)
		headers := parseRawHeaders(headersJSON)

		// Extract the correct references using our fixed function
		correctReferences := getHeaderFirst(headers, "references")
		if messageID == "<106kmd6$vnsl$1@dont-email.me>" {
			log.Printf("DEBUG storedReferences:  '%s'", storedReferences)
			log.Printf("DEBUG correctReferences: '%s'", correctReferences)
		}
		// Check if the stored references differ from the correct ones
		if storedReferences != correctReferences {
			fixed++

			if verbose {
				fmt.Printf("   🔧 Article %d (%s):\n", articleNum, messageID)
				fmt.Printf("      Old: %q\n", storedReferences)
				fmt.Printf("      New: %q\n", correctReferences)
			}

			if !dryRun {
				_, err := tx.Exec("UPDATE articles SET \"references\" = ? WHERE article_num = ?", correctReferences, articleNum)
				if err != nil {
					if verbose {
						fmt.Printf("   ❌ Failed to update article %d: %v\n", articleNum, err)
					}
					continue
				}
			}
		}
	}

	if err = rows.Err(); err != nil {
		return processed, fixed, fmt.Errorf("error iterating rows: %w", err)
	}

	if !dryRun && tx != nil {
		if err := tx.Commit(); err != nil {
			return processed, fixed, fmt.Errorf("failed to commit reference fixes: %w", err)
		}
	}

	return processed, fixed, nil
}

// rebuildThreadsInNewsgroup rebuilds thread relationships for a newsgroup using batched processing
func rebuildThreadsInNewsgroup(groupDB *database.GroupDBs, verbose bool, batchSize int) (int, error) {
	// Get total article count
	var totalCount int
	err := groupDB.DB.QueryRow("SELECT COUNT(*) FROM articles").Scan(&totalCount)
	if err != nil {
		return 0, fmt.Errorf("failed to get article count: %w", err)
	}

	if verbose {
		fmt.Printf("   📊 Rebuilding threads for %d articles in batches of %d\n", totalCount, batchSize)
	}

	// First, clear existing thread data to start fresh
	tx, err := groupDB.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Clear existing threads
	_, err = tx.Exec("DELETE FROM threads")
	if err != nil {
		return 0, fmt.Errorf("failed to clear existing threads: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit thread cleanup: %w", err)
	}

	// Build message-ID to article-number mapping efficiently
	msgIDToArticleNum := make(map[string]int64)

	offset := 0
	for offset < totalCount {
		currentBatchSize := batchSize
		if offset+batchSize > totalCount {
			currentBatchSize = totalCount - offset
		}

		// Load batch of article mappings
		rows, err := groupDB.DB.Query(`
			SELECT article_num, message_id
			FROM articles
			ORDER BY article_num
			LIMIT ? OFFSET ?`, currentBatchSize, offset)

		if err != nil {
			return 0, fmt.Errorf("failed to query articles batch: %w", err)
		}

		for rows.Next() {
			var articleNum int64
			var messageID string
			if err := rows.Scan(&articleNum, &messageID); err != nil {
				rows.Close()
				return 0, fmt.Errorf("failed to scan article mapping: %w", err)
			}
			msgIDToArticleNum[messageID] = articleNum
		}
		rows.Close()

		offset += currentBatchSize

		if verbose && offset%50000 == 0 {
			fmt.Printf("   📊 Built message-ID mapping: %d/%d articles\n", offset, totalCount)
		}
	}

	if verbose {
		fmt.Printf("   📊 Message-ID mapping complete: %d entries\n", len(msgIDToArticleNum))
	}

	// Now process articles in batches to build thread relationships
	var totalThreadsBuilt int
	offset = 0

	for offset < totalCount {
		currentBatchSize := batchSize
		if offset+batchSize > totalCount {
			currentBatchSize = totalCount - offset
		}

		threadsBuilt, err := processThreadBatch(groupDB, msgIDToArticleNum, offset, currentBatchSize, verbose)
		if err != nil {
			return totalThreadsBuilt, fmt.Errorf("failed to process thread batch at offset %d: %w", offset, err)
		}

		totalThreadsBuilt += threadsBuilt
		offset += currentBatchSize

		if verbose {
			fmt.Printf("   📊 Threading progress: %d/%d articles processed, %d threads built\n",
				offset, totalCount, totalThreadsBuilt)
		}
	}

	return totalThreadsBuilt, nil
}

func processThreadBatch(groupDB *database.GroupDBs, msgIDToArticleNum map[string]int64, offset, batchSize int, verbose bool) (int, error) {
	// Get batch of articles with their references
	rows, err := groupDB.DB.Query(`
		SELECT article_num, message_id, "references"
		FROM articles
		ORDER BY article_num
		LIMIT ? OFFSET ?
	`, batchSize, offset)
	if err != nil {
		return 0, fmt.Errorf("failed to query articles: %w", err)
	}
	defer rows.Close()

	// Start transaction for this batch
	tx, err := groupDB.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	threadStmt, err := tx.Prepare("INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return 0, fmt.Errorf("failed to prepare thread insert statement: %w", err)
	}
	defer threadStmt.Close()

	var threadsBuilt int

	for rows.Next() {
		var articleNum int64
		var messageID, references string

		err := rows.Scan(&articleNum, &messageID, &references)
		if err != nil {
			if verbose {
				fmt.Printf("   ⚠️  Error scanning article: %v\n", err)
			}
			continue
		}

		refs := parseReferences(references)

		if len(refs) == 0 {
			// This is a thread root
			_, err = threadStmt.Exec(articleNum, nil, articleNum, 0, 0)
			if err != nil {
				if verbose {
					fmt.Printf("   ⚠️  Failed to insert thread root for article %d: %v\n", articleNum, err)
				}
				continue
			}
			threadsBuilt++
		} else {
			// This is a reply - find the best parent
			var parentArticleNum int64
			var rootArticleNum int64
			depth := 1

			// Find the most recent parent in the references chain
			for i := len(refs) - 1; i >= 0; i-- {
				if parentNum, exists := msgIDToArticleNum[refs[i]]; exists {
					parentArticleNum = parentNum

					// Find the root of this thread by looking up the parent's thread entry
					err := tx.QueryRow("SELECT root_article, depth FROM threads WHERE child_article = ?", parentNum).Scan(&rootArticleNum, &depth)
					if err == nil {
						depth++ // This article is one level deeper than its parent
						break
					}
					// If parent not found in threads yet, treat parent as root
					rootArticleNum = parentNum
					depth = 1
					break
				}
			}

			// If no parent found in our database, treat this as a root
			if parentArticleNum == 0 {
				rootArticleNum = articleNum
				depth = 0
			}

			_, err = threadStmt.Exec(rootArticleNum, parentArticleNum, articleNum, depth, 0)
			if err != nil {
				if verbose {
					fmt.Printf("   ⚠️  Failed to insert thread entry for article %d: %v\n", articleNum, err)
				}
				continue
			}
			threadsBuilt++
		}
	}

	if err = rows.Err(); err != nil {
		return threadsBuilt, fmt.Errorf("error iterating articles: %w", err)
	}

	// Commit this batch
	if err := tx.Commit(); err != nil {
		return threadsBuilt, fmt.Errorf("failed to commit thread batch: %w", err)
	}

	return threadsBuilt, nil
} // parseReferences is a simplified version of utils.ParseReferences for this tool
func parseReferences(refs string) []string {
	if refs == "" {
		return []string{}
	}

	// Use strings.Fields() for robust whitespace handling
	fields := strings.Fields(refs)

	var cleanRefs []string
	for _, ref := range fields {
		ref = strings.TrimSpace(ref)
		if ref != "" && strings.HasPrefix(ref, "<") && strings.HasSuffix(ref, ">") {
			cleanRefs = append(cleanRefs, ref)
		}
	}

	return cleanRefs
}
