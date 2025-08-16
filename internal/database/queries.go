// Package database provides query helpers for go-pugleaf models
package database

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// DateParseAdapter is a function type for parsing date strings
type DateParseAdapter func(string) time.Time

// Global date parser adapter - can be set by the calling package to use their date parser
var GlobalDateParser DateParseAdapter

var testLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05.000Z",
	"2006-01-02 15:04:05.000000",
	"2006-01-02 15:04:05",
}

// parseDateString parses a date string using the adapter if available, otherwise uses basic parsing
func parseDateString(dateStr string) time.Time {
	if GlobalDateParser != nil {
		// Use the adapter (e.g., processor.ParseNNTPDate)
		if parsed := GlobalDateParser(dateStr); !parsed.IsZero() {
			return parsed
		}
		// Fall through to basic parsing if adapter fails
	}

	// Basic fallback parsing
	for _, layout := range testLayouts {
		if parsed, err := time.Parse(layout, dateStr); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// --- Main DB Queries ---

// AddProvider adds a new provider to the main database
const query_AddProvider = `INSERT INTO providers (name, grp, host, port, ssl, username, password, max_conns, enabled, priority, max_art_size)
	          VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (db *Database) AddProvider(provider *models.Provider) error {
	_, err := retryableExec(db.mainDB, query_AddProvider,
		provider.Name, provider.Grp, provider.Host, provider.Port,
		provider.SSL, provider.Username, provider.Password,
		provider.MaxConns, provider.Enabled, provider.Priority,
		provider.MaxArtSize)
	if err != nil {
		return fmt.Errorf("failed to add provider %s: %w", provider.Name, err)
	}
	return nil
}

// DeleteProvider deletes a provider from the main database
func (db *Database) DeleteProvider(id int) error {
	_, err := retryableExec(db.mainDB, `DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete provider %d: %w", id, err)
	}
	return nil
}

const query_SetProvider = `UPDATE providers SET
		grp = ?, host = ?, port = ?, ssl = ?, username = ?, password = ?,
		max_conns = ?, enabled = ?, priority = ?, max_art_size = ?
		WHERE id = ?`

func (db *Database) SetProvider(provider *models.Provider) error {
	_, err := retryableExec(db.mainDB, query_SetProvider,
		provider.Grp, provider.Host, provider.Port,
		provider.SSL, provider.Username, provider.Password,
		provider.MaxConns, provider.Enabled, provider.Priority,
		provider.MaxArtSize, provider.ID)
	if err != nil {
		return fmt.Errorf("failed to update provider %d: %w", provider.ID, err)
	}
	return nil
}

// GetProviders returns all providers
const query_GetProviders = `SELECT id, enabled, priority, name, host, port, ssl, username, password, max_conns, max_art_size, created_at FROM providers order by priority ASC`

func (db *Database) GetProviders() ([]*models.Provider, error) {
	rows, err := retryableQuery(db.mainDB, query_GetProviders)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Provider
	for rows.Next() {
		var p models.Provider
		if err := rows.Scan(&p.ID, &p.Enabled, &p.Priority, &p.Name, &p.Host, &p.Port, &p.SSL, &p.Username, &p.Password, &p.MaxConns, &p.MaxArtSize, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, nil
}

// InsertNewsgroup inserts a new newsgroup
const query_InsertNewsgroup = `INSERT INTO newsgroups (name, description, last_article, message_count, active, expiry_days, max_articles, max_art_size, high_water, low_water, status, hierarchy) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (db *Database) InsertNewsgroup(g *models.Newsgroup) error {
	// Auto-populate hierarchy from newsgroup name if not set
	if g.Hierarchy == "" {
		g.Hierarchy = ExtractHierarchyFromGroupName(g.Name)
	}
	_, err := retryableExec(db.mainDB, query_InsertNewsgroup, g.Name, g.Description, g.LastArticle, g.MessageCount, g.Active, g.ExpiryDays, g.MaxArticles, g.MaxArtSize, g.HighWater, g.LowWater, g.Status, g.Hierarchy)

	// Invalidate hierarchy cache for the affected hierarchy
	if err == nil && db.HierarchyCache != nil {
		db.HierarchyCache.InvalidateHierarchy(g.Hierarchy)
	}

	return err
}

const query_MainDBGetAllNewsgroupsCount = `SELECT COUNT(*) FROM newsgroups`

func (db *Database) MainDBGetAllNewsgroupsCount() int64 {
	var count int64
	err := retryableQueryRowScan(db.mainDB, query_MainDBGetAllNewsgroupsCount, nil, &count)
	if err != nil {
		log.Printf("MainDBGetNewsgroupsCount: Failed to get newsgroups count: %v", err)
		return 0
	}
	return count
}

const query_MainDBGetNewsgroupsActiveCount = `SELECT COUNT(*) FROM newsgroups WHERE active = 1`

func (db *Database) MainDBGetNewsgroupsActiveCount() int64 {
	var count int64
	err := retryableQueryRowScan(db.mainDB, query_MainDBGetNewsgroupsActiveCount, nil, &count)
	if err != nil {
		log.Printf("MainDBGetNewsgroupsActiveCount: Failed to get newsgroups count: %v", err)
		return 0
	}
	return count
}

// MainDBGetAllNewsgroups returns all newsgroups
const query_MainDBGetAllNewsgroups = `SELECT id, name, description, last_article, message_count, active, expiry_days, max_articles, max_art_size, high_water, low_water, status, hierarchy, created_at FROM newsgroups order by name`

func (db *Database) MainDBGetAllNewsgroups() ([]*models.Newsgroup, error) {
	rows, err := retryableQuery(db.mainDB, query_MainDBGetAllNewsgroups)
	if err != nil {
		log.Printf("MainDBGetAllNewsgroups: Failed to query newsgroups: %v", err)
		return nil, err
	}
	defer rows.Close()
	var out []*models.Newsgroup
	for rows.Next() {
		var g models.Newsgroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.LastArticle, &g.MessageCount, &g.Active, &g.ExpiryDays, &g.MaxArticles, &g.MaxArtSize, &g.HighWater, &g.LowWater, &g.Status, &g.Hierarchy, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, nil
}

// MainDBGetNewsgroup returns a newsgroups information from MainDB
const query_MainDBGetNewsgroup = `SELECT id, name, description, last_article, message_count, active, expiry_days, max_articles, max_art_size, high_water, low_water, status, hierarchy, created_at FROM newsgroups WHERE name = ?`

func (db *Database) MainDBGetNewsgroup(newsgroup string) (*models.Newsgroup, error) {
	rows, err := retryableQuery(db.mainDB, query_MainDBGetNewsgroup, newsgroup)
	if err != nil {
		log.Printf("MainDBGetNewsgroup: Failed to query newsgroup '%s': %v", newsgroup, err)
		return nil, err
	}
	defer rows.Close()
	var g models.Newsgroup
	found := false
	for rows.Next() {
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.LastArticle, &g.MessageCount, &g.Active, &g.ExpiryDays, &g.MaxArticles, &g.MaxArtSize, &g.HighWater, &g.LowWater, &g.Status, &g.Hierarchy, &g.CreatedAt); err != nil {
			return nil, err
		}
		found = true
	}
	if !found {
		return nil, sql.ErrNoRows
	}
	return &g, nil
}

// UpdateNewsgroup updates an existing newsgroup
const query_UpdateNewsgroup = `UPDATE newsgroups SET description = ?, last_article = ?, message_count = ?, active = ?, expiry_days = ?, max_articles = ?, high_water = ?, low_water = ?, status = ?, hierarchy = ? WHERE name = ?`

func (db *Database) UpdateNewsgroup(g *models.Newsgroup) error {
	// Auto-populate hierarchy from newsgroup name if not set
	if g.Hierarchy == "" {
		g.Hierarchy = ExtractHierarchyFromGroupName(g.Name)
	}

	_, err := retryableExec(db.mainDB, query_UpdateNewsgroup,
		g.Description, g.LastArticle, g.MessageCount, g.Active, g.ExpiryDays, g.MaxArticles, g.HighWater, g.LowWater, g.Status, g.Hierarchy, g.Name,
	)

	// Invalidate hierarchy cache for the affected hierarchy
	if err == nil && db.HierarchyCache != nil {
		db.HierarchyCache.InvalidateHierarchy(g.Hierarchy)
	}

	return err
}

// UpdateNewsgroupExpiry updates the expiry_days for a newsgroup
const query_UpdateNewsgroupExpiry = `UPDATE newsgroups SET expiry_days = ? WHERE name = ?`

func (db *Database) UpdateNewsgroupExpiry(name string, expiryDays int) error {
	_, err := retryableExec(db.mainDB, query_UpdateNewsgroupExpiry, expiryDays, name)
	return err
}

// UpdateNewsgroupExpiryPrefix updates the expiry_days for a newsgroup
const query_UpdateNewsgroupExpiryPrefix = `UPDATE newsgroups SET expiry_days = ? WHERE name LIKE ? `

func (db *Database) UpdateNewsgroupExpiryPrefix(name string, expiryDays int) error {
	_, err := retryableExec(db.mainDB, query_UpdateNewsgroupExpiryPrefix, expiryDays, name+"%")
	return err
}

// UpdateNewsgroupMaxArticles updates the expiry_days for a newsgroup
const query_UpdateNewsgroupMaxArticles = `UPDATE newsgroups SET max_articles = ? WHERE name = ?`

func (db *Database) UpdateNewsgroupMaxArticles(name string, maxArticles int) error {
	_, err := retryableExec(db.mainDB, query_UpdateNewsgroupMaxArticles, maxArticles, name)
	return err
}

// UpdateNewsgroupMaxArticles updates the expiry_days for a newsgroup
const query_UpdateNewsgroupMaxArticlesPrefix = `UPDATE newsgroups SET max_articles = ? WHERE name LIKE ?`

func (db *Database) UpdateNewsgroupMaxArticlesPrefix(name string, maxArticles int) error {
	_, err := retryableExec(db.mainDB, query_UpdateNewsgroupMaxArticlesPrefix, maxArticles, name+"%")
	return err
}

// UpdateNewsgroupMaxArtSize updates the max_art_size for a newsgroup
const query_UpdateNewsgroupMaxArtSize = `UPDATE newsgroups SET max_art_size = ? WHERE name = ?`

func (db *Database) UpdateNewsgroupMaxArtSize(name string, maxArtSize int) error {
	_, err := retryableExec(db.mainDB, query_UpdateNewsgroupMaxArtSize, maxArtSize, name)
	return err
}

const query_UpdateNewsgroupActive = `UPDATE newsgroups SET active = ? WHERE name = ?`

// UpdateNewsgroupActive updates the active status for a newsgroup
func (db *Database) UpdateNewsgroupActive(name string, active bool) error {
	_, err := retryableExec(db.mainDB, query_UpdateNewsgroupActive, active, name)

	// Update hierarchy cache with new active status instead of invalidating
	if err == nil && db.HierarchyCache != nil {
		db.HierarchyCache.UpdateNewsgroupActiveStatus(name, active)
	}

	return err
}

// BulkUpdateNewsgroupActive updates the active status for multiple newsgroups
func (db *Database) BulkUpdateNewsgroupActive(names []string, active bool) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}

	// Use transaction for atomicity
	tx, err := db.mainDB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Build placeholders for IN clause
	placeholders := make([]string, len(names))
	args := make([]interface{}, len(names)+1)
	args[0] = active

	for i, name := range names {
		placeholders[i] = "?"
		args[i+1] = name
	}

	query := fmt.Sprintf(
		`UPDATE newsgroups SET active = ? WHERE name IN (%s)`,
		strings.Join(placeholders, ","),
	)

	result, err := tx.Exec(query, args...)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// Update hierarchy cache with new active status for all affected newsgroups
	if db.HierarchyCache != nil {
		for _, name := range names {
			db.HierarchyCache.UpdateNewsgroupActiveStatus(name, active)
		}
	}

	return int(rowsAffected), nil
}

// BulkDeleteNewsgroups deletes multiple inactive newsgroups
func (db *Database) BulkDeleteNewsgroups(names []string) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}

	// Use transaction for atomicity
	tx, err := db.mainDB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Build placeholders for IN clause - only delete inactive newsgroups
	placeholders := make([]string, len(names))
	args := make([]interface{}, len(names))

	for i, name := range names {
		placeholders[i] = "?"
		args[i] = name
	}

	query := fmt.Sprintf(
		`DELETE FROM newsgroups WHERE name IN (%s) AND active = 0`,
		strings.Join(placeholders, ","),
	)

	result, err := tx.Exec(query, args...)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// Invalidate hierarchy cache for all affected newsgroups
	if db.HierarchyCache != nil {
		for _, name := range names {
			hierarchy := ExtractHierarchyFromGroupName(name)
			db.HierarchyCache.InvalidateHierarchy(hierarchy)
		}
	}

	return int(rowsAffected), nil
}

// UpdateNewsgroupDescription updates the description for a newsgroup
const query_UpdateNewsgroupDescription = `UPDATE newsgroups SET description = ? WHERE name = ?`

func (db *Database) UpdateNewsgroupDescription(name string, description string) error {
	_, err := retryableExec(db.mainDB, query_UpdateNewsgroupDescription, description, name)
	return err
}

// DeleteNewsgroup deletes a newsgroup from the main database
const query_DeleteNewsgroup = `DELETE FROM newsgroups WHERE name = ? AND active = 0`

func (db *Database) DeleteNewsgroup(name string) error {
	// Get hierarchy before deletion for cache invalidation
	newsgroup, err := db.MainDBGetNewsgroup(name)
	var hierarchy string
	if err == nil {
		hierarchy = newsgroup.Hierarchy
	} else {
		// Fallback: extract hierarchy from name
		hierarchy = ExtractHierarchyFromGroupName(name)
	}

	_, err = retryableExec(db.mainDB, query_DeleteNewsgroup, name)

	// Invalidate hierarchy cache for the affected hierarchy
	if err == nil && db.HierarchyCache != nil {
		db.HierarchyCache.InvalidateHierarchy(hierarchy)
	}

	return err
}

const query_GetThreadsCount = `SELECT COUNT(*) FROM threads`

func (db *Database) GetThreadsCount(groupDBs *GroupDBs) (int64, error) {
	var count int64

	err := retryableQueryRowScan(groupDBs.DB, query_GetThreadsCount, nil, &count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

const query_GetArticlesCount = `SELECT COUNT(*) FROM articles`

func (db *Database) GetArticlesCount(groupDBs *GroupDBs) (int64, error) {
	var count int64

	err := retryableQueryRowScan(groupDBs.DB, query_GetArticlesCount, nil, &count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetArticleCountFromMainDB gets the article count from the main database
// without opening the group database. This is much more efficient than GetArticlesCount.
// The message_count field is kept up-to-date by db_batch.go during article processing.
func (db *Database) GetArticleCountFromMainDB(groupName string) (int64, error) {
	newsgroupInfo, err := db.MainDBGetNewsgroup(groupName)
	if err != nil {
		return 0, err
	}
	return newsgroupInfo.MessageCount, nil
}

// GetLastArticleDate returns the date_sent of the most recent article in the group
// Returns nil if no articles found
const query_GetLastArticleDate = `SELECT MAX(date_sent) FROM articles`

func (db *Database) GetLastArticleDate(groupDBs *GroupDBs) (*time.Time, error) {
	var lastDateStr sql.NullString

	err := retryableQueryRowScan(groupDBs.DB, query_GetLastArticleDate, nil, &lastDateStr)
	if err != nil {
		return nil, fmt.Errorf("failed to get last article date for group %s: %w", groupDBs.Newsgroup, err)
	}

	if !lastDateStr.Valid || lastDateStr.String == "" {
		return nil, nil // No articles found
	}

	// Parse the date string using the adapter
	lastDate := parseDateString(lastDateStr.String)
	if lastDate.IsZero() {
		return nil, fmt.Errorf("failed to parse last article date '%s' for group %s", lastDateStr.String, groupDBs.Newsgroup)
	}

	return &lastDate, nil
}

const query_GetAllArticles = `SELECT article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, reply_count, path, headers_json, body_text, imported_at FROM articles ORDER BY article_num ASC`

func (db *Database) GetAllArticles(groupDBs *GroupDBs) ([]*models.Article, error) {
	log.Printf("GetArticles: group '%s' fetching articles", groupDBs.Newsgroup)

	rows, err := retryableQuery(groupDBs.DB, query_GetAllArticles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Article
	for rows.Next() {
		var a models.Article
		var artnum int64
		if err := rows.Scan(&artnum, &a.MessageID, &a.Subject, &a.FromHeader, &a.DateSent, &a.DateString, &a.References, &a.Bytes, &a.Lines, &a.ReplyCount, &a.Path, &a.HeadersJSON, &a.BodyText, &a.ImportedAt); err != nil {
			return nil, err
		}
		a.ArticleNums = make(map[*string]int64)
		a.ArticleNums[groupDBs.NewsgroupPtr] = artnum
		a.NewsgroupsPtr = append(a.NewsgroupsPtr, groupDBs.NewsgroupPtr)
		out = append(out, &a)
	}
	return out, nil
}

// InsertThread inserts a thread into a group's threads database
const query_InsertThread = `INSERT INTO threads (root_article, parent_article, child_article, depth, thread_order) VALUES (?, ?, ?, ?, ?)`

func (db *Database) InsertThread(groupDBs *GroupDBs, t *models.Thread, a *models.Article) error {
	_, err := retryableExec(groupDBs.DB, query_InsertThread,
		t.RootArticle, t.ParentArticle, t.ChildArticle, t.Depth, t.ThreadOrder,
	)

	return err
}

const query_GetThreads = `SELECT id, root_article, parent_article, child_article, depth, thread_order FROM threads`

func (db *Database) GetThreads(groupDBs *GroupDBs) ([]*models.Thread, error) {
	rows, err := retryableQuery(groupDBs.DB, query_GetThreads)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Thread
	for rows.Next() {
		var t models.Thread
		var parentArticle sql.NullInt64
		if err := rows.Scan(&t.ID, &t.RootArticle, &parentArticle, &t.ChildArticle, &t.Depth, &t.ThreadOrder); err != nil {
			return nil, err
		}
		if parentArticle.Valid {
			t.ParentArticle = &parentArticle.Int64
		}
		out = append(out, &t)
	}
	return out, nil
}

// InsertOverview inserts an overview entry using the articles table (unified schema)
const query_InsertOverview = `INSERT INTO articles (subject, from_header, date_sent, date_string, message_id, "references", bytes, lines, reply_count, downloaded) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
const query_ImportOverview = `INSERT INTO articles (article_num, subject, from_header, date_sent, date_string, message_id, "references", bytes, lines, reply_count, downloaded) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (db *Database) InsertOverview(groupDBs *GroupDBs, o *models.Overview) (int64, error) {
	var res sql.Result
	var err error

	// Format DateSent as UTC string to avoid timezone encoding issues
	dateSentStr := o.DateSent.UTC().Format("2006-01-02 15:04:05")

	if o.ArticleNum == 0 {
		// Auto-increment article_num - don't include it in INSERT
		res, err = retryableExec(groupDBs.DB, query_InsertOverview,
			o.Subject, o.FromHeader, dateSentStr, o.DateString, o.MessageID, o.References, o.Bytes, o.Lines, o.ReplyCount, o.Downloaded,
		)
	} else {
		// Explicit article_num provided (e.g. from ImportOverview)
		res, err = retryableExec(groupDBs.DB, query_ImportOverview,
			o.ArticleNum, o.Subject, o.FromHeader, dateSentStr, o.DateString, o.MessageID, o.References, o.Bytes, o.Lines, o.ReplyCount, o.Downloaded,
		)
	}
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return id, err
}

const query_GetOverviews = `SELECT article_num, subject, from_header, date_sent, date_string, message_id, "references", bytes, lines, reply_count, downloaded FROM articles`

func (db *Database) GetOverviews(groupDBs *GroupDBs) ([]*models.Overview, error) {
	log.Printf("GetOverviews: group '%s' fetching overviews from articles table", groupDBs.Newsgroup)

	rows, err := retryableQuery(groupDBs.DB, query_GetOverviews)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Overview
	for rows.Next() {
		var o models.Overview
		if err := rows.Scan(&o.ArticleNum, &o.Subject, &o.FromHeader, &o.DateSent, &o.DateString, &o.MessageID, &o.References, &o.Bytes, &o.Lines, &o.ReplyCount, &o.Downloaded); err != nil {
			return nil, err
		}
		out = append(out, &o)
	}
	return out, nil
}

/*
// SetOverviewDownloaded sets the downloaded flag for an article in the articles table
func (db *Database) SetOverviewDownloaded(groupDBs *GroupDBs, articleNum int64, downloaded int) error {
	db.Batch.BatchCaptureSetOverviewDownloaded(groupDBs.Newsgroup, articleNum)
	//

		_, err := groupDBs.DB.Exec(
			`UPDATE articles SET downloaded = ? WHERE article_num = ?`,
			downloaded, articleNum,
		)

		return err
	//
	return nil
}
*/

// GetUndownloadedOverviews returns all overview entries from articles table that have not been downloaded
const query_GetUndownloadedOverviews = `SELECT article_num, subject, from_header, date_sent, date_string, message_id, "references", bytes, lines, reply_count, downloaded FROM articles WHERE downloaded = 0 ORDER BY article_num ASC LIMIT ?`

func (db *Database) GetUndownloadedOverviews(groupDBs *GroupDBs, fetchMax int) ([]*models.Overview, error) {
	rows, err := groupDBs.DB.Query(query_GetUndownloadedOverviews, fetchMax)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Overview
	for rows.Next() {
		var o models.Overview
		if err := rows.Scan(&o.ArticleNum, &o.Subject, &o.FromHeader, &o.DateSent, &o.DateString, &o.MessageID, &o.References, &o.Bytes, &o.Lines, &o.ReplyCount, &o.Downloaded); err != nil {
			return nil, err
		}
		out = append(out, &o)
	}
	return out, nil
}

// --- User Queries ---
const query_InsertUser = `INSERT INTO users (username, email, password_hash, display_name) VALUES (?, ?, ?, ?)`

func (db *Database) InsertUser(u *models.User) error {
	_, err := db.mainDB.Exec(query_InsertUser,
		u.Username, u.Email, u.PasswordHash, u.DisplayName,
	)
	return err
}

const query_GetUserByUsername = `SELECT id, username, email, password_hash, display_name, created_at FROM users WHERE username = ?`

func (db *Database) GetUserByUsername(username string) (*models.User, error) {
	row := db.mainDB.QueryRow(query_GetUserByUsername, username)
	var u models.User
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

const query_GetUserByEmail = `SELECT id, username, email, password_hash, display_name, created_at FROM users WHERE email = ?`

func (db *Database) GetUserByEmail(email string) (*models.User, error) {
	row := db.mainDB.QueryRow(query_GetUserByEmail, email)
	var u models.User
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

const query_GetUserByID = `SELECT id, username, email, password_hash, display_name, created_at FROM users WHERE id = ?`

func (db *Database) GetUserByID(id int64) (*models.User, error) {
	row := db.mainDB.QueryRow(query_GetUserByID, id)
	var u models.User
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateUserEmail updates a user's email address
const query_UpdateUserEmail = `UPDATE users SET email = ? WHERE id = ?`

func (db *Database) UpdateUserEmail(userID int64, email string) error {
	_, err := db.mainDB.Exec(query_UpdateUserEmail, email, userID)
	return err
}

// UpdateUserPassword updates a user's password hash
const query_UpdateUserPassword = `UPDATE users SET password_hash = ? WHERE id = ?`

func (db *Database) UpdateUserPassword(userID int64, passwordHash string) error {
	_, err := db.mainDB.Exec(query_UpdateUserPassword, passwordHash, userID)
	return err
}

// --- Session Queries ---
const query_InsertSession = `INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`

func (db *Database) InsertSession(s *models.Session) error {
	_, err := db.mainDB.Exec(query_InsertSession, s.ID, s.UserID, s.CreatedAt, s.ExpiresAt)
	return err
}

const query_GetSession = `SELECT id, user_id, created_at, expires_at FROM sessions WHERE id = ?`

func (db *Database) GetSession(id string) (*models.Session, error) {
	row := db.mainDB.QueryRow(query_GetSession, id)
	var s models.Session
	if err := row.Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.ExpiresAt); err != nil {
		return nil, err
	}
	return &s, nil
}

const query_DeleteSession = `DELETE FROM sessions WHERE id = ?`

func (db *Database) DeleteSession(id string) error {
	_, err := db.mainDB.Exec(query_DeleteSession, id)
	return err
}

// --- UserPermission Queries ---
const query_InsertUserPermission = `INSERT INTO user_permissions (user_id, permission, granted_at) VALUES (?, ?, ?)`

func (db *Database) InsertUserPermission(up *models.UserPermission) error {
	_, err := db.mainDB.Exec(query_InsertUserPermission, up.UserID, up.Permission, up.GrantedAt)
	return err
}

const query_GetUserPermissions = `SELECT id, user_id, permission, granted_at FROM user_permissions WHERE user_id = ?`

func (db *Database) GetUserPermissions(userID int) ([]*models.UserPermission, error) {
	rows, err := db.mainDB.Query(query_GetUserPermissions, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.UserPermission
	for rows.Next() {
		var up models.UserPermission
		if err := rows.Scan(&up.ID, &up.UserID, &up.Permission, &up.GrantedAt); err != nil {
			return nil, err
		}
		out = append(out, &up)
	}
	return out, nil
}

// GetArticleByNum retrieves an article by its article number
const query_GetArticleByNum = `SELECT article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, reply_count, path, headers_json, body_text, imported_at FROM articles WHERE article_num = ?`

func (db *Database) GetArticleByNum(groupDBs *GroupDBs, articleNum int64) (*models.Article, error) {
	// Try cache first
	if db.ArticleCache != nil {
		if article, found := db.ArticleCache.Get(groupDBs.Newsgroup, articleNum); found {
			return article, nil
		}
	}
	row := groupDBs.DB.QueryRow(query_GetArticleByNum, articleNum)
	var a models.Article
	var artnum int64
	if err := row.Scan(&artnum, &a.MessageID, &a.Subject, &a.FromHeader, &a.DateSent, &a.DateString, &a.References, &a.Bytes, &a.Lines, &a.ReplyCount, &a.Path, &a.HeadersJSON, &a.BodyText, &a.ImportedAt); err != nil {
		return nil, err
	}
	a.ArticleNums = make(map[*string]int64)
	a.ArticleNums[groupDBs.NewsgroupPtr] = artnum
	a.NewsgroupsPtr = append(a.NewsgroupsPtr, groupDBs.NewsgroupPtr)

	// Cache the result
	if db.ArticleCache != nil {
		db.ArticleCache.Put(groupDBs.Newsgroup, articleNum, &a)
	}
	return &a, nil
}

// GetArticleByMessageID retrieves an article by its message ID
const query_GetArticleByMessageID = `SELECT article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, reply_count, path, headers_json, body_text, imported_at FROM articles WHERE message_id = ?`

func (db *Database) GetArticleByMessageID(groupDBs *GroupDBs, messageID string) (*models.Article, error) {
	//log.Printf("GetArticleByMessageID: group '%s' fetching article with message ID '%s'", groupDBs.Newsgroup, messageID)

	row := groupDBs.DB.QueryRow(query_GetArticleByMessageID, messageID)
	var a models.Article
	var artnum int64
	if err := row.Scan(&artnum, &a.MessageID, &a.Subject, &a.FromHeader, &a.DateSent, &a.DateString, &a.References, &a.Bytes, &a.Lines, &a.ReplyCount, &a.Path, &a.HeadersJSON, &a.BodyText, &a.ImportedAt); err != nil {
		return nil, err
	}
	a.ArticleNums = make(map[*string]int64)
	a.ArticleNums[groupDBs.NewsgroupPtr] = artnum
	a.NewsgroupsPtr = append(a.NewsgroupsPtr, groupDBs.NewsgroupPtr)

	// Cache the result
	if db.ArticleCache != nil {
		db.ArticleCache.Put(groupDBs.Newsgroup, a.ArticleNums[groupDBs.NewsgroupPtr], &a)
	}
	return &a, nil
}

// UpdateReplyCount updates the reply count for an article
const query_UpdateReplyCount = `UPDATE articles SET reply_count = ? WHERE message_id = ?`

func (db *Database) UpdateReplyCount(groupDBs *GroupDBs, messageID string, replyCount int) error {
	_, err := retryableExec(groupDBs.DB, query_UpdateReplyCount, replyCount, messageID)
	return err
}

// IncrementReplyCount increments the reply count for an article
const query_IncrementReplyCount = `UPDATE articles SET reply_count = reply_count + 1 WHERE message_id = ?`

func (db *Database) IncrementReplyCount(groupDBs *GroupDBs, messageID string) error {
	_, err := retryableExec(groupDBs.DB,
		query_IncrementReplyCount,
		messageID,
	)
	return err
}

// GetReplyCount gets the current reply count for an article
const query_GetReplyCount = `SELECT reply_count FROM articles WHERE message_id = ?`

func (db *Database) GetReplyCount(groupDBs *GroupDBs, messageID string) (int, error) {
	row := groupDBs.DB.QueryRow(
		query_GetReplyCount,
		messageID,
	)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateOverviewReplyCount updates the reply count for an article in the articles table
const query_UpdateOverviewReplyCount = `UPDATE articles SET reply_count = ? WHERE message_id = ?`

func (db *Database) UpdateOverviewReplyCount(groupDBs *GroupDBs, messageID string, replyCount int) error {
	_, err := retryableExec(groupDBs.DB,
		query_UpdateOverviewReplyCount,
		replyCount, messageID,
	)
	return err
}

// IncrementOverviewReplyCount increments the reply count for an article in the articles table
const query_IncrementOverviewReplyCount = `UPDATE articles SET reply_count = reply_count + 1 WHERE message_id = ?`

func (db *Database) IncrementOverviewReplyCount(groupDBs *GroupDBs, messageID string) error {

	_, err := retryableExec(groupDBs.DB,
		query_IncrementOverviewReplyCount,
		messageID,
	)
	return err
}

// GetNewsgroupByName gets a newsgroup by its name
const query_GetActiveNewsgroupByName = `SELECT id, name, active FROM newsgroups WHERE name = ? and active = 1 LIMIT 1`

func (db *Database) GetActiveNewsgroupByName(name string) (*models.Newsgroup, error) {
	row := db.mainDB.QueryRow(query_GetActiveNewsgroupByName, name)
	var g models.Newsgroup
	if err := row.Scan(&g.ID, &g.Name, &g.Active); err != nil {
		return nil, err
	}
	return &g, nil
}

// UpsertNewsgroupDescription inserts or updates a newsgroup description
const query_UpsertNewsgroupDescription = `INSERT INTO newsgroups (name, description, last_article, message_count, active)
VALUES (?, ?, 0, 0, 0)
ON CONFLICT(name) DO UPDATE SET description = excluded.description`

func (db *Database) UpsertNewsgroupDescription(name, description string) error {
	_, err := db.mainDB.Exec(query_UpsertNewsgroupDescription, name, description)
	return err
}

// GetActiveNewsgroups returns only newsgroups that are active
const query_GetActiveNewsgroups = `SELECT id, name, description, last_article, message_count, active, created_at FROM newsgroups WHERE active = 1 ORDER BY name`

func (db *Database) GetActiveNewsgroups() ([]*models.Newsgroup, error) {
	rows, err := db.mainDB.Query(query_GetActiveNewsgroups)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Newsgroup
	for rows.Next() {
		var g models.Newsgroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.LastArticle, &g.MessageCount, &g.Active, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, nil
}

// GetActiveNewsgroups returns only newsgroups that are active
const query_GetActiveNewsgroupsWithMessages = `SELECT id, name, description, last_article, message_count, active, created_at FROM newsgroups WHERE message_count > 0 AND active = 1 ORDER BY name`

func (db *Database) GetActiveNewsgroupsWithMessages() ([]*models.Newsgroup, error) {
	rows, err := db.mainDB.Query(query_GetActiveNewsgroupsWithMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Newsgroup
	for rows.Next() {
		var g models.Newsgroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.LastArticle, &g.MessageCount, &g.Active, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, nil
}

// GetNewsgroupsPaginated returns newsgroups with pagination
const query_GetNewsgroupsPaginated1 = `SELECT COUNT(*) FROM newsgroups WHERE active = 1 AND message_count > 0`
const query_GetNewsgroupsPaginated2 = `SELECT id, name, description, last_article, message_count, active, created_at
FROM newsgroups WHERE active = 1 AND message_count > 0
ORDER BY name
LIMIT ? OFFSET ?`

func (db *Database) GetNewsgroupsPaginated(page, pageSize int) ([]*models.Newsgroup, int, error) {
	offset := (page - 1) * pageSize
	// Get total count
	var totalCount int
	err := db.mainDB.QueryRow(query_GetNewsgroupsPaginated1).Scan(&totalCount)
	if err != nil {
		return nil, 0, err
	}

	// Get paginated results
	rows, err := db.mainDB.Query(query_GetNewsgroupsPaginated2, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*models.Newsgroup
	for rows.Next() {
		var g models.Newsgroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.LastArticle, &g.MessageCount, &g.Active, &g.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, &g)
	}

	return out, totalCount, nil
}

// GetNewsgroupsPaginatedAdmin returns ALL newsgroups with pagination
const query_GetNewsgroupsPaginatedAdmin1 = `SELECT COUNT(*) FROM newsgroups`
const query_GetNewsgroupsPaginatedAdmin2 = `SELECT id, name, description, last_article, message_count, active, expiry_days, max_articles, max_art_size, created_at
FROM newsgroups
ORDER BY name
LIMIT ? OFFSET ?`

func (db *Database) GetNewsgroupsPaginatedAdmin(page, pageSize int) ([]*models.Newsgroup, int, error) {
	offset := (page - 1) * pageSize
	// Get total count
	var totalCount int
	err := db.mainDB.QueryRow(query_GetNewsgroupsPaginatedAdmin1).Scan(&totalCount)
	if err != nil {
		return nil, 0, err
	}

	// Get paginated results
	rows, err := db.mainDB.Query(query_GetNewsgroupsPaginatedAdmin2, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*models.Newsgroup
	for rows.Next() {
		var g models.Newsgroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.LastArticle, &g.MessageCount, &g.Active, &g.ExpiryDays, &g.MaxArticles, &g.MaxArtSize, &g.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, &g)
	}

	return out, totalCount, nil
}

// GetOverviewsPaginated returns overview entries from articles table with cursor-based pagination
// Uses article_num as cursor for better performance with large datasets
const query_GetOverviewsPaginated1 = `SELECT article_num, subject, from_header, date_sent, date_string, message_id, "references", bytes, lines, reply_count, downloaded, spam, hide
		         FROM articles
		         WHERE hide = 0 AND article_num < ?
		         ORDER BY article_num DESC
		         LIMIT ?`
const query_GetOverviewsPaginated2 = `SELECT article_num, subject, from_header, date_sent, date_string, message_id, "references", bytes, lines, reply_count, downloaded, spam, hide
		         FROM articles
		         WHERE hide = 0
		         ORDER BY article_num DESC
		         LIMIT ?`
const query_GetOverviewsPaginated3 = `SELECT article_num FROM articles
			 WHERE hide = 0 AND article_num < ?
			 ORDER BY article_num DESC
			 LIMIT 1`

func (db *Database) GetOverviewsPaginated(groupDBs *GroupDBs, lastArticleNum int64, pageSize int) ([]*models.Overview, int, bool, error) {

	// Get total count from newsgroups table in main database (much faster than COUNT(*) on articles)
	var totalCount int
	newsgroupInfo, err := db.MainDBGetNewsgroup(groupDBs.Newsgroup)
	if err != nil || newsgroupInfo == nil {
		totalCount = -1 // Fallback if newsgroup not found
	} else {
		totalCount = int(newsgroupInfo.MessageCount)
	}

	// Build query with cursor-based pagination
	var rows *sql.Rows
	if lastArticleNum > 0 {
		// Continue from last seen article (descending order by article_num)
		args := []interface{}{lastArticleNum, pageSize}
		rows, err = groupDBs.DB.Query(query_GetOverviewsPaginated1, args...)
		if err != nil {
			return nil, 0, false, err
		}
		defer rows.Close()
	} else {
		// First page
		args := []interface{}{pageSize}
		rows, err = groupDBs.DB.Query(query_GetOverviewsPaginated2, args...)
		if err != nil {
			return nil, 0, false, err
		}
		defer rows.Close()
	}

	var out []*models.Overview
	for rows.Next() {
		var o models.Overview
		if err := rows.Scan(&o.ArticleNum, &o.Subject, &o.FromHeader, &o.DateSent, &o.DateString, &o.MessageID, &o.References, &o.Bytes, &o.Lines, &o.ReplyCount, &o.Downloaded, &o.Spam, &o.Hide); err != nil {
			return nil, 0, false, err
		}
		out = append(out, &o)
	}

	// Check if there are more pages by trying to get one more record
	hasMore := false
	if len(out) == pageSize {
		var nextArticleNum int64
		err := groupDBs.DB.QueryRow(query_GetOverviewsPaginated3,
			out[len(out)-1].ArticleNum).Scan(&nextArticleNum)
		if err == nil {
			hasMore = true
		}
	}

	return out, totalCount, hasMore, nil
}

// --- Section Queries ---

// InsertSection inserts a new section into the main database
const query_InsertSection = `INSERT INTO sections (name, display_name, description, show_in_header, enable_local_spool, sort_order) VALUES (?, ?, ?, ?, ?, ?)`

func (db *Database) InsertSection(s *models.Section) error {
	_, err := db.mainDB.Exec(
		query_InsertSection,
		s.Name, s.DisplayName, s.Description, s.ShowInHeader, s.EnableLocalSpool, s.SortOrder,
	)
	return err
}

// GetSections returns all sections
const query_GetSections = `SELECT id, name, display_name, description, show_in_header, enable_local_spool, sort_order, created_at FROM sections ORDER BY sort_order, name`

func (db *Database) GetSections() ([]*models.Section, error) {
	rows, err := db.mainDB.Query(query_GetSections)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Section
	for rows.Next() {
		var s models.Section
		if err := rows.Scan(&s.ID, &s.Name, &s.DisplayName, &s.Description, &s.ShowInHeader, &s.EnableLocalSpool, &s.SortOrder, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, nil
}

// GetSectionByName returns a section by its name
const query_GetSectionByName = `SELECT id, name, display_name, description, show_in_header, enable_local_spool, sort_order, created_at FROM sections WHERE name = ?`

func (db *Database) GetSectionByName(name string) (*models.Section, error) {
	row := db.mainDB.QueryRow(query_GetSectionByName, name)
	var s models.Section
	if err := row.Scan(&s.ID, &s.Name, &s.DisplayName, &s.Description, &s.ShowInHeader, &s.EnableLocalSpool, &s.SortOrder, &s.CreatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// GetHeaderSections returns sections that should be shown in the header
const query_GetHeaderSections = `SELECT id, name, display_name, description, show_in_header, enable_local_spool, sort_order, created_at FROM sections WHERE show_in_header = 1 ORDER BY sort_order, name`

func (db *Database) GetHeaderSections() ([]*models.Section, error) {
	rows, err := db.mainDB.Query(query_GetHeaderSections)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Section
	for rows.Next() {
		var s models.Section
		if err := rows.Scan(&s.ID, &s.Name, &s.DisplayName, &s.Description, &s.ShowInHeader, &s.EnableLocalSpool, &s.SortOrder, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, nil
}

// --- Section Group Queries ---

// InsertSectionGroup inserts a new section group mapping
const query_InsertSectionGroup = `INSERT INTO section_groups (section_id, newsgroup_name, group_description, sort_order, is_category_header) VALUES (?, ?, ?, ?, ?)`

func (db *Database) InsertSectionGroup(sg *models.SectionGroup) error {
	_, err := db.mainDB.Exec(
		query_InsertSectionGroup,
		sg.SectionID, sg.NewsgroupName, sg.GroupDescription, sg.SortOrder, sg.IsCategoryHeader,
	)
	return err
}

// GetSectionGroups returns all groups for a specific section
const query_GetSectionGroups = `SELECT id, section_id, newsgroup_name, group_description, sort_order, is_category_header, created_at FROM section_groups WHERE section_id = ? ORDER BY sort_order, newsgroup_name`

func (db *Database) GetSectionGroups(sectionID int) ([]*models.SectionGroup, error) {
	rows, err := db.mainDB.Query(query_GetSectionGroups, sectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.SectionGroup
	for rows.Next() {
		var sg models.SectionGroup
		if err := rows.Scan(&sg.ID, &sg.SectionID, &sg.NewsgroupName, &sg.GroupDescription, &sg.SortOrder, &sg.IsCategoryHeader, &sg.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &sg)
	}
	return out, nil
}

// GetSectionGroupsByName returns all section groups for a newsgroup name
const query_GetSectionGroupsByName = `SELECT id, section_id, newsgroup_name, group_description, sort_order, is_category_header, created_at FROM section_groups WHERE newsgroup_name = ?`

func (db *Database) GetSectionGroupsByName(newsgroupName string) ([]*models.SectionGroup, error) {
	rows, err := db.mainDB.Query(query_GetSectionGroupsByName, newsgroupName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.SectionGroup
	for rows.Next() {
		var sg models.SectionGroup
		if err := rows.Scan(&sg.ID, &sg.SectionID, &sg.NewsgroupName, &sg.GroupDescription, &sg.SortOrder, &sg.IsCategoryHeader, &sg.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &sg)
	}
	return out, nil
}

const query_GetProviderByName = `SELECT id, name, grp, host, port, ssl, username, password, max_conns, enabled, priority, max_art_size
	          FROM providers WHERE name = ? ORDER by id ASC LIMIT 1`

func (db *Database) GetProviderByName(name string) (*models.Provider, error) {
	row := db.mainDB.QueryRow(query_GetProviderByName, name)
	var provider models.Provider
	err := row.Scan(&provider.ID, &provider.Name, &provider.Grp, &provider.Host, &provider.Port,
		&provider.SSL, &provider.Username, &provider.Password, &provider.MaxConns, &provider.Enabled, &provider.Priority,
		&provider.MaxArtSize)
	if err == sql.ErrNoRows {
		return nil, nil // Provider not found
	} else if err != nil {
		return nil, fmt.Errorf("failed to get provider %s: %w", name, err)
	}

	return &provider, nil
}

const query_GetProviderByID = `SELECT id, name, grp, host, port, ssl, username, password, max_conns, enabled, priority, max_art_size
	          FROM providers WHERE id = ? LIMIT 1`

func (db *Database) GetProviderByID(id int) (*models.Provider, error) {
	row := db.mainDB.QueryRow(query_GetProviderByID, id)
	var provider models.Provider
	err := row.Scan(&provider.ID, &provider.Name, &provider.Grp, &provider.Host, &provider.Port,
		&provider.SSL, &provider.Username, &provider.Password, &provider.MaxConns, &provider.Enabled, &provider.Priority,
		&provider.MaxArtSize)
	if err == sql.ErrNoRows {
		return nil, nil // Provider not found
	} else if err != nil {
		return nil, fmt.Errorf("failed to get provider %d: %w", id, err)
	}

	return &provider, nil
}

const query_IsNewsGroupInSections = `SELECT 1 FROM section_groups WHERE newsgroup_name = ? LIMIT 1`

func (db *Database) IsNewsGroupInSections(name string) bool {
	if db.SectionsCache.IsInSections(name) {
		return true
	}
	row := db.GetMainDB().QueryRow(
		query_IsNewsGroupInSections,
		name,
	)
	var exists int
	if err := row.Scan(&exists); err != nil {
		return false
	}
	result := exists > 0
	if result {
		db.SectionsCache.AddGroupToSectionsCache(name)
	}
	return result
}

// GetTopGroupsByMessageCount returns the top N groups ordered by message count
const query_GetTopGroupsByMessageCount = `
		SELECT name, description, message_count, last_article, created_at, updated_at
		FROM newsgroups
		ORDER BY message_count DESC
		LIMIT ?`

func (db *Database) GetTopGroupsByMessageCount(limit int) ([]*models.Newsgroup, error) {
	rows, err := db.mainDB.Query(query_GetTopGroupsByMessageCount, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []*models.Newsgroup
	for rows.Next() {
		group := &models.Newsgroup{}
		err := rows.Scan(
			&group.Name,
			&group.Description,
			&group.MessageCount,
			&group.LastArticle,
			&group.CreatedAt,
			&group.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}

	return groups, rows.Err()
}

// GetTotalThreadsCount returns the total number of threads across all groups
func (db *Database) GetTotalThreadsCount() (int64, error) {
	// Get all newsgroups
	groups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		return 0, err
	}

	var totalThreads int64
	for _, group := range groups {
		// Get group database
		groupDBs, err := db.GetGroupDBs(group.Name)
		if err != nil {
			continue // Skip groups that don't have databases yet
		}

		// Count threads in this group
		threadCount, err := db.GetThreadsCount(groupDBs)
		if err != nil {
			groupDBs.Return(db)
			continue // Skip groups with errors
		}

		totalThreads += threadCount
		groupDBs.Return(db)
	}

	return totalThreads, nil
}

// SearchNewsgroups searches for newsgroups by name pattern with pagination
const query_SearchNewsgroups = `
		SELECT name, description, last_article, message_count, active, expiry_days, max_articles, max_art_size, created_at, updated_at
		FROM newsgroups
		WHERE active = 1 AND (name LIKE ? COLLATE NOCASE
		OR description LIKE ? COLLATE NOCASE)
		ORDER BY message_count DESC, name ASC
		LIMIT ? OFFSET ?
	` // SearchNewsgroups searches for newsgroups by name pattern with pagination

const query_SearchNewsgroupsAdmin = `
		SELECT name, description, last_article, message_count, active, expiry_days, max_articles, max_art_size, created_at, updated_at
		FROM newsgroups
		WHERE (name LIKE ? COLLATE NOCASE
		OR description LIKE ? COLLATE NOCASE)
		ORDER BY message_count DESC, name ASC
		LIMIT ? OFFSET ?
	`

func (db *Database) SearchNewsgroups(searchTerm string, limit, offset int, admin bool) ([]*models.Newsgroup, error) {
	var query string
	switch admin {
	case true:
		query = query_SearchNewsgroups
	default:
		query = query_SearchNewsgroupsAdmin

	}
	// Use LIKE for pattern matching, case-insensitive
	searchPattern := "%" + searchTerm + "%"
	rows, err := db.mainDB.Query(query, searchPattern, searchPattern, limit, offset)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []*models.Newsgroup
	for rows.Next() {
		g := &models.Newsgroup{}
		err := rows.Scan(
			&g.Name, &g.Description, &g.LastArticle, &g.MessageCount,
			&g.Active, &g.ExpiryDays, &g.MaxArticles, &g.MaxArtSize, &g.CreatedAt, &g.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}

	return groups, rows.Err()
}

// CountSearchNewsgroups counts total newsgroups matching search pattern
const query_CountSearchNewsgroups = `
		SELECT COUNT(*)
		FROM newsgroups
		WHERE active = 1 AND (name LIKE ? COLLATE NOCASE
		OR description LIKE ? COLLATE NOCASE)
	`

func (db *Database) CountSearchNewsgroups(searchTerm string) (int, error) {
	// Use LIKE for pattern matching, case-insensitive
	searchPattern := "%" + searchTerm + "%"

	var count int
	err := db.mainDB.QueryRow(query_CountSearchNewsgroups, searchPattern, searchPattern).Scan(&count)

	return count, err
}

// GetAllUsers retrieves all users from the database
const query_GetAllUsers = `SELECT id, username, email, display_name, password_hash, created_at FROM users ORDER BY username`

func (db *Database) GetAllUsers() ([]*models.User, error) {
	rows, err := db.mainDB.Query(query_GetAllUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*models.User
	for rows.Next() {
		var user models.User
		if err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.DisplayName, &user.PasswordHash, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, &user)
	}
	return users, nil
}

// GetOverviewsRange returns overview entries from articles table for a specific range of article numbers
const query_GetOverviewsRange = `SELECT article_num, subject, from_header, date_sent, date_string, message_id, "references", bytes, lines, reply_count, downloaded
		 FROM articles
		 WHERE article_num >= ? AND article_num <= ?
		 ORDER BY article_num ASC`

func (db *Database) GetOverviewsRange(groupDBs *GroupDBs, startNum, endNum int64) ([]*models.Overview, error) {
	if startNum > endNum {
		return nil, fmt.Errorf("start number %d is greater than end number %d", startNum, endNum)
	}

	// Limit range to prevent excessive queries
	if endNum-startNum > 1000 {
		endNum = startNum + 1000
	}

	rows, err := groupDBs.DB.Query(query_GetOverviewsRange, startNum, endNum)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.Overview

	for rows.Next() {
		var o models.Overview
		if err := rows.Scan(&o.ArticleNum, &o.Subject, &o.FromHeader, &o.DateSent, &o.DateString, &o.MessageID, &o.References, &o.Bytes, &o.Lines, &o.ReplyCount, &o.Downloaded); err != nil {
			return nil, err
		}
		out = append(out, &o)
	}

	return out, nil
}

// GetOverviewByMessageID retrieves an overview entry from articles table by message ID
const query_GetOverviewByMessageID = `
		SELECT article_num, subject, from_header, date_sent, date_string,
			   message_id, "references", bytes, lines, reply_count, downloaded
		FROM articles
		WHERE message_id = ? LIMIT 1
	`

func (db *Database) GetOverviewByMessageID(groupDBs *GroupDBs, messageID string) (*models.Overview, error) {
	overview := &models.Overview{}
	err := groupDBs.DB.QueryRow(query_GetOverviewByMessageID, messageID).Scan(
		&overview.ArticleNum, &overview.Subject, &overview.FromHeader,
		&overview.DateSent, &overview.DateString, &overview.MessageID,
		&overview.References, &overview.Bytes, &overview.Lines,
		&overview.ReplyCount, &overview.Downloaded,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get overview for message ID %s: %w", messageID, err)
	}

	return overview, nil
}

// GetHeaderFieldRange returns specific header field values for a range of articles
const query_GetHeaderFieldRange1 = `SELECT article_num, subject FROM articles WHERE article_num >= ? AND article_num <= ? ORDER BY article_num ASC`
const query_GetHeaderFieldRange2 = `SELECT article_num, from_header FROM articles WHERE article_num >= ? AND article_num <= ? ORDER BY article_num ASC`
const query_GetHeaderFieldRange3 = `SELECT article_num, date_string FROM articles WHERE article_num >= ? AND article_num <= ? ORDER BY article_num ASC`
const query_GetHeaderFieldRange4 = `SELECT article_num, message_id FROM articles WHERE article_num >= ? AND article_num <= ? ORDER BY article_num ASC`
const query_GetHeaderFieldRange5 = `SELECT article_num, "references" FROM articles WHERE article_num >= ? AND article_num <= ? ORDER BY article_num ASC`
const query_GetHeaderFieldRange6 = `SELECT article_num, bytes FROM articles WHERE article_num >= ? AND article_num <= ? ORDER BY article_num ASC`
const query_GetHeaderFieldRange7 = `SELECT article_num, lines FROM articles WHERE article_num >= ? AND article_num <= ? ORDER BY article_num ASC`

func (db *Database) GetHeaderFieldRange(groupDBs *GroupDBs, field string, startNum, endNum int64) (map[int64]string, error) {
	if startNum > endNum {
		return nil, fmt.Errorf("start number %d is greater than end number %d", startNum, endNum)
	}

	// Limit range to prevent excessive queries
	if endNum-startNum > 1000 {
		endNum = startNum + 1000
	}

	var query string
	var dbToQuery *sql.DB

	switch strings.ToLower(field) {
	case "subject":
		query = query_GetHeaderFieldRange1
		dbToQuery = groupDBs.DB
	case "from":
		query = query_GetHeaderFieldRange2
		dbToQuery = groupDBs.DB
	case "date":
		query = query_GetHeaderFieldRange3
		dbToQuery = groupDBs.DB
	case "message-id":
		query = query_GetHeaderFieldRange4
		dbToQuery = groupDBs.DB
	case "references":
		query = query_GetHeaderFieldRange5
		dbToQuery = groupDBs.DB
	case "bytes":
		query = query_GetHeaderFieldRange6
		dbToQuery = groupDBs.DB
	case "lines":
		query = query_GetHeaderFieldRange7
		dbToQuery = groupDBs.DB
	default:
		// For other headers, try to get from the full article headers
		// For now, return empty result for unsupported headers
		return make(map[int64]string), nil
	}

	rows, err := dbToQuery.Query(query, startNum, endNum)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64]string)
	for rows.Next() {
		var articleNum int64
		if strings.ToLower(field) == "bytes" || strings.ToLower(field) == "lines" {
			var value int
			if err := rows.Scan(&articleNum, &value); err != nil {
				return nil, err
			}
			result[articleNum] = fmt.Sprintf("%d", value)
		} else {
			var value string
			if err := rows.Scan(&articleNum, &value); err != nil {
				return nil, err
			}
			result[articleNum] = value
		}
	}
	return result, nil
}

// UpdateNewsgroupWatermarks updates the high and low water marks for a newsgroup
const query_UpdateNewsgroupWatermarks = `UPDATE newsgroups SET high_water = ?, low_water = ? WHERE name = ?`

func (db *Database) UpdateNewsgroupWatermarks(name string, highWater, lowWater int) error {
	_, err := db.mainDB.Exec(query_UpdateNewsgroupWatermarks, highWater, lowWater, name)
	return err
}

// UpdateNewsgroupStatus updates the NNTP status for a newsgroup
const query_UpdateNewsgroupStatus = `UPDATE newsgroups SET status = ? WHERE name = ?`

func (db *Database) UpdateNewsgroupStatus(name string, status string) error {
	_, err := db.mainDB.Exec(
		query_UpdateNewsgroupStatus,
		status, name,
	)
	return err
}

// GetNewsgroupsByPattern gets newsgroups matching a SQL LIKE pattern
const query_GetNewsgroupsByPattern = `SELECT id, name, description, last_article, message_count, active, expiry_days, max_articles, max_art_size, high_water, low_water, status, created_at FROM newsgroups WHERE name LIKE ? ORDER BY name`

func (db *Database) GetNewsgroupsByPattern(pattern string) ([]*models.Newsgroup, error) {
	rows, err := db.mainDB.Query(query_GetNewsgroupsByPattern, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.Newsgroup
	for rows.Next() {
		var g models.Newsgroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.LastArticle, &g.MessageCount, &g.Active, &g.ExpiryDays, &g.MaxArticles, &g.MaxArtSize, &g.HighWater, &g.LowWater, &g.Status, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, nil
}

// GetNewsgroupsByPrefix gets newsgroups with names starting with the given prefix
func (db *Database) GetNewsgroupsByPrefix(prefix string) ([]*models.Newsgroup, error) {
	return db.GetNewsgroupsByPattern(prefix + "%")
}

// GetNewsgroupsByExactPrefix gets newsgroups that match exactly the prefix (no dots after prefix)
const query_GetNewsgroupsByExactPrefix = `
		SELECT id, name, description, last_article, message_count, active, expiry_days, max_articles, max_art_size, high_water, low_water, status, created_at, updated_at
		FROM newsgroups
		WHERE active = 1 AND name = ? OR (name LIKE ? AND name NOT LIKE ?)
		ORDER BY name
	`

func (db *Database) GetNewsgroupsByExactPrefix(prefix string) ([]*models.Newsgroup, error) {
	rows, err := db.mainDB.Query(query_GetNewsgroupsByExactPrefix, prefix, prefix+".%", prefix+".%.%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.Newsgroup
	for rows.Next() {
		var g models.Newsgroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.LastArticle, &g.MessageCount, &g.Active, &g.ExpiryDays, &g.MaxArticles, &g.MaxArtSize, &g.HighWater, &g.LowWater, &g.Status, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, nil
}

// GetHierarchySubLevels gets immediate sub-hierarchy names and their group counts efficiently with pagination
func (db *Database) GetHierarchySubLevels(prefix string, page int, pageSize int) (map[string]int, int, error) {
	// Use cache if available, otherwise fall back to direct query
	if db.HierarchyCache != nil {
		return db.HierarchyCache.GetHierarchySubLevels(db, prefix, page, pageSize)
	}
	return db.getHierarchySubLevelsDirect(prefix, page, pageSize)
}

// getHierarchySubLevelsDirect is the original uncached implementation
const query_getHierarchySubLevelsDirect1 = `
		SELECT COUNT(DISTINCT SUBSTR(name, ?, INSTR(SUBSTR(name, ?), '.') - 1))
		FROM newsgroups
		WHERE active = 1 AND name LIKE ? AND name != ? AND INSTR(SUBSTR(name, ?), '.') > 0
	`
const query_getHierarchySubLevelsDirect2 = `
		SELECT
			SUBSTR(name, ?, INSTR(SUBSTR(name, ?), '.') - 1) as sub_hierarchy,
			COUNT(*) as group_count
		FROM newsgroups
		WHERE active = 1 AND name LIKE ? AND name != ? AND INSTR(SUBSTR(name, ?), '.') > 0
		GROUP BY sub_hierarchy
		ORDER BY sub_hierarchy
		LIMIT ? OFFSET ?
	`

func (db *Database) getHierarchySubLevelsDirect(prefix string, page int, pageSize int) (map[string]int, int, error) {
	// First get total count of sub-hierarchies
	var totalCount int
	err := db.mainDB.QueryRow(query_getHierarchySubLevelsDirect1, len(prefix)+2, len(prefix)+2, prefix+".%", prefix, len(prefix)+2).Scan(&totalCount)

	if err != nil {
		return nil, totalCount, err
	}

	// Get paginated sub-hierarchy names and their counts
	offset := (page - 1) * pageSize
	rows, err := db.mainDB.Query(query_getHierarchySubLevelsDirect2, len(prefix)+2, len(prefix)+2, prefix+".%", prefix, len(prefix)+2, pageSize, offset)

	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var subHierarchy string
		var count int
		if err := rows.Scan(&subHierarchy, &count); err != nil {
			return nil, 0, err
		}
		result[subHierarchy] = count
	}

	return result, totalCount, nil
}

var empty []*models.Newsgroup

// GetDirectGroupsAtLevel gets newsgroups that are direct children of the given prefix with pagination
const query_GetDirectGroupsAtLevel = `
		SELECT COUNT(*)
		FROM newsgroups
		WHERE active = 1 AND name LIKE ? AND name NOT LIKE ? AND active = 1
	`

func (db *Database) GetDirectGroupsAtLevel(prefix string, sortBy string, page int, pageSize int) ([]*models.Newsgroup, int, error) {
	// Use cache if available, otherwise fall back to direct query
	if db.HierarchyCache != nil {
		return db.HierarchyCache.GetDirectGroupsAtLevel(db, prefix, sortBy, page, pageSize)
	}
	return db.getDirectGroupsAtLevelDirect(prefix, sortBy, page, pageSize)
}

// getDirectGroupsAtLevelDirect is the original uncached implementation
func (db *Database) getDirectGroupsAtLevelDirect(prefix string, sortBy string, page int, pageSize int) ([]*models.Newsgroup, int, error) {
	// First get total count
	var totalCount int
	err := db.mainDB.QueryRow(query_GetDirectGroupsAtLevel, prefix+".%", prefix+".%.%").Scan(&totalCount)

	if err != nil {
		return nil, 0, err
	}
	if totalCount > pageSize {
		// return nothing and just link to to the flatview
		return empty, totalCount, nil
	}
	var orderBy string
	if sortBy == "name" {
		orderBy = "name ASC"
	} else {
		orderBy = "updated_at DESC"
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT id, name, description, last_article, message_count, active, expiry_days, max_articles, max_art_size, high_water, low_water, status, created_at, updated_at
		FROM newsgroups
		WHERE active = 1 AND name LIKE ? AND name NOT LIKE ? AND active = 1
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, orderBy)

	rows, err := db.mainDB.Query(query, prefix+".%", prefix+".%.%", pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*models.Newsgroup
	for rows.Next() {
		var g models.Newsgroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.LastArticle, &g.MessageCount, &g.Active, &g.ExpiryDays, &g.MaxArticles, &g.MaxArtSize, &g.HighWater, &g.LowWater, &g.Status, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, &g)
	}
	return out, totalCount, nil
}

// HIERARCHY FUNCTIONS

// ExtractHierarchyFromGroupName extracts the top-level hierarchy from a newsgroup name
func ExtractHierarchyFromGroupName(groupName string) string {
	parts := strings.Split(groupName, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// GetAllHierarchies returns all hierarchies
const query_GetAllHierarchies = `SELECT id, name, description, group_count, last_updated, created_at FROM hierarchies ORDER BY name`

func (db *Database) GetAllHierarchies() ([]*models.Hierarchy, error) {
	rows, err := db.mainDB.Query(query_GetAllHierarchies)
	if err != nil {
		log.Printf("GetAllHierarchies: Failed to query hierarchies: %v", err)
		return nil, err
	}
	defer rows.Close()

	var hierarchies []*models.Hierarchy
	for rows.Next() {
		hierarchy := &models.Hierarchy{}
		var description sql.NullString
		err := rows.Scan(&hierarchy.ID, &hierarchy.Name, &description,
			&hierarchy.GroupCount, &hierarchy.LastUpdated, &hierarchy.CreatedAt)
		if err != nil {
			log.Printf("GetAllHierarchies: Failed to scan hierarchy: %v", err)
			continue
		}
		if description.Valid {
			hierarchy.Description = description.String
		}
		hierarchies = append(hierarchies, hierarchy)
	}
	return hierarchies, nil
}

// GetHierarchiesPaginated returns hierarchies with pagination and optional sorting
func (db *Database) GetHierarchiesPaginated(page, pageSize int, sortBy string) ([]*models.Hierarchy, int, error) {
	// Use cache if available, otherwise fall back to direct query
	if db.HierarchyCache != nil {
		return db.HierarchyCache.GetHierarchiesPaginated(db, page, pageSize, sortBy)
	}
	return db.getHierarchiesPaginatedDirect(page, pageSize, sortBy)
}

// getHierarchiesPaginatedDirect is the original uncached implementation
const query_getHierarchiesPaginatedDirect1 = `SELECT COUNT(DISTINCT h.id)
	                          FROM hierarchies h
	                          INNER JOIN newsgroups n ON n.hierarchy = h.name
	                          WHERE h.group_count > 0 AND n.message_count > 0 AND n.active = 1`

func (db *Database) getHierarchiesPaginatedDirect(page, pageSize int, sortBy string) ([]*models.Hierarchy, int, error) {
	// First get total count of hierarchies that have active newsgroups with messages
	// Using optimized query with hierarchy column instead of LIKE pattern matching
	var totalCount int
	err := db.mainDB.QueryRow(query_getHierarchiesPaginatedDirect1).Scan(&totalCount)
	if err != nil {
		return nil, 0, err
	}

	// Calculate offset
	offset := (page - 1) * pageSize

	// Determine sort order based on parameter
	var orderBy string
	switch sortBy {
	case "newest":
		orderBy = "h.last_updated DESC"
	case "groups":
		orderBy = "h.group_count DESC"
	default: // "name" or any other value defaults to alphabetical
		orderBy = "h.name ASC"
	}

	// Get paginated hierarchies that have active newsgroups with messages
	// Using optimized query with hierarchy column (O(1) join vs O(n) LIKE)
	query := `SELECT DISTINCT h.id, h.name, h.description, h.group_count, h.last_updated, h.created_at
			  FROM hierarchies h
			  INNER JOIN newsgroups n ON n.hierarchy = h.name
			  WHERE h.group_count > 0 AND n.message_count > 0 AND n.active = 1
			  ORDER BY ` + orderBy + `
			  LIMIT ? OFFSET ?`

	rows, err := db.mainDB.Query(query, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var hierarchies []*models.Hierarchy
	for rows.Next() {
		hierarchy := &models.Hierarchy{}
		var description sql.NullString
		err := rows.Scan(&hierarchy.ID, &hierarchy.Name, &description,
			&hierarchy.GroupCount, &hierarchy.LastUpdated, &hierarchy.CreatedAt)
		if err != nil {
			log.Printf("GetHierarchiesPaginated: Failed to scan hierarchy: %v", err)
			continue
		}
		if description.Valid {
			hierarchy.Description = description.String
		}
		hierarchies = append(hierarchies, hierarchy)
	}

	return hierarchies, totalCount, nil
}

// UpdateHierarchiesLastUpdated updates each hierarchy's last_updated field to match
// the highest updated_at value from its child newsgroups
const query_UpdateHierarchiesLastUpdated = `UPDATE hierarchies
			  SET last_updated = (
				  SELECT MAX(n.updated_at)
				  FROM newsgroups n
				  WHERE n.hierarchy = hierarchies.name
				  AND n.active = 1
				  AND n.message_count > 0
			  )
			  WHERE EXISTS (
				  SELECT 1
				  FROM newsgroups n
				  WHERE n.hierarchy = hierarchies.name
				  AND n.active = 1
				  AND n.message_count > 0
			  )`

func (db *Database) UpdateHierarchiesLastUpdated() error {
	log.Printf("UpdateHierarchiesLastUpdated: Starting hierarchy last_updated synchronization...")

	// Update all hierarchies' last_updated field based on their child newsgroups' max updated_at
	result, err := db.mainDB.Exec(query_UpdateHierarchiesLastUpdated)
	if err != nil {
		log.Printf("UpdateHierarchiesLastUpdated: Failed to update hierarchies: %v", err)
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		log.Printf("UpdateHierarchiesLastUpdated: Failed to get rows affected: %v", err)
		return err
	}

	log.Printf("UpdateHierarchiesLastUpdated: Successfully updated %d hierarchies", rowsAffected)
	return nil
}

// UpdateHierarchyCounts updates group counts for all hierarchies
const query_UpdateHierarchyCounts1 = `UPDATE newsgroups
		SET hierarchy = CASE
			WHEN name LIKE '%.%' THEN SUBSTR(name, 1, INSTR(name, '.') - 1)
			ELSE name
		END
		WHERE hierarchy IS NULL`
const query_UpdateHierarchyCounts2 = `
		INSERT OR REPLACE INTO hierarchies (name, group_count, last_updated, created_at)
		SELECT
			hierarchy,
			COUNT(*) as group_count,
			CURRENT_TIMESTAMP as last_updated,
			COALESCE((SELECT created_at FROM hierarchies WHERE name = newsgroups.hierarchy), CURRENT_TIMESTAMP) as created_at
		FROM newsgroups
		WHERE hierarchy IS NOT NULL
		AND hierarchy != ''
		AND active = 1
		GROUP BY hierarchy`
const query_UpdateHierarchyCounts3 = `SELECT COUNT(*) FROM hierarchies WHERE group_count > 0`

func (db *Database) UpdateHierarchyCounts() error {
	log.Printf("UpdateHierarchyCounts: Starting optimized hierarchy sync...")

	// First, ensure all newsgroups have their hierarchy column populated
	_, err := db.mainDB.Exec(query_UpdateHierarchyCounts1)
	if err != nil {
		log.Printf("UpdateHierarchyCounts: Failed to populate hierarchy column: %v", err)
		return err
	}

	// Use SQL to efficiently count and update hierarchies
	// This is much faster than loading 42k records into memory
	_, err = db.mainDB.Exec(query_UpdateHierarchyCounts2)
	if err != nil {
		log.Printf("UpdateHierarchyCounts: Failed to update hierarchy counts: %v", err)
		return err
	}

	// Get count of updated hierarchies for logging
	var hierarchyCount int
	err = db.mainDB.QueryRow(query_UpdateHierarchyCounts3).Scan(&hierarchyCount)
	if err == nil {
		log.Printf("UpdateHierarchyCounts: Successfully updated %d hierarchies using optimized SQL", hierarchyCount)
	}

	log.Printf("UpdateHierarchyCounts: Optimized hierarchy sync completed")
	return nil
}

// GetNewsgroupsByHierarchy returns newsgroups belonging to a specific hierarchy with sorting
const query_GetNewsgroupsByHierarchy1 = `SELECT COUNT(*) FROM newsgroups WHERE name LIKE ? AND active = 1`
const query_GetNewsgroupsByHierarchy2 = `SELECT COUNT(*) FROM newsgroups WHERE hierarchy = ? AND active = 1`

func (db *Database) GetNewsgroupsByHierarchy(hierarchy string, page, pageSize int, sortBy string) ([]*models.Newsgroup, int, error) {
	// Check if this is a sub-hierarchy path (contains dots)
	var countQuery string
	var queryArgs []interface{}

	if strings.Contains(hierarchy, ".") {
		// For sub-hierarchies like "gmane.comp", find groups that start with "gmane.comp."
		pattern := hierarchy + ".%"
		countQuery = query_GetNewsgroupsByHierarchy1
		queryArgs = append(queryArgs, pattern)
	} else {
		// For top-level hierarchies like "gmane", use the hierarchy column
		countQuery = query_GetNewsgroupsByHierarchy2
		queryArgs = append(queryArgs, hierarchy)
	}

	// First get total count
	var totalCount int
	err := db.mainDB.QueryRow(countQuery, queryArgs...).Scan(&totalCount)
	if err != nil {
		return nil, 0, err
	}

	// Calculate offset
	offset := (page - 1) * pageSize

	// Determine sort order based on parameter
	var orderBy string
	switch sortBy {

	case "activity":
		orderBy = "updated_at DESC" // Sort by actual last activity
	default: // "name" or any other value defaults to alphabetical
		orderBy = "name ASC"
	}

	// Build the main query with the same WHERE condition
	var whereClause string
	var mainQueryArgs []interface{}

	if strings.Contains(hierarchy, ".") {
		// For sub-hierarchies, use LIKE pattern
		pattern := hierarchy + ".%"
		whereClause = "WHERE name LIKE ? AND active = 1"
		mainQueryArgs = append(mainQueryArgs, pattern)
	} else {
		// For top-level hierarchies, use hierarchy column
		whereClause = "WHERE hierarchy = ? AND active = 1"
		mainQueryArgs = append(mainQueryArgs, hierarchy)
	}

	// Get paginated newsgroups
	query := `SELECT id, name, description, active, message_count, last_article,
			  expiry_days, max_articles, max_art_size, hierarchy, created_at, updated_at
			  FROM newsgroups
			  ` + whereClause + `
			  ORDER BY ` + orderBy + `
			  LIMIT ? OFFSET ?`

	// Add pagination parameters
	mainQueryArgs = append(mainQueryArgs, pageSize, offset)

	rows, err := db.mainDB.Query(query, mainQueryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var newsgroups []*models.Newsgroup
	for rows.Next() {
		newsgroup := &models.Newsgroup{}
		err := rows.Scan(&newsgroup.ID, &newsgroup.Name, &newsgroup.Description,
			&newsgroup.Active, &newsgroup.MessageCount, &newsgroup.LastArticle,
			&newsgroup.ExpiryDays, &newsgroup.MaxArticles, &newsgroup.MaxArtSize, &newsgroup.Hierarchy, &newsgroup.CreatedAt, &newsgroup.UpdatedAt)
		if err != nil {
			log.Printf("GetNewsgroupsByHierarchy: Failed to scan newsgroup: %v", err)
			continue
		}
		newsgroups = append(newsgroups, newsgroup)
	}

	return newsgroups, totalCount, nil
}

// DeleteUser deletes a user and all associated data
func (db *Database) DeleteUser(userID int64) error {
	// Start transaction to ensure all deletions happen atomically
	tx, err := db.mainDB.Begin()
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete user sessions
	_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("failed to delete user sessions: %w", err)
	}

	// Delete user permissions
	_, err = tx.Exec(`DELETE FROM user_permissions WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("failed to delete user permissions: %w", err)
	}

	// Delete associated NNTP sessions
	_, err = tx.Exec(`DELETE FROM nntp_sessions WHERE user_id IN (SELECT id FROM nntp_users WHERE web_user_id = ?)`, userID)
	if err != nil {
		return fmt.Errorf("failed to delete NNTP sessions: %w", err)
	}

	// Delete associated NNTP users
	_, err = tx.Exec(`DELETE FROM nntp_users WHERE web_user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("failed to delete NNTP users: %w", err)
	}

	// Finally delete the user
	result, err := tx.Exec(`DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	// Check if user was actually deleted
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check deletion result: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("user with ID %d not found", userID)
	}

	// Commit transaction
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// ResetAllNewsgroupData resets all newsgroup counters and flushes all articles, threads, overview, and cache tables
// WARNING: This will permanently delete ALL articles, threads, and overview data from ALL newsgroups!
func (db *Database) ResetAllNewsgroupData() error {
	log.Printf("ResetAllNewsgroupData: Starting complete reset of all newsgroup data...")

	// Get all newsgroups from main database
	newsgroups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		return fmt.Errorf("failed to get newsgroups: %w", err)
	}

	log.Printf("ResetAllNewsgroupData: Found %d newsgroups to reset", len(newsgroups))

	var resetCount int
	var errorCount int

	// Reset each newsgroup
	for _, newsgroup := range newsgroups {
		log.Printf("ResetAllNewsgroupData: Resetting newsgroup '%s'...", newsgroup.Name)

		// Reset the group database tables
		err := db.ResetNewsgroupData(newsgroup.Name)
		if err != nil {
			log.Printf("ResetAllNewsgroupData: Error resetting newsgroup '%s': %v", newsgroup.Name, err)
			errorCount++
			continue
		}

		// Reset counters in main database
		err = db.ResetNewsgroupCounters(newsgroup.Name)
		if err != nil {
			log.Printf("ResetAllNewsgroupData: Error resetting counters for newsgroup '%s': %v", newsgroup.Name, err)
			errorCount++
			continue
		}

		resetCount++
		log.Printf("ResetAllNewsgroupData: Successfully reset newsgroup '%s'", newsgroup.Name)
	}

	if errorCount > 0 {
		log.Printf("ResetAllNewsgroupData: Completed with %d successful resets and %d errors", resetCount, errorCount)
		return fmt.Errorf("reset completed with %d errors out of %d newsgroups", errorCount, len(newsgroups))
	}

	log.Printf("ResetAllNewsgroupData: Successfully reset all %d newsgroups", resetCount)
	return nil
}

// ResetNewsgroupData resets articles, threads, overview, and cache tables for a specific newsgroup
func (db *Database) ResetNewsgroupData(newsgroupName string) error {
	log.Printf("ResetNewsgroupData: Resetting data for newsgroup '%s'", newsgroupName)

	// Get the group database connection
	groupDBs, err := db.GetGroupDBs(newsgroupName)
	if err != nil {
		// If group database doesn't exist yet, nothing to reset
		log.Printf("ResetNewsgroupData: No database found for newsgroup '%s', skipping", newsgroupName)
		return nil
	}
	defer groupDBs.Return(db)

	// Begin transaction for atomic reset
	tx, err := groupDBs.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to start transaction for newsgroup '%s': %w", newsgroupName, err)
	}
	defer tx.Rollback()

	// Get counts before deletion for logging
	var articleCount, threadCount, cacheCount, treeCount, statsCount int64

	tx.QueryRow("SELECT COUNT(*) FROM articles").Scan(&articleCount)
	tx.QueryRow("SELECT COUNT(*) FROM threads").Scan(&threadCount)
	tx.QueryRow("SELECT COUNT(*) FROM thread_cache").Scan(&cacheCount)
	tx.QueryRow("SELECT COUNT(*) FROM cached_trees").Scan(&treeCount)
	tx.QueryRow("SELECT COUNT(*) FROM tree_stats").Scan(&statsCount)

	log.Printf("ResetNewsgroupData: Newsgroup '%s' before reset - Articles: %d, Threads: %d, Cache: %d, Trees: %d, Stats: %d",
		newsgroupName, articleCount, threadCount, cacheCount, treeCount, statsCount)

	// Delete all data from tables in reverse dependency order
	// 1. Delete tree stats (depends on thread_cache)
	_, err = tx.Exec("DELETE FROM tree_stats")
	if err != nil {
		return fmt.Errorf("failed to delete tree_stats for newsgroup '%s': %w", newsgroupName, err)
	}

	// 2. Delete cached trees
	_, err = tx.Exec("DELETE FROM cached_trees")
	if err != nil {
		return fmt.Errorf("failed to delete cached_trees for newsgroup '%s': %w", newsgroupName, err)
	}

	// 3. Delete thread cache
	_, err = tx.Exec("DELETE FROM thread_cache")
	if err != nil {
		return fmt.Errorf("failed to delete thread_cache for newsgroup '%s': %w", newsgroupName, err)
	}

	// 4. Delete threads
	_, err = tx.Exec("DELETE FROM threads")
	if err != nil {
		return fmt.Errorf("failed to delete threads for newsgroup '%s': %w", newsgroupName, err)
	}

	// 5. Delete articles (main data table)
	_, err = tx.Exec("DELETE FROM articles")
	if err != nil {
		return fmt.Errorf("failed to delete articles for newsgroup '%s': %w", newsgroupName, err)
	}

	// Reset auto-increment counters to start fresh
	_, err = tx.Exec("DELETE FROM sqlite_sequence WHERE name IN ('articles', 'threads', 'cached_trees')")
	if err != nil {
		// This is not critical, continue without failing
		log.Printf("ResetNewsgroupData: Warning - could not reset auto-increment for newsgroup '%s': %v", newsgroupName, err)
	}

	// Commit the transaction
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("failed to commit reset transaction for newsgroup '%s': %w", newsgroupName, err)
	}

	log.Printf("ResetNewsgroupData: Successfully cleared all data for newsgroup '%s'", newsgroupName)
	return nil
}

// ResetNewsgroupCounters resets the message counters and water marks in the main database for a newsgroup
const query_ResetNewsgroupCounters = `UPDATE newsgroups SET
			message_count = 0,
			last_article = 0,
			high_water = 0,
			low_water = 1,
			updated_at = 0
		WHERE name = ?`

func (db *Database) ResetNewsgroupCounters(newsgroupName string) error {
	log.Printf("ResetNewsgroupCounters: Resetting counters for newsgroup '%s'", newsgroupName)

	// Reset all counters to 0 and water marks to default values
	_, err := retryableExec(db.mainDB, query_ResetNewsgroupCounters, newsgroupName)

	if err != nil {
		return fmt.Errorf("failed to reset counters for newsgroup '%s': %w", newsgroupName, err)
	}

	log.Printf("ResetNewsgroupCounters: Successfully reset counters for newsgroup '%s'", newsgroupName)
	return nil
}

// --- Site News Queries ---

// GetAllSiteNews returns all site news entries ordered by date_published DESC
const query_GetAllSiteNews = `SELECT id, subject, content, date_published, is_visible, created_at, updated_at
			  FROM site_news ORDER BY date_published DESC`

func (db *Database) GetAllSiteNews() ([]*models.SiteNews, error) {
	rows, err := retryableQuery(db.mainDB, query_GetAllSiteNews)
	if err != nil {
		return nil, fmt.Errorf("failed to query all site news: %w", err)
	}
	defer rows.Close()

	var news []*models.SiteNews
	for rows.Next() {
		var item models.SiteNews
		var isVisibleInt int

		err := rows.Scan(&item.ID, &item.Subject, &item.Content, &item.DatePublished,
			&isVisibleInt, &item.CreatedAt, &item.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan site news row: %w", err)
		}

		item.IsVisible = isVisibleInt == 1
		news = append(news, &item)
	}

	return news, nil
}

// GetVisibleSiteNews returns only visible site news entries ordered by date_published DESC
const query_GetVisibleSiteNews = `SELECT id, subject, content, date_published, is_visible, created_at, updated_at
			  FROM site_news WHERE is_visible = 1 ORDER BY date_published DESC`

func (db *Database) GetVisibleSiteNews() ([]*models.SiteNews, error) {
	rows, err := retryableQuery(db.mainDB, query_GetVisibleSiteNews)
	if err != nil {
		return nil, fmt.Errorf("failed to query visible site news: %w", err)
	}
	defer rows.Close()

	var news []*models.SiteNews
	for rows.Next() {
		var item models.SiteNews
		var isVisibleInt int

		err := rows.Scan(&item.ID, &item.Subject, &item.Content, &item.DatePublished,
			&isVisibleInt, &item.CreatedAt, &item.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan site news row: %w", err)
		}

		item.IsVisible = isVisibleInt == 1
		news = append(news, &item)
	}

	return news, nil
}

// GetSiteNewsByID returns a site news entry by ID
const query_GetSiteNewsByID = `SELECT id, subject, content, date_published, is_visible, created_at, updated_at
			  FROM site_news WHERE id = ?`

func (db *Database) GetSiteNewsByID(id int) (*models.SiteNews, error) {
	var item models.SiteNews
	var isVisibleInt int
	err := retryableQueryRowScan(db.mainDB, query_GetSiteNewsByID, []interface{}{id},
		&item.ID, &item.Subject, &item.Content, &item.DatePublished,
		&isVisibleInt, &item.CreatedAt, &item.UpdatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("site news with ID %d not found", id)
		}
		return nil, fmt.Errorf("failed to get site news by ID %d: %w", id, err)
	}

	item.IsVisible = isVisibleInt == 1
	return &item, nil
}

// CreateSiteNews creates a new site news entry
const query_CreateSiteNews = `INSERT INTO site_news (subject, content, date_published, is_visible)
			  VALUES (?, ?, ?, ?)`

func (db *Database) CreateSiteNews(news *models.SiteNews) error {
	isVisibleInt := 0
	if news.IsVisible {
		isVisibleInt = 1
	}

	result, err := retryableExec(db.mainDB, query_CreateSiteNews, news.Subject, news.Content,
		news.DatePublished, isVisibleInt)
	if err != nil {
		return fmt.Errorf("failed to create site news: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert ID for site news: %w", err)
	}

	news.ID = int(id)
	return nil
}

// UpdateSiteNews updates an existing site news entry
const query_UpdateSiteNews = `UPDATE site_news SET subject = ?, content = ?, date_published = ?,
			  is_visible = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`

func (db *Database) UpdateSiteNews(news *models.SiteNews) error {
	isVisibleInt := 0
	if news.IsVisible {
		isVisibleInt = 1
	}

	_, err := retryableExec(db.mainDB, query_UpdateSiteNews, news.Subject, news.Content,
		news.DatePublished, isVisibleInt, news.ID)
	if err != nil {
		return fmt.Errorf("failed to update site news ID %d: %w", news.ID, err)
	}

	return nil
}

// DeleteSiteNews deletes a site news entry
const query_DeleteSiteNews = `DELETE FROM site_news WHERE id = ?`

func (db *Database) DeleteSiteNews(id int) error {
	_, err := retryableExec(db.mainDB, query_DeleteSiteNews, id)
	if err != nil {
		return fmt.Errorf("failed to delete site news ID %d: %w", id, err)
	}
	return nil
}

// ToggleSiteNewsVisibility toggles the visibility of a site news entry
const query_ToggleSiteNewsVisibility = `UPDATE site_news SET is_visible = (1 - is_visible) WHERE id = ?`

func (db *Database) ToggleSiteNewsVisibility(id int) error {
	_, err := retryableExec(db.mainDB, query_ToggleSiteNewsVisibility, id)
	if err != nil {
		return fmt.Errorf("failed to toggle visibility for site news ID %d: %w", id, err)
	}

	return nil
}

// GetSpamArticles gets articles with spam flags for admin page with pagination
const query_GetSpamArticles1 = `SELECT COUNT(*) FROM spam`
const query_GetSpamArticles2 = `
		SELECT s.newsgroup_id, s.article_num, n.name as newsgroup_name
		FROM spam s
		JOIN newsgroups n ON s.newsgroup_id = n.id
		ORDER BY s.id DESC
		LIMIT ? OFFSET ?`

func (db *Database) GetSpamArticles(offset, limit int) ([]*models.Overview, []string, int, error) {
	// First get total count of spam articles
	var totalCount int
	err := db.mainDB.QueryRow(query_GetSpamArticles1).Scan(&totalCount)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to get spam count: %w", err)
	}

	// Get spam articles with pagination using the spam table
	rows, err := db.mainDB.Query(query_GetSpamArticles2, limit, offset)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to query spam articles: %w", err)
	}
	defer rows.Close()

	var spamArticles []*models.Overview
	var groupNames []string

	for rows.Next() {
		var newsgroupID, articleNum int
		var newsgroupName string

		if err := rows.Scan(&newsgroupID, &articleNum, &newsgroupName); err != nil {
			return nil, nil, 0, fmt.Errorf("failed to scan spam article row: %w", err)
		}

		// Get article details from the specific newsgroup database
		groupDBs, err := db.GetGroupDBs(newsgroupName)
		if err != nil {
			// Log error but continue with next article
			log.Printf("Failed to get group database for %s: %v", newsgroupName, err)
			continue
		}

		// Use existing function to get article overview
		overview, err := db.GetOverviewByArticleNum(groupDBs, int64(articleNum))
		groupDBs.Return(db)

		if err != nil {
			// Log error but continue with next article - article might have been deleted
			log.Printf("Failed to get overview for article %d in group %s: %v", articleNum, newsgroupName, err)
			continue
		}

		spamArticles = append(spamArticles, overview)
		groupNames = append(groupNames, newsgroupName)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, 0, fmt.Errorf("error iterating spam articles: %w", err)
	}

	return spamArticles, groupNames, totalCount, nil
}
