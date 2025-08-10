-- go-pugleaf: Initial main database schema
-- Created: 2025-06-20

PRAGMA foreign_keys = ON;

-- System configuration
CREATE TABLE IF NOT EXISTS config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- NNTP providers
CREATE TABLE IF NOT EXISTS providers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    enabled BOOLEAN NOT NULL DEFAULT false,
    priority INTEGER NOT NULL DEFAULT 0,
    grp VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL UNIQUE,
    host VARCHAR(255) NOT NULL,
    port INTEGER NOT NULL,
    ssl BOOLEAN NOT NULL DEFAULT 1,
    username TEXT,
    password TEXT,
    max_conns INTEGER NOT NULL DEFAULT 0,
    max_art_size INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Newsgroups metadata
CREATE TABLE IF NOT EXISTS newsgroups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    last_article INTEGER,
    message_count INTEGER,
    active BOOLEAN NOT NULL DEFAULT 1,
    expiry_days INTEGER NOT NULL DEFAULT 0,
    max_articles INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_name ON newsgroups(name);
CREATE INDEX IF NOT EXISTS idx_last_article ON newsgroups(last_article);
CREATE INDEX IF NOT EXISTS idx_message_count ON newsgroups(message_count);
CREATE INDEX IF NOT EXISTS idx_active ON newsgroups(active);


-- Users
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Sessions
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- User permissions
CREATE TABLE IF NOT EXISTS user_permissions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    permission TEXT NOT NULL,
    granted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Migration for sections support in go-pugleaf
-- This adds support for RockSolid Light style sections

-- Sections table (equivalent to menu.conf entries)
CREATE TABLE IF NOT EXISTS sections (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT 'unset:display_name',
    description TEXT NOT NULL DEFAULT '',
    show_in_header BOOLEAN DEFAULT 1,
    enable_local_spool BOOLEAN DEFAULT 1,
    sort_order INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Section group mappings (groups.txt entries mapped to sections)
CREATE TABLE IF NOT EXISTS section_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    section_id INTEGER NOT NULL,
    newsgroup_name TEXT NOT NULL,
    group_description TEXT NOT NULL DEFAULT '',
    sort_order INTEGER DEFAULT 0,
    is_category_header BOOLEAN DEFAULT 0, -- For lines starting with ':'
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (section_id) REFERENCES sections(id),
    UNIQUE(section_id, newsgroup_name)
);

-- Index for performance
CREATE INDEX IF NOT EXISTS idx_section_groups_section ON section_groups(section_id);
CREATE INDEX IF NOT EXISTS idx_section_groups_newsgroup ON section_groups(newsgroup_name);
CREATE INDEX IF NOT EXISTS idx_section_groups_sort_order ON section_groups(sort_order);

CREATE TABLE IF NOT EXISTS api_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    apitoken VARCHAR(64) UNIQUE NOT NULL,
    ownername VARCHAR(255) NULL,
    ownerid INTEGER DEFAULT 0, -- 0 for system tokens, >0 for user IDs
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    expires_at DATETIME,
    is_enabled BOOLEAN DEFAULT 0,
    usage_count INTEGER DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_api_token ON api_tokens(apitoken);
CREATE INDEX IF NOT EXISTS idx_last_used_at ON api_tokens(last_used_at);
CREATE INDEX IF NOT EXISTS idx_expires_at ON api_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_is_enabled ON api_tokens(is_enabled);
CREATE INDEX IF NOT EXISTS idx_ownerid ON api_tokens(ownerid);


-- Add NNTP-specific users table for newsreader client authentication
-- This is separate from the web users table to allow different credential management

CREATE TABLE IF NOT EXISTS nntp_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE CHECK(length(username) >= 10 AND length(username) <= 128),
    password TEXT NOT NULL CHECK(length(password) <= 128), -- stores bcrypt hashes (~60 chars)
    maxconns INTEGER NOT NULL DEFAULT 0,
    posting INTEGER NOT NULL DEFAULT 0 CHECK(posting IN (0, 1)),
    web_user_id INTEGER DEFAULT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_login DATETIME DEFAULT NULL,
    is_active INTEGER NOT NULL DEFAULT 1 CHECK(is_active IN (0, 1)),
    FOREIGN KEY(web_user_id) REFERENCES users(id) ON DELETE SET NULL
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_nntp_users_username ON nntp_users(username);
CREATE INDEX IF NOT EXISTS idx_nntp_users_active ON nntp_users(is_active);
CREATE INDEX IF NOT EXISTS idx_nntp_users_posting ON nntp_users(posting);
CREATE INDEX IF NOT EXISTS idx_nntp_users_web_user_id ON nntp_users(web_user_id);

-- Optional: Track NNTP user sessions/connections
CREATE TABLE IF NOT EXISTS nntp_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    connection_id TEXT NOT NULL,
    remote_addr TEXT NOT NULL DEFAULT '',
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_activity DATETIME DEFAULT CURRENT_TIMESTAMP,
    is_active INTEGER NOT NULL DEFAULT 1 CHECK(is_active IN (0, 1)),
    FOREIGN KEY(user_id) REFERENCES nntp_users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_nntp_sessions_user_id ON nntp_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_nntp_sessions_active ON nntp_sessions(is_active);
CREATE INDEX IF NOT EXISTS idx_nntp_sessions_connection_id ON nntp_sessions(connection_id);

-- go-pugleaf: AI Models schema for user-friendly model management
-- Created: 2025-06-28

PRAGMA foreign_keys = ON;

-- AI Models for chat functionality
CREATE TABLE IF NOT EXISTS ai_models (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    post_key TEXT NOT NULL UNIQUE,              -- Maps to proxy's Post field (e.g. "gemma3_12b")
    display_name TEXT NOT NULL DEFAULT '',                 -- User-friendly name (e.g. "Gemma 3 12B")
    ollama_model_name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',                           -- Short description for users
    is_active BOOLEAN NOT NULL DEFAULT 1,       -- Admin can enable/disable models
    is_default BOOLEAN NOT NULL DEFAULT 0,      -- Default selection for new chats
    sort_order INTEGER NOT NULL DEFAULT 0,      -- Display order in UI
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

PRAGMA foreign_keys = ON;

-- Make the field required for new records (after setting existing ones)

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_ai_models_ollama_name ON ai_models(ollama_model_name);
CREATE INDEX IF NOT EXISTS idx_ai_models_post_key ON ai_models(post_key);
CREATE INDEX IF NOT EXISTS idx_ai_models_active ON ai_models(is_active);
CREATE INDEX IF NOT EXISTS idx_ai_models_default ON ai_models(is_default);
CREATE INDEX IF NOT EXISTS idx_ai_models_sort_order ON ai_models(sort_order);

-- Ensure only one default model at a time
CREATE TRIGGER IF NOT EXISTS ai_models_single_default
    BEFORE UPDATE OF is_default ON ai_models
    WHEN NEW.is_default = 1
BEGIN
    UPDATE ai_models SET is_default = 0 WHERE is_default = 1 AND id != NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS ai_models_single_default_insert
    BEFORE INSERT ON ai_models
    WHEN NEW.is_default = 1
BEGIN
    UPDATE ai_models SET is_default = 0 WHERE is_default = 1;
END;

-- Update timestamp trigger
CREATE TRIGGER IF NOT EXISTS ai_models_update_timestamp
    AFTER UPDATE ON ai_models
BEGIN
    UPDATE ai_models SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
END;


-- go-pugleaf: Add history system configuration
-- This migration adds support for storing history system settings
-- to prevent breaking changes when UseShortHashLen is modified

-- Insert default history configuration if not exists
INSERT OR IGNORE INTO config (key, value) VALUES ('history_use_short_hash_len', '3');

-- Add comment to indicate this is a critical setting that cannot be changed once set
INSERT OR IGNORE INTO config (key, value) VALUES ('history_config_locked', 'false');

-- go-pugleaf: Add registration control setting to config table
-- Migration: 0010_maindb_registration.sql

-- Migration: Enhanced User Session Security
-- Adds session management fields to users table

-- Add session management columns to users table
ALTER TABLE users ADD COLUMN session_id VARCHAR(64) DEFAULT '';
ALTER TABLE users ADD COLUMN last_login_ip VARCHAR(45) DEFAULT ''; -- Supports IPv6
ALTER TABLE users ADD COLUMN session_expires_at DATETIME NULL;
ALTER TABLE users ADD COLUMN login_attempts INTEGER DEFAULT 0;

-- Create indexes for performance
CREATE INDEX IF NOT EXISTS idx_users_session_id ON users(session_id);
CREATE INDEX IF NOT EXISTS idx_users_session_expires ON users(session_expires_at);
CREATE INDEX IF NOT EXISTS idx_users_login_attempts ON users(login_attempts);

-- Clean up any existing expired sessions (optional)
UPDATE users SET session_id = '', session_expires_at = NULL WHERE session_expires_at < CURRENT_TIMESTAMP;

-- Add default registration enabled setting
INSERT OR IGNORE INTO config (key, value) VALUES ('registration_enabled', 'true');

-- Update the registration setting description comment
-- The registration_enabled setting controls whether new users can register
-- Values: 'true' (registration enabled) or 'false' (registration disabled)
-- Default: 'true' (enabled)

