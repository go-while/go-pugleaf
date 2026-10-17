package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
)

// DefaultPostQueueMaxAttempts is the PostQueueMaxAttempts used when the config key is missing,
// not a number or <= 0 (migration 0030 inserts the key with this value).
const DefaultPostQueueMaxAttempts = 5

// postQueueLastErrorMax caps the stored last_error text (bytes).
const postQueueLastErrorMax = 1024

// ErrPostQueueEntryUnchanged is returned by DeletePostQueueEntry and RetryPostQueueEntry when
// no row matched: the entry does not exist, is being posted right now (in_processing) or, for
// a retry, was already posted.
var ErrPostQueueEntryUnchanged = errors.New("post queue entry not found, in processing or already posted")

// PostQueueEntry represents a record in the post_queue table
type PostQueueEntry struct {
	ID             int64     `db:"id"`
	NewsgroupID    int64     `db:"newsgroup_id"`
	MessageID      string    `db:"message_id"` // Added in migration 0016
	Created        time.Time `db:"created"`
	PostedToRemote bool      `db:"posted_to_remote"`
	InProcessing   bool      `db:"in_processing"` // Added in migration 0017
	Attempts       int       `db:"attempts"`      // Added in migration 0030: 441 rejections so far
	LastError      string    `db:"last_error"`    // Added in migration 0030: last failure ("" = none)
}

// PostQueueMaxAttempts returns the config key PostQueueMaxAttempts: a row with attempts >= this
// value is parked (not selected for posting). A missing, invalid or <= 0 value reads as
// DefaultPostQueueMaxAttempts.
func (d *Database) PostQueueMaxAttempts() int {
	v, err := d.GetConfigValue(config.CFG_KEY_POSTQUEUE_MAXATTEMPTS)
	if err != nil {
		log.Printf("Database: Failed to read %s, using %d: %v", config.CFG_KEY_POSTQUEUE_MAXATTEMPTS, DefaultPostQueueMaxAttempts, err)
		return DefaultPostQueueMaxAttempts
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return DefaultPostQueueMaxAttempts
	}
	return n
}

// InsertPostQueueEntry inserts a new entry into the post_queue table
// This is called when an article is first queued from the web interface
func (d *Database) InsertPostQueueEntry(newsgroupID int64, messageID string) error {
	query := `
		INSERT INTO post_queue (newsgroup_id, message_id, posted_to_remote, in_processing)
		VALUES (?, ?, 0, 0)
	`
	_, err := RetryableExec(d.mainDB, query, newsgroupID, messageID)
	if err != nil {
		log.Printf("Database: Failed to insert post_queue entry: %v", err)
		return err
	}

	log.Printf("Database: Inserted post_queue entry msgId='%s'", messageID)
	return nil
}

// GetPendingPostQueueEntries retrieves entries that haven't been posted to remote servers, aren't being processed
// and aren't parked (attempts < PostQueueMaxAttempts), and marks them in_processing in the same transaction.
func (d *Database) GetPendingPostQueueEntries(limit int) ([]PostQueueEntry, error) {
	maxAttempts := d.PostQueueMaxAttempts()
	// Select entries that are available for processing
	query := `
		SELECT id, newsgroup_id, message_id, created, posted_to_remote, in_processing, attempts, COALESCE(last_error, '')
		FROM post_queue
		WHERE posted_to_remote = 0 AND in_processing = 0 AND attempts < ?
		ORDER BY created ASC LIMIT ?
	`

	var entries []PostQueueEntry
	// RetryableTransactionExec may run the closure again (SQLITE_BUSY): it starts from empty results every time.
	err := RetryableTransactionExec(d.mainDB, func(tx *sql.Tx) error {
		entries = nil
		var ids []int64

		rows, err := tx.Query(query, maxAttempts, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry PostQueueEntry
			if err := rows.Scan(&entry.ID, &entry.NewsgroupID, &entry.MessageID, &entry.Created, &entry.PostedToRemote, &entry.InProcessing,
				&entry.Attempts, &entry.LastError); err != nil {
				return err
			}
			entries = append(entries, entry)
			ids = append(ids, entry.ID)
		}
		// close the rows before the UPDATE of the same transaction (the deferred Close is then a no-op)
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}

		if len(ids) == 0 {
			return nil
		}

		// Mark all selected entries as in_processing
		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		updateQuery := fmt.Sprintf(`UPDATE post_queue SET in_processing = 1 WHERE id IN (%s)`,
			strings.Join(placeholders, ","))
		if _, err := tx.Exec(updateQuery, args...); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Update the entries to reflect the new state
	for i := range entries {
		entries[i].InProcessing = true
	}
	return entries, nil
}

// MarkPostQueueAsPostedToRemote marks an entry as posted to remote servers and resets in_processing
func (d *Database) MarkPostQueueAsPostedToRemote(id int64) error {
	query := `UPDATE post_queue SET posted_to_remote = 1, in_processing = 0 WHERE id = ?`

	_, err := RetryableExec(d.mainDB, query, id)
	if err != nil {
		log.Printf("Database: Failed to mark post_queue entry %d as posted to remote: %v", id, err)
		return err
	}

	//log.Printf("Database: Marked post_queue entry %d as posted to remote", id)
	return nil
}

// RecordPostQueueFailure returns entry id to pending after a failed posting attempt and stores
// errText as its last_error. countAttempt (every provider rejected the article with 441) also
// increments attempts; a row reaching PostQueueMaxAttempts is parked. Connection and pool
// errors pass countAttempt=false: they say nothing about the article.
func (d *Database) RecordPostQueueFailure(id int64, errText string, countAttempt bool) error {
	if len(errText) > postQueueLastErrorMax {
		errText = strings.ToValidUTF8(errText[:postQueueLastErrorMax], "")
	}
	query := `UPDATE post_queue SET in_processing = 0, last_error = ?,
		attempts = attempts + CASE WHEN ? THEN 1 ELSE 0 END WHERE id = ?`
	if _, err := RetryableExec(d.mainDB, query, errText, countAttempt, id); err != nil {
		log.Printf("Database: Failed to record failure of post_queue entry %d: %v", id, err)
		return err
	}
	return nil
}

// ResetPostQueueProcessing resets the in_processing flag for entries that failed processing
func (d *Database) ResetPostQueueProcessing(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	// Build the placeholders for the IN clause
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`UPDATE post_queue SET in_processing = 0 WHERE id IN (%s)`,
		strings.Join(placeholders, ","))

	_, err := RetryableExec(d.mainDB, query, args...)
	if err != nil {
		log.Printf("Database: Failed to reset in_processing for post_queue entries: %v", err)
		return err
	}

	log.Printf("Database: Reset in_processing flag for %d post_queue entries", len(ids))
	return nil
}

// ResetAllPostQueueProcessing resets all in_processing flags - useful for cleanup on startup
func (d *Database) ResetAllPostQueueProcessing() error {
	query := `UPDATE post_queue SET in_processing = 0 WHERE in_processing = 1`

	result, err := RetryableExec(d.mainDB, query)
	if err != nil {
		log.Printf("Database: Failed to reset all in_processing flags: %v", err)
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		log.Printf("Database: Failed to get rows affected for reset all in_processing: %v", err)
		return err
	}

	if rowsAffected > 0 {
		log.Printf("Database: Reset in_processing flag for %d stale post_queue entries", rowsAffected)
	}
	return nil
}

// PostQueueEntryWithDetails extends PostQueueEntry with additional information for admin display
type PostQueueEntryWithDetails struct {
	PostQueueEntry
	Newsgroup string `db:"newsgroup"`
	Status    string // Computed status based on flags
}

// GetAllPostQueueEntries retrieves all post queue entries for admin display with pagination and filtering.
// statusFilter is "", "pending", "processing", "completed" or "parked" (pending excludes parked rows).
func (d *Database) GetAllPostQueueEntries(limit, offset int, statusFilter, searchTerm string) ([]*PostQueueEntryWithDetails, error) {
	maxAttempts := d.PostQueueMaxAttempts()
	baseQuery := `
		SELECT pq.id, pq.newsgroup_id, COALESCE(pq.message_id, ''), pq.created, pq.posted_to_remote, pq.in_processing,
		       pq.attempts, COALESCE(pq.last_error, ''), ng.name as newsgroup
		FROM post_queue pq
		LEFT JOIN newsgroups ng ON ng.id = pq.newsgroup_id
	`

	var conditions []string
	var args []interface{}

	// Apply status filter
	if statusFilter != "" {
		switch statusFilter {
		case "pending":
			conditions = append(conditions, "pq.posted_to_remote = 0 AND pq.in_processing = 0 AND pq.attempts < ?")
			args = append(args, maxAttempts)
		case "parked":
			conditions = append(conditions, "pq.posted_to_remote = 0 AND pq.in_processing = 0 AND pq.attempts >= ?")
			args = append(args, maxAttempts)
		case "processing":
			conditions = append(conditions, "pq.in_processing = 1")
		case "completed":
			conditions = append(conditions, "pq.posted_to_remote = 1")
		}
	}

	// Apply search filter
	if searchTerm != "" {
		searchCondition := "(ng.name LIKE ? OR pq.message_id LIKE ?)"
		searchParam := "%" + searchTerm + "%"
		conditions = append(conditions, searchCondition)
		args = append(args, searchParam, searchParam)
	}

	// Build WHERE clause
	query := baseQuery
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	// Add ordering and pagination
	query += " ORDER BY pq.created DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
		if offset > 0 {
			query += " OFFSET ?"
			args = append(args, offset)
		}
	}

	rows, err := RetryableQuery(d.mainDB, query, args...)
	if err != nil {
		log.Printf("Database: Failed to get post queue entries: %v", err)
		return nil, err
	}
	defer rows.Close()

	var entries []*PostQueueEntryWithDetails
	for rows.Next() {
		entry := &PostQueueEntryWithDetails{}
		var newsgroup *string

		err := rows.Scan(&entry.ID, &entry.NewsgroupID, &entry.MessageID, &entry.Created,
			&entry.PostedToRemote, &entry.InProcessing,
			&entry.Attempts, &entry.LastError, &newsgroup)
		if err != nil {
			log.Printf("Database: Failed to scan post queue entry: %v", err)
			return nil, err
		}

		// Set optional fields
		if newsgroup != nil {
			entry.Newsgroup = *newsgroup
		}

		// Compute status
		if entry.PostedToRemote {
			entry.Status = "completed"
		} else if entry.InProcessing {
			entry.Status = "processing"
		} else if entry.Attempts >= maxAttempts {
			entry.Status = "parked"
		} else {
			entry.Status = "pending"
		}

		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		log.Printf("Database: Failed to read post queue entries: %v", err)
		return nil, err
	}

	return entries, nil
}

// GetPostQueueStats returns statistics about the post queue for admin display: total, pending,
// parked, processing and completed (pending excludes parked rows).
func (d *Database) GetPostQueueStats() (map[string]int, error) {
	stats := make(map[string]int)
	maxAttempts := d.PostQueueMaxAttempts()

	// Get total count
	var total int
	err := RetryableQueryRowScan(d.mainDB, "SELECT COUNT(*) FROM post_queue", nil, &total)
	if err != nil {
		return nil, err
	}
	stats["total"] = total

	// Get pending count
	var pending int
	err = RetryableQueryRowScan(d.mainDB, "SELECT COUNT(*) FROM post_queue WHERE posted_to_remote = 0 AND in_processing = 0 AND attempts < ?",
		[]interface{}{maxAttempts}, &pending)
	if err != nil {
		return nil, err
	}
	stats["pending"] = pending

	// Get parked count (attempts reached PostQueueMaxAttempts)
	var parked int
	err = RetryableQueryRowScan(d.mainDB, "SELECT COUNT(*) FROM post_queue WHERE posted_to_remote = 0 AND in_processing = 0 AND attempts >= ?",
		[]interface{}{maxAttempts}, &parked)
	if err != nil {
		return nil, err
	}
	stats["parked"] = parked

	// Get processing count
	var processing int
	err = RetryableQueryRowScan(d.mainDB, "SELECT COUNT(*) FROM post_queue WHERE in_processing = 1", nil, &processing)
	if err != nil {
		return nil, err
	}
	stats["processing"] = processing

	// Get completed count
	var completed int
	err = RetryableQueryRowScan(d.mainDB, "SELECT COUNT(*) FROM post_queue WHERE posted_to_remote = 1", nil, &completed)
	if err != nil {
		return nil, err
	}
	stats["completed"] = completed

	return stats, nil
}

// DeletePostQueueEntry deletes a post queue entry by ID unless it is being posted right now
// (in_processing). It returns ErrPostQueueEntryUnchanged when no row was deleted.
func (d *Database) DeletePostQueueEntry(id int64) error {
	query := `DELETE FROM post_queue WHERE id = ? AND in_processing = 0`

	result, err := RetryableExec(d.mainDB, query, id)
	if err != nil {
		log.Printf("Database: Failed to delete post queue entry %d: %v", id, err)
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		log.Printf("Database: Failed to get rows affected for delete of post queue entry %d: %v", id, err)
		return err
	}
	if n == 0 {
		return ErrPostQueueEntryUnchanged
	}

	log.Printf("Database: Deleted post queue entry %d", id)
	return nil
}

// RetryPostQueueEntry resets the attempt counter and last error of an entry that is neither
// posted nor being posted, so a parked entry is selected again. It returns
// ErrPostQueueEntryUnchanged when no row matched.
func (d *Database) RetryPostQueueEntry(id int64) error {
	query := `UPDATE post_queue SET attempts = 0, last_error = NULL
		WHERE id = ? AND posted_to_remote = 0 AND in_processing = 0`

	result, err := RetryableExec(d.mainDB, query, id)
	if err != nil {
		log.Printf("Database: Failed to retry post queue entry %d: %v", id, err)
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		log.Printf("Database: Failed to get rows affected for retry of post queue entry %d: %v", id, err)
		return err
	}
	if n == 0 {
		return ErrPostQueueEntryUnchanged
	}

	log.Printf("Database: Reset post queue entry %d for retry", id)
	return nil
}

// CleanupPostedEntries deletes successfully posted entries from the post_queue table
// If cleanupOlder > 0, only deletes posted entries older than N days
// If cleanupOlder = 0, deletes all posted entries
func (d *Database) CleanupPostedEntries(cleanupPosted bool, cleanupOlder int) (int64, error) {
	if !cleanupPosted && cleanupOlder == 0 {
		log.Printf("ERROR: CleanupPostedEntries called with no action (cleanupPosted=false and cleanupOlder=0)")
		return 0, nil
	}

	var query string
	var args []interface{}

	if cleanupOlder > 0 {
		// Delete posted entries older than N days
		query = `DELETE FROM post_queue WHERE posted_to_remote = 1 AND created < datetime('now', '-' || ? || ' days')`
		args = append(args, cleanupOlder)
	} else {
		// Delete all posted entries
		query = `DELETE FROM post_queue WHERE posted_to_remote = 1`
	}

	result, err := RetryableExec(d.mainDB, query, args...)
	if err != nil {
		log.Printf("Database: Failed to cleanup posted entries: %v", err)
		return 0, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		log.Printf("Database: Failed to get rows affected for cleanup: %v", err)
		return 0, err
	}

	if cleanupOlder > 0 {
		log.Printf("Database: Cleaned up %d posted entries older than %d days from post_queue", rowsAffected, cleanupOlder)
	} else {
		log.Printf("Database: Cleaned up %d posted entries from post_queue", rowsAffected)
	}
	return rowsAffected, nil
}
