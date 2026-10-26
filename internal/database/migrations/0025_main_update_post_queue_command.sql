-- Migration: Update cron job commands with default args

UPDATE cron_jobs
SET command = './post-queue -max-batch 100 -cleanup-older 0'
WHERE command = './post-queue';

UPDATE cron_jobs
SET command = './pugleaf-fetcher -group "" -max-batch 1000 -max-queue 1280 -max-batch-threads 16 -download-max-par 1 -fetch-active-only=true'
WHERE command = './pugleaf-fetcher';