package database

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// PostQueueEntry represents a record in the post_queue table
type PostQueueEntry struct {
	ID             int64     `db:"id"`
	NewsgroupID    int64     `db:"newsgroup_id"`
	MessageID      string    `db:"message_id"` // Added in migration 0016
	Created        time.Time `db:"created"`
	PostedToRemote bool      `db:"posted_to_remote"`
	InProcessing   bool      `db:"in_processing"` // Added in migration 0017
}

// InsertPostQueueEntry inserts a new entry into the post_queue table
// This is called when an article is first queued from the web interface
func (d *Database) InsertPostQueueEntry(newsgroupID int64, messageID string) error {
	query := `
		INSERT INTO post_queue (newsgroup_id, message_id, posted_to_remote, in_processing)
		VALUES (?, ?, 0, 0)
	`
	_, err := d.mainDB.Exec(query, newsgroupID, messageID)
	if err != nil {
		log.Printf("Database: Failed to insert post_queue entry: %v", err)
		return err
	}

	log.Printf("Database: Inserted post_queue entry msgId='%s'", messageID)
	return nil
}

// GetPendingPostQueueEntries retrieves entries that haven't been posted to remote servers and aren't being processed
func (d *Database) GetPendingPostQueueEntries(limit int) ([]PostQueueEntry, error) {
	// Start a transaction to atomically select and mark as in_processing
	tx, err := d.mainDB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Select entries that are available for processing
	query := `
		SELECT id, newsgroup_id, message_id, created, posted_to_remote, in_processing
		FROM post_queue
		WHERE posted_to_remote = 0 AND in_processing = 0
		ORDER BY created ASC LIMIT ?
	`

	rows, err := tx.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []PostQueueEntry
	var ids []int64
	for rows.Next() {
		var entry PostQueueEntry
		err := rows.Scan(&entry.ID, &entry.NewsgroupID, &entry.MessageID, &entry.Created, &entry.PostedToRemote, &entry.InProcessing)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
		ids = append(ids, entry.ID)
	}

	// Mark all selected entries as in_processing
	if len(ids) > 0 {
		// Build the placeholders for the IN clause
		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}

		updateQuery := fmt.Sprintf(`UPDATE post_queue SET in_processing = 1 WHERE id IN (%s)`,
			strings.Join(placeholders, ","))

		_, err = tx.Exec(updateQuery, args...)
		if err != nil {
			return nil, err
		}

		// Update the entries to reflect the new state
		for i := range entries {
			entries[i].InProcessing = true
		}
	}

	// Commit the transaction
	if err = tx.Commit(); err != nil {
		return nil, err
	}

	return entries, nil
}

// MarkPostQueueAsPostedToRemote marks an entry as posted to remote servers and resets in_processing
func (d *Database) MarkPostQueueAsPostedToRemote(id int64) error {
	query := `UPDATE post_queue SET posted_to_remote = 1, in_processing = 0 WHERE id = ?`

	_, err := d.mainDB.Exec(query, id)
	if err != nil {
		log.Printf("Database: Failed to mark post_queue entry %d as posted to remote: %v", id, err)
		return err
	}

	//log.Printf("Database: Marked post_queue entry %d as posted to remote", id)
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

	_, err := d.mainDB.Exec(query, args...)
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

	result, err := d.mainDB.Exec(query)
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

// GetAllPostQueueEntries retrieves all post queue entries for admin display with pagination and filtering
func (d *Database) GetAllPostQueueEntries(limit, offset int, statusFilter, searchTerm string) ([]*PostQueueEntryWithDetails, error) {
	baseQuery := `
		SELECT pq.id, pq.newsgroup_id, pq.message_id, pq.created, pq.posted_to_remote, pq.in_processing,
		       ng.name as newsgroup
		FROM post_queue pq
		LEFT JOIN newsgroups ng ON ng.id = pq.newsgroup_id
	`

	var conditions []string
	var args []interface{}

	// Apply status filter
	if statusFilter != "" {
		switch statusFilter {
		case "pending":
			conditions = append(conditions, "pq.posted_to_remote = 0 AND pq.in_processing = 0")
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

	rows, err := d.mainDB.Query(query, args...)
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
			&newsgroup)
		if err != nil {
			log.Printf("Database: Failed to scan post queue entry: %v", err)
			continue
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
		} else {
			entry.Status = "pending"
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// GetPostQueueStats returns statistics about the post queue for admin display
func (d *Database) GetPostQueueStats() (map[string]int, error) {
	stats := make(map[string]int)

	// Get total count
	var total int
	err := d.mainDB.QueryRow("SELECT COUNT(*) FROM post_queue").Scan(&total)
	if err != nil {
		return nil, err
	}
	stats["total"] = total

	// Get pending count
	var pending int
	err = d.mainDB.QueryRow("SELECT COUNT(*) FROM post_queue WHERE posted_to_remote = 0 AND in_processing = 0").Scan(&pending)
	if err != nil {
		return nil, err
	}
	stats["pending"] = pending

	// Get processing count
	var processing int
	err = d.mainDB.QueryRow("SELECT COUNT(*) FROM post_queue WHERE in_processing = 1").Scan(&processing)
	if err != nil {
		return nil, err
	}
	stats["processing"] = processing

	// Get completed count
	var completed int
	err = d.mainDB.QueryRow("SELECT COUNT(*) FROM post_queue WHERE posted_to_remote = 1").Scan(&completed)
	if err != nil {
		return nil, err
	}
	stats["completed"] = completed

	return stats, nil
}

// DeletePostQueueEntry deletes a post queue entry by ID
func (d *Database) DeletePostQueueEntry(id int64) error {
	query := `DELETE FROM post_queue WHERE posted_to_remote = 1 AND id = ?`

	_, err := d.mainDB.Exec(query, id)
	if err != nil {
		log.Printf("Database: Failed to delete post queue entry %d: %v", id, err)
		return err
	}

	log.Printf("Database: Deleted post queue entry %d", id)
	return nil
}

// RetryPostQueueEntry resets a failed/completed entry to be processed again
func (d *Database) RetryPostQueueEntry(id int64) error {
	query := `UPDATE post_queue SET posted_to_remote = 0, in_processing = 0 WHERE id = ?`

	_, err := d.mainDB.Exec(query, id)
	if err != nil {
		log.Printf("Database: Failed to retry post queue entry %d: %v", id, err)
		return err
	}

	log.Printf("Database: Reset post queue entry %d for retry", id)
	return nil
}

// CleanupPostedEntries deletes successfully posted entries from the post_queue table
// If cleanupOlder > 0, only deletes posted entries older than N days
// If cleanupOlder = 0, deletes all posted entries
func (d *Database) CleanupPostedEntries(cleanupPosted bool, cleanupOlder int) (int64, error) {
	if !cleanupPosted && cleanupOlder == 0 {
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

	result, err := d.mainDB.Exec(query, args...)
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
