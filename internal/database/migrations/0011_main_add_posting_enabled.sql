-- go-pugleaf: Add posting_enabled field to providers table
-- Migration: 0011
-- Purpose: Allow enabling/disabling posting capability per provider

PRAGMA foreign_keys = ON;

-- Add posting_enabled field to providers table
ALTER TABLE providers ADD COLUMN posting_enabled INTEGER NOT NULL DEFAULT 0 CHECK(posting_enabled IN (0, 1));

-- Create index for posting_enabled field for better query performance
CREATE INDEX IF NOT EXISTS idx_providers_posting_enabled ON providers(posting_enabled);

-- Optional: Update any existing providers to have posting disabled by default
-- (This is already handled by the DEFAULT 0 in the column definition)
