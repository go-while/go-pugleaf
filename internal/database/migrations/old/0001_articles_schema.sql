-- go-pugleaf: Per-group articles database schema
-- This file should be applied to each group_<name>_articles.db
-- Created: 2025-06-20

PRAGMA foreign_keys = ON;

-- Articles table: stores full article data
CREATE TABLE IF NOT EXISTS articles (
    article_num INTEGER PRIMARY KEY, -- NNTP article number in group
    message_id TEXT NOT NULL UNIQUE,
    subject TEXT,
    from_header TEXT,
    date_sent DATETIME,
    date_string TEXT,
    "references" TEXT,
    bytes INTEGER,
    lines INTEGER,
    reply_count INTEGER DEFAULT 0,
    path TEXT,         -- NNTP Path header
    headers_json TEXT, -- All headers as JSON
    body_text TEXT,    -- Article body
    imported_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_articles_message_id ON articles(message_id);
CREATE INDEX IF NOT EXISTS idx_articles_date_sent ON articles(date_sent);
