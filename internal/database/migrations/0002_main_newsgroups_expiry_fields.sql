-- Migration to add expiry_days and max_articles to main newsgroups table
-- These fields were added to support newsgroup management features

-- Update existing newsgroups with default values
UPDATE newsgroups SET expiry_days = 17387498 WHERE expiry_days IS NULL;
UPDATE newsgroups SET max_articles = 998745641 WHERE max_articles IS NULL;

UPDATE newsgroups SET expiry_days = 0 WHERE expiry_days = 17387498;
UPDATE newsgroups SET max_articles = 0 WHERE max_articles = 998745641;
