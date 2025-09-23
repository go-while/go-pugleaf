-- Migration: Remove system_status table
-- This migration drops the system_status table if it exists

-- Drop the index first if it exists
DROP INDEX IF EXISTS idx_system_status_shutdown_state;

-- Drop the system_status table if it exists
DROP TABLE IF EXISTS system_status;
