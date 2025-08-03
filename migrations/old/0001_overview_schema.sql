-- go-pugleaf: Per-group overview database schema
-- This file should be applied to each group_<name>_overview.db
-- Created: 2025-06-20

PRAGMA foreign_keys = ON;

-- Overview table: XOVER data for fast group listing
CREATE TABLE IF NOT EXISTS overview (
    article_num INTEGER PRIMARY KEY AUTOINCREMENT,
    subject TEXT,
    from_header TEXT,
    date_sent DATETIME,
    date_string TEXT,
    message_id TEXT NOT NULL,
    "references" TEXT,
    bytes INTEGER,
    lines INTEGER,
    reply_count INTEGER DEFAULT 0,
    downloaded INTEGER NOT NULL DEFAULT 0
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_overview_date_sent ON overview(date_sent);
CREATE INDEX IF NOT EXISTS idx_overview_message_id ON overview(message_id);
CREATE INDEX IF NOT EXISTS idx_overview_downloaded ON overview(downloaded);
CREATE INDEX IF NOT EXISTS idx_overview_ref ON overview("references");
