-- Migration: Add unique constraint to cron_jobs name and insert default jobs
-- This migration makes the cron job name unique and adds default system cron jobs

-- Add unique constraint to name column
CREATE UNIQUE INDEX IF NOT EXISTS idx_cron_jobs_name_unique ON cron_jobs(name);

-- Insert default cron jobs if they don't exist
-- All jobs are disabled by default for manual activation

-- Post Queue Processor - runs every 5 minutes
INSERT OR IGNORE INTO cron_jobs (
    name,
    command,
    interval_minutes,
    start_hour_minute,
    enabled,
    created_at
) VALUES (
    'post-queue',
    './post-queue',
    5,
    '',
    0,
    CURRENT_TIMESTAMP
);

-- Pugleaf Fetcher - runs every 5 minutes
INSERT OR IGNORE INTO cron_jobs (
    name,
    command,
    interval_minutes,
    start_hour_minute,
    enabled,
    created_at
) VALUES (
    'pugleaf-fetcher',
    './pugleaf-fetcher',
    5,
    '',
    0,
    CURRENT_TIMESTAMP
);

-- News Expiry - runs daily at 02:59
INSERT OR IGNORE INTO cron_jobs (
    name,
    command,
    interval_minutes,
    start_hour_minute,
    enabled,
    created_at
) VALUES (
    'expire-news',
    './expire-news',
    1440,
    '02:59',
    0,
    CURRENT_TIMESTAMP
);
