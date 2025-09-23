-- Migration: Create cron_jobs table
-- This migration creates the cron_jobs table for managing scheduled tasks

CREATE TABLE IF NOT EXISTS cron_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    command TEXT NOT NULL,
    interval_minutes INTEGER NOT NULL,
    start_hour_minute TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT 1,
    last_run DATETIME NULL,
    run_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Create index for efficient querying of pending jobs
CREATE INDEX IF NOT EXISTS idx_cron_jobs_next_run ON cron_jobs(enabled);

-- Create index for efficient querying by name
CREATE INDEX IF NOT EXISTS idx_cron_jobs_name ON cron_jobs(name);
