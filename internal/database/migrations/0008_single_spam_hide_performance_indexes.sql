-- Additional indexes for articles table spam/hide performance optimization
-- Migration: 0008_single_spam_hide_performance_indexes.sql

-- Composite index for filtering and ordering with hide=0 and date
CREATE INDEX IF NOT EXISTS idx_articles_hide_date ON articles(hide, date_sent DESC);

-- Composite index for spam filtering with hide constraint
CREATE INDEX IF NOT EXISTS idx_articles_spam_hide ON articles(spam, hide);

-- Composite index for efficient thread cache queries (hide filtering with thread_root lookups)
CREATE INDEX IF NOT EXISTS idx_articles_hide_article_num ON articles(hide, article_num);

-- Index for efficient spam counter checks
CREATE INDEX IF NOT EXISTS idx_articles_article_num_spam ON articles(article_num, spam);
