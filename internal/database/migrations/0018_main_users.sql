-- go-pugleaf: Enhanced User Management Fields
-- Migration: 0015_users_enhancements.sql
-- Adds user verification, posting controls, and activity tracking

-- Add new user management fields to users table
ALTER TABLE users ADD COLUMN verified INTEGER NOT NULL DEFAULT 0 CHECK(verified IN (0, 1));
ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0, 1));
ALTER TABLE users ADD COLUMN no_posting INTEGER NOT NULL DEFAULT 0 CHECK(no_posting IN (0, 1));
ALTER TABLE users ADD COLUMN post_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN lastpost_unix INTEGER DEFAULT NULL;

-- Create indexes for performance on new fields
CREATE INDEX IF NOT EXISTS idx_users_verified ON users(verified);
CREATE INDEX IF NOT EXISTS idx_users_disabled ON users(disabled);
CREATE INDEX IF NOT EXISTS idx_users_no_posting ON users(no_posting);
CREATE INDEX IF NOT EXISTS idx_users_post_count ON users(post_count);
CREATE INDEX IF NOT EXISTS idx_users_lastpost_unix ON users(lastpost_unix);

-- Comments for field meanings:
-- verified: 0 = unverified user, 1 = verified user (email confirmed, etc.)
-- disabled: 0 = active user, 1 = disabled user (cannot login)
-- no_posting: 0 = can post, 1 = cannot post (read-only access)
-- post_count: total number of posts made by user
-- lastpost_unix: Unix timestamp of user's last post (NULL if never posted)
