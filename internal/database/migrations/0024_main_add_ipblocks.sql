-- Migration to add IP blocking configuration
-- Default blocked IP ranges for common problematic networks

-- Add BadIPs configuration key with default CIDR ranges
INSERT OR IGNORE INTO config (key, value) VALUES ('BadIPs', '47.74.0.0/15,47.76.0.0/14,47.80.0.0/13,185.191.171.0/24');

-- Add BlockBadIPs toggle (default: false - disabled)
INSERT OR IGNORE INTO config (key, value) VALUES ('BlockBadIPs', 'true');