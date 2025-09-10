-- go-pugleaf: Add posting field to providers table
-- Migration: 0011
-- Purpose: Allow enabling/disabling posting capability per provider

PRAGMA foreign_keys = ON;

-- Add posting field to providers table
ALTER TABLE providers ADD COLUMN posting INTEGER NOT NULL DEFAULT 0 CHECK(posting IN (0, 1));

-- Create index for posting field for better query performance
CREATE INDEX IF NOT EXISTS idx_providers_posting ON providers(posting);

-- Optional: Update any existing providers to have posting disabled by default
-- (This is already handled by the DEFAULT 0 in the column definition)
