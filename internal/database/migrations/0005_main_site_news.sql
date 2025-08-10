-- go-pugleaf: Create site news table for dynamic home page news management

CREATE TABLE IF NOT EXISTS site_news (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subject TEXT NOT NULL,
    content TEXT NOT NULL,
    date_published DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    is_visible INTEGER NOT NULL DEFAULT 1, -- 1 = visible, 0 = hidden
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Create index for efficient queries
CREATE INDEX IF NOT EXISTS idx_site_news_visible_date ON site_news(is_visible, date_published DESC);
CREATE INDEX IF NOT EXISTS idx_site_news_date ON site_news(date_published DESC);
