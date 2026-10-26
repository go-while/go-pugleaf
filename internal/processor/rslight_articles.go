package processor

import (
	"database/sql"
	"errors"
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

// legacyArticleOutcome classifies the result of importing one legacy article.
type legacyArticleOutcome int

const (
	legacyImported     legacyArticleOutcome = iota
	legacySkippedEmpty                      // empty article, nothing to import
	legacyDuplicate                         // already known, not a failure
	legacyFailed                            // parse or process error
)

// classifyLegacyImportErr maps the error of importLegacyArticle to its counter.
func classifyLegacyImportErr(err error) legacyArticleOutcome {
	switch {
	case err == nil:
		return legacyImported
	case errors.Is(err, error_empty_article):
		return legacySkippedEmpty
	case errors.Is(err, errLegacyDuplicate):
		return legacyDuplicate
	default:
		return legacyFailed
	}
}

// ImportSQLiteArticles imports articles from legacy SQLite databases
func (leg *LegacyImporter) ImportSQLiteArticles(sqlitePath string) error {
	return leg.importSQLiteArticlesWith(sqlitePath, leg.importLegacyArticle)
}

// importSQLiteArticlesWith reads every article of one legacy SQLite file and passes it to
// importArticle. Failed, empty and duplicate articles are counted, not returned.
func (leg *LegacyImporter) importSQLiteArticlesWith(sqlitePath string, importArticle func(*LegacyArticle) error) error {
	//log.Printf("Starting SQLite articles import from: %s", sqlitePath)

	// Open the legacy SQLite database
	legacyDB, err := sql.Open("sqlite3", sqlitePath)
	if err != nil {
		return fmt.Errorf("failed to open legacy SQLite database: %w", err)
	}
	defer legacyDB.Close()

	// Query all articles in a single query - much faster than LIMIT/OFFSET
	groupsCounter := make(map[string]int64)
	var imported, failed, skippedEmpty, duplicates uint64
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

	stopped := false
	for rows.Next() {
		// a closed leg.stop ends the file after the current article (counters are kept)
		select {
		case <-leg.stop: // nil: never
			stopped = true
		default:
		}
		if stopped {
			break
		}
		var article LegacyArticle
		err := rows.Scan(
			&article.Newsgroup,
			&article.MsgID,
			&article.Article,
		)
		if err != nil {
			log.Printf("Warning: failed to scan article row: %v", err)
			failed++
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
		err = importArticle(&article)
		switch classifyLegacyImportErr(err) {
		case legacySkippedEmpty:
			skippedEmpty++
			continue
		case legacyDuplicate:
			duplicates++
			continue
		case legacyFailed:
			failed++
			log.Printf("[RSLIGHT-IMPORT] WARN article %s db='%s': %v", article.MsgID, sqlitePath, err)
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
	if !stopped {
		if err = rows.Err(); err != nil {
			return fmt.Errorf("error during row iteration: %w", err)
		}
	}

	state := "DONE"
	if stopped {
		state = "STOPPED"
	}
	log.Printf("[RSLIGHT-IMPORT] %s: %d imported, %d failed, %d skippedEmpty, %d duplicates @db='%s' took %v", state, imported, failed, skippedEmpty, duplicates, sqlitePath, time.Since(start))
	// update total counters (the only groupsDone increment per file; a stopped file is not done)
	leg.mux.Lock()
	defer leg.mux.Unlock()
	leg.imported += imported
	leg.skipped += failed
	leg.skippedEmpty += skippedEmpty
	leg.duplicates += duplicates
	if !stopped {
		leg.groupsDone++
	}
	for group, count := range groupsCounter {
		leg.proc.LegacyCounter.Add("articles:"+group, count)
	}
	if stopped {
		return fmt.Errorf("file '%s' not finished: %w", sqlitePath, ErrLegacyImportStopped)
	}
	return nil
}
