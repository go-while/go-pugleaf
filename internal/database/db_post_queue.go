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
		INSERT INTO post_queue (newsgroup_id, message_id, created, posted_to_remote)
		VALUES (?, ?, CURRENT_TIMESTAMP, 0)
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

	log.Printf("Database: Marked post_queue entry %d as posted to remote", id)
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
