-- go-pugleaf: Add proxy support for NNTP providers
-- Created: 2025-09-12
-- Adds proxy configuration fields to providers table for Tor and proxy support

PRAGMA foreign_keys = ON;

-- Add proxy configuration fields to providers table
ALTER TABLE providers ADD COLUMN proxy_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE providers ADD COLUMN proxy_type TEXT NOT NULL DEFAULT 'direct';
ALTER TABLE providers ADD COLUMN proxy_host TEXT DEFAULT '';
ALTER TABLE providers ADD COLUMN proxy_port INTEGER DEFAULT 0;
ALTER TABLE providers ADD COLUMN proxy_username TEXT DEFAULT '';
ALTER TABLE providers ADD COLUMN proxy_password TEXT DEFAULT '';

-- Add check constraint for valid proxy types
-- Note: SQLite doesn't support adding constraints to existing tables, so we'll handle validation in Go

-- Create index for proxy-enabled providers
CREATE INDEX IF NOT EXISTS idx_providers_proxy_enabled ON providers(proxy_enabled);

-- Insert default configuration values for proxy settings
INSERT OR IGNORE INTO config (key, value) VALUES ('default_tor_proxy_host', '127.0.0.1');
INSERT OR IGNORE INTO config (key, value) VALUES ('default_tor_proxy_port', '9050');
INSERT OR IGNORE INTO config (key, value) VALUES ('proxy_connect_timeout', '30');
INSERT OR IGNORE INTO config (key, value) VALUES ('proxy_fallback_enabled', 'false');
