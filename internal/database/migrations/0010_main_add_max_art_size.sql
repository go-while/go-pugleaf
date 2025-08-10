-- go-pugleaf: Add max_art_size column to newsgroups table
-- This column allows per-newsgroup article size limits similar to providers

-- Add max_art_size column to newsgroups table with default value of 0 (no limit)
ALTER TABLE newsgroups ADD COLUMN max_art_size INTEGER NOT NULL DEFAULT 0;
