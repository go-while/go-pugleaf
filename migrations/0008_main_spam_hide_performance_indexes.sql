-- Additional indexes for spam/hide performance optimization - MAIN DATABASE ONLY
-- Migration: 0008_main_spam_hide_performance_indexes.sql

-- Composite index for spam table queries (most queries order by id DESC)
CREATE INDEX IF NOT EXISTS idx_spam_id_desc ON spam(id DESC);

-- Composite index for efficient spam counting by newsgroup
CREATE INDEX IF NOT EXISTS idx_spam_newsgroup_id_article_num ON spam(newsgroup_id, article_num);

-- Composite index for user spam flags by newsgroup (for admin queries)
CREATE INDEX IF NOT EXISTS idx_user_spam_flags_newsgroup_id ON user_spam_flags(newsgroup_id);

-- Composite index for user spam flags with timestamp (for cleanup/analysis)
CREATE INDEX IF NOT EXISTS idx_user_spam_flags_flagged_at ON user_spam_flags(flagged_at);
