-- Migration to add NNTP-specific fields to main newsgroups table
-- This allows the main database to handle NNTP functionality without needing active.db

-- Add NNTP water marks and status to main newsgroups table
ALTER TABLE newsgroups ADD COLUMN high_water INTEGER DEFAULT 0;
ALTER TABLE newsgroups ADD COLUMN low_water INTEGER DEFAULT 1;
ALTER TABLE newsgroups ADD COLUMN status CHAR(1) DEFAULT 'y';

-- Update existing newsgroups with default NNTP values
UPDATE newsgroups SET high_water = 0 WHERE high_water IS NULL;
UPDATE newsgroups SET low_water = 1 WHERE low_water IS NULL;
UPDATE newsgroups SET status = 'y' WHERE status IS NULL;

-- Add indexes for NNTP performance
CREATE INDEX IF NOT EXISTS idx_newsgroups_status ON newsgroups(status);
CREATE INDEX IF NOT EXISTS idx_newsgroups_high_water ON newsgroups(high_water);
CREATE INDEX IF NOT EXISTS idx_newsgroups_low_water ON newsgroups(low_water);
