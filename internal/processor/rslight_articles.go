package processor

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
)

func (leg *LegacyImporter) QuickOpenToGetNewsgroup(sqlitePath string) (string, error) {
	// Open the legacy SQLite database
	legacyDB, err := sql.Open("sqlite3", sqlitePath)
	if err != nil {
		return "", fmt.Errorf("failed to open legacy SQLite database: %w", err)
	}
	defer legacyDB.Close()
	//log.Printf("[RSLIGHT-IMPORT] Getting newsgroup from db='%s'", sqlitePath)

	rows, err := database.RetryableQuery(legacyDB, `
		SELECT newsgroup
		FROM articles
		ORDER BY id ASC LIMIT 1
	`)
	if err != nil {
		return "", fmt.Errorf("failed to query legacy articles: %w", err)
	}
	defer rows.Close()
	var article LegacyArticle
	for rows.Next() {
		err := rows.Scan(
			&article.Newsgroup,
		)
		if err != nil {
			log.Printf("Warning: failed to scan article row: %v", err)
			return "", fmt.Errorf("failed to scan article row: %w", err)
		}
	}
	log.Printf("[RSLIGHT-IMPORT] db='%s' => Newsgroup: %s", sqlitePath, article.Newsgroup)
	return article.Newsgroup, nil
}

// ImportSQLiteArticles imports articles from legacy SQLite databases
func (leg *LegacyImporter) ImportSQLiteArticles(sqlitePath string) error {
	//log.Printf("Starting SQLite articles import from: %s", sqlitePath)

	// Open the legacy SQLite database
	legacyDB, err := sql.Open("sqlite3", sqlitePath)
	if err != nil {
		return fmt.Errorf("failed to open legacy SQLite database: %w", err)
	}
	defer legacyDB.Close()

	// Query all articles in a single query - much faster than LIMIT/OFFSET
	groupsCounter := make(map[string]int64)
	var imported, skipped, skippedEmpty uint64
	lastReportTime := time.Now()
	lastReportCount := uint64(0)
	start := time.Now()

	//log.Printf("[RSLIGHT-IMPORT] Starting single query to read all articles from db='%s'", sqlitePath)
	//queryStart := time.Now()

	rows, err := database.RetryableQuery(legacyDB, `
		SELECT newsgroup, msgid, article
		FROM articles
		ORDER BY id ASC
	`)
	if err != nil {
		return fmt.Errorf("failed to query legacy articles: %w", err)
	}
	defer rows.Close()

	//log.Printf("[RSLIGHT-IMPORT] queried all articles from db='%s' took %v", sqlitePath, time.Since(queryStart))

	// Process articles one by one as we iterate through the result set
	const reportInterval = 1000 // Report progress every 1000 articles
	var articlesRead uint64

	for rows.Next() {
		var article LegacyArticle
		err := rows.Scan(
			&article.Newsgroup,
			&article.MsgID,
			&article.Article,
		)
		if err != nil {
			log.Printf("Warning: failed to scan article row: %v", err)
			skipped++
			continue
		}

		articlesRead++
		article.Newsgroup = strings.TrimSpace(article.Newsgroup)
		newsgroup := strings.TrimSuffix(filepath.Base(sqlitePath), "-articles.db3")

		if newsgroup != article.Newsgroup {
			log.Printf("[RSLIGHT-IMPORT] Info: newsgroup '%s' from file '%s' does not match expected newsgroup '%s'", article.Newsgroup, sqlitePath, newsgroup)
			article.Newsgroup = newsgroup // overwrite primary newsgroup so that we surely import that article to that group because it was a crosspost!
		}
		// Process the article immediately
		err = leg.importLegacyArticle(&article)
		if err != nil {
			if err != error_empty_article {
				skippedEmpty++
				log.Printf("[RSLIGHT-IMPORT] WARN article %s db='%s': %v", article.MsgID, sqlitePath, err)
				continue
			}
			skipped++
			continue
		}

		imported++
		groupsCounter[article.Newsgroup]++

		// Progress reporting
		if articlesRead%reportInterval == 0 {
			now := time.Now()
			elapsed := now.Sub(lastReportTime)
			processed := imported - lastReportCount
			rate := float64(processed) / elapsed.Seconds()
			log.Printf("[RSLIGHT-IMPORT] did %d articles - rate: %.1f art/s (total read: %d) db='%s' took %v",
				imported, rate, articlesRead, sqlitePath, elapsed)
			lastReportTime = now
			lastReportCount = imported
		}
	}

	// Check for iteration errors
	if err = rows.Err(); err != nil {
		return fmt.Errorf("error during row iteration: %w", err)
	}

	log.Printf("[RSLIGHT-IMPORT] DONE: %d imported, %d skipped, %d skippedEmpty @db='%s' took %v", imported, skipped, skippedEmpty, sqlitePath, time.Since(start))
	// update total counters
	leg.mux.Lock()
	defer leg.mux.Unlock()
	leg.imported += imported
	leg.skipped += skipped
	leg.groupsDone++
	for group, count := range groupsCounter {
		leg.proc.LegacyCounter.Add("articles:"+group, count)
	}
	return nil
}
