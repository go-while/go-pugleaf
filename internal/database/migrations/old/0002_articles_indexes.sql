-- Migration for improved articles performance
-- Add indexes for common queries

CREATE INDEX IF NOT EXISTS idx_articles_article_num ON articles(article_num);
CREATE INDEX IF NOT EXISTS idx_articles_message_id ON articles(message_id);
CREATE INDEX IF NOT EXISTS idx_articles_date_sent ON articles(date_sent);
CREATE INDEX IF NOT EXISTS idx_articles_subject ON articles(subject);
CREATE INDEX IF NOT EXISTS idx_articles_from_header ON articles(from_header);
