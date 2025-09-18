-- go-pugleaf: Add in_processing field to post_queue table
-- Created: 2025-09-13
-- This migration adds an in_processing field to prevent multiple processes from working on the same articles

PRAGMA foreign_keys = ON;

-- Add in_processing field to post_queue table
ALTER TABLE post_queue ADD COLUMN in_processing INTEGER NOT NULL DEFAULT 0 CHECK (in_processing IN (0, 1));

-- Create index for the new field for performance
CREATE INDEX IF NOT EXISTS idx_post_queue_in_processing ON post_queue(in_processing);

-- Create composite index for common queries (not posted and not in processing)
CREATE INDEX IF NOT EXISTS idx_post_queue_available ON post_queue(posted_to_remote, in_processing) WHERE posted_to_remote = 0 AND in_processing = 0;
