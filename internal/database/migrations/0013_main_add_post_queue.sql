-- go-pugleaf: Add post_queue table for tracking posted articles from web interface
-- Created: 2025-09-12
-- This table tracks articles posted through the web interface to manage posting to remote servers

PRAGMA foreign_keys = ON;

-- Create post_queue table to track articles posted from web interface
CREATE TABLE IF NOT EXISTS post_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    artnum INTEGER NOT NULL,
    newsgroup_id INTEGER NOT NULL,
    created DATETIME DEFAULT CURRENT_TIMESTAMP,
    posted_to_remote INTEGER NOT NULL DEFAULT 0 CHECK (posted_to_remote IN (0, 1)),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (newsgroup_id) REFERENCES newsgroups(id) ON DELETE CASCADE,
    UNIQUE(newsgroup_id, artnum)
);

-- Create indexes for performance
CREATE INDEX IF NOT EXISTS idx_post_queue_user_id ON post_queue(user_id);
CREATE INDEX IF NOT EXISTS idx_post_queue_newsgroup_id ON post_queue(newsgroup_id);
CREATE INDEX IF NOT EXISTS idx_post_queue_artnum ON post_queue(artnum);
CREATE INDEX IF NOT EXISTS idx_post_queue_posted_to_remote ON post_queue(posted_to_remote);
CREATE INDEX IF NOT EXISTS idx_post_queue_created ON post_queue(created);

-- Create composite index for common queries
CREATE INDEX IF NOT EXISTS idx_post_queue_newsgroup_posted ON post_queue(newsgroup_id, posted_to_remote);
