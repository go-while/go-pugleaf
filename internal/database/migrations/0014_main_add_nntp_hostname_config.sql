-- go-pugleaf: Add NNTP hostname configuration
-- Created: 2025-09-12
-- Adds LocalNNTPHostname configuration to support processor hostname validation

PRAGMA foreign_keys = ON;

-- Insert default configuration value for NNTP hostname
-- Empty default value - will be set by admin or fail validation if not configured
INSERT OR IGNORE INTO config (key, value) VALUES ('local_nntp_hostname', '');
