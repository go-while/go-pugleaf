package database

import (
	"log"
	"time"
)

// PostQueueEntry represents a record in the post_queue table
type PostQueueEntry struct {
	ID             int64     `db:"id"`
	NewsgroupID    int64     `db:"newsgroup_id"`
	MessageID      string    `db:"message_id"` // Added in migration 0016
	Created        time.Time `db:"created"`
	PostedToRemote bool      `db:"posted_to_remote"`
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

// GetPendingPostQueueEntries retrieves entries that haven't been posted to remote servers
func (d *Database) GetPendingPostQueueEntries() ([]PostQueueEntry, error) {
	query := `
		SELECT id, newsgroup_id, message_id, created, posted_to_remote
		FROM post_queue
		WHERE posted_to_remote = 0
		ORDER BY created ASC
	`

	rows, err := d.mainDB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []PostQueueEntry
	for rows.Next() {
		var entry PostQueueEntry
		err := rows.Scan(&entry.ID, &entry.NewsgroupID, &entry.MessageID, &entry.Created, &entry.PostedToRemote)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// MarkPostQueueAsPostedToRemote marks an entry as posted to remote servers
func (d *Database) MarkPostQueueAsPostedToRemote(id int64) error {
	query := `UPDATE post_queue SET posted_to_remote = 1 WHERE id = ?`

	_, err := d.mainDB.Exec(query, id)
	if err != nil {
		log.Printf("Database: Failed to mark post_queue entry %d as posted to remote: %v", id, err)
		return err
	}

	log.Printf("Database: Marked post_queue entry %d as posted to remote", id)
	return nil
}
