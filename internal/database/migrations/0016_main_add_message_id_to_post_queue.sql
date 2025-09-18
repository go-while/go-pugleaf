-- go-pugleaf: Add message_id field to post_queue table
-- Created: 2025-09-13
-- This migration adds a message_id field to track articles before they get article numbers

-- Create post_queue table to track articles posted from web interface
DROP TABLE IF EXISTS post_queue;

CREATE TABLE IF NOT EXISTS post_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    newsgroup_id INTEGER NOT NULL,
    message_id TEXT,
    created DATETIME DEFAULT CURRENT_TIMESTAMP,
    posted_to_remote INTEGER NOT NULL DEFAULT 0 CHECK (posted_to_remote IN (0, 1)),
    FOREIGN KEY (newsgroup_id) REFERENCES newsgroups(id) ON DELETE CASCADE
);

-- Create indexes for performance
CREATE INDEX IF NOT EXISTS idx_post_queue_newsgroup_id ON post_queue(newsgroup_id);
CREATE INDEX IF NOT EXISTS idx_post_queue_posted_to_remote ON post_queue(posted_to_remote);
CREATE INDEX IF NOT EXISTS idx_post_queue_created ON post_queue(created);
CREATE INDEX IF NOT EXISTS idx_post_queue_message_id ON post_queue(message_id);
CREATE INDEX IF NOT EXISTS idx_post_queue_newsgroup_posted ON post_queue(newsgroup_id, posted_to_remote);