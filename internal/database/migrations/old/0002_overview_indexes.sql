-- Migration for improved overview performance
-- Add indexes for common queries

CREATE INDEX IF NOT EXISTS idx_overview_article_num ON overview(article_num);
CREATE INDEX IF NOT EXISTS idx_overview_message_id ON overview(message_id);
CREATE INDEX IF NOT EXISTS idx_overview_date_sent ON overview(date_sent);
CREATE INDEX IF NOT EXISTS idx_overview_downloaded ON overview(downloaded);
