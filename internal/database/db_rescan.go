package database

import (
	"fmt"
	"log"
)

// RecoverDatabase attempts to recover the database by checking for missing articles and last_insert_ids mismatches

func (db *Database) Rescan(newsgroup string) error {
	if newsgroup == "" {
		return nil // Nothing to rescan
	}
	// first look into the maindb newsgroups table and get the latest numbers
	latest, err := db.GetLatestArticleNumbers(newsgroup)
	if err != nil {
		return err
	}
	// open groupDBs
	groupDB, err := db.GetGroupDBs(newsgroup)
	if err != nil {
		return err
	}
	defer groupDB.Return(db)
	// Get the latest article number from the groupDB
	latestArticle, err := db.GetLatestArticleNumberFromOverview(newsgroup)
	if err != nil {
		return err
	}
	// Compare with the latest from the mainDB
	if latestArticle > latest[newsgroup] {
		log.Printf("Found new articles in group '%s': %d (latest: %d)", newsgroup, latestArticle, latest[newsgroup])
		// TODO: Handle new articles (e.g., fetch and insert into mainDB)
	}
	return nil
}

func (db *Database) GetLatestArticleNumberFromOverview(newsgroup string) (int64, error) {
	// Since overview table is unified with articles, query articles table instead
	groupDB, err := db.GetGroupDBs(newsgroup)
	if err != nil {
		return 0, err
	}
	defer groupDB.Return(db)

	var latestArticle int64
	err = groupDB.DB.QueryRow(`
		SELECT MAX(article_num)
		FROM articles
	`).Scan(&latestArticle)
	if err != nil {
		return 0, err
	}

	return latestArticle, nil
}

func (db *Database) GetLatestArticleNumbers(newsgroup string) (map[string]int64, error) {
	// Query the latest article numbers for the specified newsgroup
	rows, err := db.GetMainDB().Query(`
		SELECT name, last_article
		FROM newsgroups
		WHERE name = ?
	`, newsgroup)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	latest := make(map[string]int64)
	for rows.Next() {
		var group string
		var lastID int64
		if err := rows.Scan(&group, &lastID); err != nil {
			return nil, err
		}
		latest[group] = lastID
	}

	return latest, nil
}

// ConsistencyReport represents the results of a database consistency check
type ConsistencyReport struct {
	Newsgroup           string
	MainDBLastArticle   int64
	ArticlesMaxNum      int64
	OverviewMaxNum      int64
	ThreadsMaxNum       int64
	ArticleCount        int64
	OverviewCount       int64
	ThreadCount         int64
	MissingArticles     []int64
	MissingOverviews    []int64
	OrphanedOverviews   []int64 // New: overview entries without articles
	OrphanedThreads     []int64
	MessageIDMismatches []string
	Errors              []string
	HasInconsistencies  bool
}

// CheckDatabaseConsistency performs a comprehensive consistency check for a newsgroup
func (db *Database) CheckDatabaseConsistency(newsgroup string) (*ConsistencyReport, error) {
	report := &ConsistencyReport{
		Newsgroup:           newsgroup,
		MissingArticles:     []int64{},
		MissingOverviews:    []int64{},
		OrphanedOverviews:   []int64{},
		OrphanedThreads:     []int64{},
		MessageIDMismatches: []string{},
		Errors:              []string{},
	}

	// 1. Get main DB newsgroup info
	mainDBInfo, err := db.GetLatestArticleNumbers(newsgroup)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get main DB info: %v", err))
		return report, nil
	}
	if lastArticle, exists := mainDBInfo[newsgroup]; exists {
		report.MainDBLastArticle = lastArticle
	}

	// 2. Get group databases
	groupDB, err := db.GetGroupDBs(newsgroup)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get group databases: %v", err))
		return report, nil
	}
	defer groupDB.Return(db)

	// 3. Get max article numbers from each table (handle NULL for empty tables)
	err = groupDB.DB.QueryRow("SELECT COALESCE(MAX(article_num), 0) FROM articles").Scan(&report.ArticlesMaxNum)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get max article_num from articles: %v", err))
	}

	// Since overview is now unified with articles, OverviewMaxNum equals ArticlesMaxNum
	report.OverviewMaxNum = report.ArticlesMaxNum

	err = groupDB.DB.QueryRow("SELECT COALESCE(MAX(root_article), 0) FROM threads").Scan(&report.ThreadsMaxNum)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get max root_article from threads: %v", err))
	}

	// 4. Get counts from each table
	err = groupDB.DB.QueryRow("SELECT COUNT(*) FROM articles").Scan(&report.ArticleCount)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get article count: %v", err))
	}

	// Since overview is now unified with articles, OverviewCount equals ArticleCount
	report.OverviewCount = report.ArticleCount

	err = groupDB.DB.QueryRow("SELECT COUNT(*) FROM threads").Scan(&report.ThreadCount)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("Failed to get thread count: %v", err))
	}

	// 5. Find missing articles (gaps in article numbering)
	report.MissingArticles = db.findMissingArticles(groupDB, report.ArticlesMaxNum)

	// Since overview is unified with articles, there are no missing or orphaned overviews
	report.MissingOverviews = []int64{}  // No longer needed
	report.OrphanedOverviews = []int64{} // No longer needed
	//report.OrphanedOverviews = db.findOrphanedOverviews(groupDB)

	// 8. Find orphaned threads (threads pointing to non-existent articles)
	report.OrphanedThreads = db.findOrphanedThreads(groupDB)

	// Since overview is unified with articles, no message ID mismatches are possible
	report.MessageIDMismatches = []string{} // No longer needed

	// 10. Determine if there are inconsistencies (simplified for unified schema)
	report.HasInconsistencies = len(report.MissingArticles) > 0 ||
		len(report.OrphanedThreads) > 0 ||
		len(report.Errors) > 0 ||
		report.MainDBLastArticle != report.ArticlesMaxNum
		// Removed overview-related checks since overview is unified with articles

	return report, nil
}

// findMissingArticles finds gaps in article numbering
func (db *Database) findMissingArticles(groupDB *GroupDBs, maxArticleNum int64) []int64 {
	var missing []int64
	if maxArticleNum <= 0 {
		return missing
	}

	// Get all article numbers
	rows, err := groupDB.DB.Query("SELECT article_num FROM articles ORDER BY article_num")
	if err != nil {
		return missing
	}
	defer rows.Close()

	var articleNums []int64
	for rows.Next() {
		var num int64
		if err := rows.Scan(&num); err != nil {
			continue
		}
		articleNums = append(articleNums, num)
	}

	// Find gaps
	expectedNum := int64(1)
	for _, actualNum := range articleNums {
		for expectedNum < actualNum {
			missing = append(missing, expectedNum)
			expectedNum++
		}
		expectedNum = actualNum + 1
	}

	return missing
}

// findOrphanedThreads finds thread entries pointing to non-existent articles
func (db *Database) findOrphanedThreads(groupDB *GroupDBs) []int64 {
	var orphaned []int64

	// Get all article numbers from articles table
	articleNums := make(map[int64]bool)
	rows, err := groupDB.DB.Query("SELECT article_num FROM articles")
	if err != nil {
		return orphaned
	}
	defer rows.Close()

	for rows.Next() {
		var num int64
		if err := rows.Scan(&num); err != nil {
			continue
		}
		articleNums[num] = true
	}

	// Get all root_article numbers from threads table
	rows, err = groupDB.DB.Query("SELECT DISTINCT root_article FROM threads")
	if err != nil {
		return orphaned
	}
	defer rows.Close()

	for rows.Next() {
		var rootArticle int64
		if err := rows.Scan(&rootArticle); err != nil {
			continue
		}
		// Check if this root_article exists in articles table
		if !articleNums[rootArticle] {
			orphaned = append(orphaned, rootArticle)
		}
	}

	return orphaned
}

// PrintConsistencyReport prints a human-readable consistency report
func (report *ConsistencyReport) PrintReport() {
	fmt.Printf("\n=== Database Consistency Report for '%s' ===\n", report.Newsgroup)

	if len(report.Errors) > 0 {
		fmt.Printf("ERRORS:\n")
		for _, err := range report.Errors {
			fmt.Printf("  - %s\n", err)
		}
		fmt.Printf("\n")
	}

	fmt.Printf("Main DB Last Article: %d\n", report.MainDBLastArticle)
	fmt.Printf("Articles Max Num:     %d\n", report.ArticlesMaxNum)
	fmt.Printf("Overview Max Num:     %d\n", report.OverviewMaxNum)
	fmt.Printf("Threads Max Num:      %d\n", report.ThreadsMaxNum)
	fmt.Printf("\n")

	fmt.Printf("Article Count:        %d\n", report.ArticleCount)
	fmt.Printf("Overview Count:       %d\n", report.OverviewCount)
	fmt.Printf("Thread Count:         %d\n", report.ThreadCount)
	fmt.Printf("\n")

	if len(report.MissingArticles) > 0 {
		fmt.Printf("Missing Articles (%d): %v\n", len(report.MissingArticles), report.MissingArticles)
	}

	if len(report.MissingOverviews) > 0 {
		fmt.Printf("Missing Overviews (%d): %v\n", len(report.MissingOverviews), report.MissingOverviews)
	}

	if len(report.OrphanedOverviews) > 0 {
		fmt.Printf("Orphaned Overviews (%d): %v\n", len(report.OrphanedOverviews), report.OrphanedOverviews)
	}

	if len(report.OrphanedThreads) > 0 {
		fmt.Printf("Orphaned Threads (%d): %v\n", len(report.OrphanedThreads), report.OrphanedThreads)
	}

	if len(report.MessageIDMismatches) > 0 {
		fmt.Printf("Message ID Mismatches (%d): %v\n", len(report.MessageIDMismatches), report.MessageIDMismatches)
	}

	if report.HasInconsistencies {
		fmt.Printf("\n❌ INCONSISTENCIES DETECTED!\n")
	} else {
		fmt.Printf("\n✅ Database is consistent.\n")
	}
	fmt.Printf("============================================\n\n")
}

/* CODE REFERENCE

type Article struct {
	GetDataFunc func(what string, group string) string `json:"-" db:"-"`
	RWMutex     sync.RWMutex                           `json:"-" db:"-"`
	ArticleNum  int64                                  `json:"article_num" db:"article_num"`
	MessageID   string                                 `json:"message_id" db:"message_id"`
	Subject     string                                 `json:"subject" db:"subject"`
	FromHeader  string                                 `json:"from_header" db:"from_header"`
	DateSent    time.Time                              `json:"date_sent" db:"date_sent"`
	DateString  string                                 `json:"date_string" db:"date_string"`
	References  string                                 `json:"references" db:"references"`
	Bytes       int                                    `json:"bytes" db:"bytes"`
	Lines       int                                    `json:"lines" db:"lines"`
	ReplyCount  int                                    `json:"reply_count" db:"reply_count"`
	HeadersJSON string                                 `json:"headers_json" db:"headers_json"`
	BodyText    string                                 `json:"body_text" db:"body_text"`
	Path        string                                 `json:"path" db:"path"` // headers network path
	ImportedAt  time.Time                              `json:"imported_at" db:"imported_at"`
	Sanitized   bool                                   `json:"-" db:"-"`
	MsgIdItem   *history.MessageIdItem                 `json:"-" db:"-"` // Cached MessageIdItem for history lookups
}

// Newsgroup represents a subscribed newsgroup
type Newsgroup struct {
	ID           int    `json:"id" db:"id"`
	Name         string `json:"name" db:"name"`
	Active       bool   `json:"active" db:"active"`
	Description  string `json:"description" db:"description"`
	LastArticle  int64  `json:"last_article" db:"last_article"`
	MessageCount int64  `json:"message_count" db:"message_count"`
	ExpiryDays   int    `json:"expiry_days" db:"expiry_days"`
	MaxArticles  int    `json:"max_articles" db:"max_articles"`
	// NNTP-specific fields
	HighWater int       `json:"high_water" db:"high_water"`
	LowWater  int       `json:"low_water" db:"low_water"`
	Status    string    `json:"status" db:"status"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

type Overview struct {
	ArticleNum int64     `json:"article_num" db:"article_num"`
	Subject    string    `json:"subject" db:"subject"`
	FromHeader string    `json:"from_header" db:"from_header"`
	DateSent   time.Time `json:"date_sent" db:"date_sent"`
	DateString string    `json:"date_string" db:"date_string"`
	MessageID  string    `json:"message_id" db:"message_id"`
	References string    `json:"references" db:"references"`
	Bytes      int       `json:"bytes" db:"bytes"`
	Lines      int       `json:"lines" db:"lines"`
	ReplyCount int       `json:"reply_count" db:"reply_count"`
	Downloaded int       `json:"downloaded" db:"downloaded"` // 0 = not downloaded, 1 = downloaded
	Sanitized  bool      `json:"-" db:"-"`
}

// ForumThread represents a complete thread with root article and replies
type ForumThread struct {
	RootArticle  *Overview   `json:"thread_root"`   // The original post
	Replies      []*Overview `json:"replies"`       // All replies in flat list
	MessageCount int         `json:"message_count"` // Total messages in thread
	LastActivity time.Time   `json:"last_activity"` // Most recent activity
}

// Thread represents a parent/child relationship for threading
type Thread struct {
	ID            int    `json:"id" db:"id"`
	RootArticle   int64  `json:"root_article" db:"root_article"`
	ParentArticle *int64 `json:"parent_article" db:"parent_article"` // Pointer for NULL values
	ChildArticle  int64  `json:"child_article" db:"child_article"`
	Depth         int    `json:"depth" db:"depth"`
	ThreadOrder   int    `json:"thread_order" db:"thread_order"`
}
*/
