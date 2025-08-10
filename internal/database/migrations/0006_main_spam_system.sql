-- Create spam tracking table in main database
CREATE TABLE spam (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    newsgroup_id INTEGER NOT NULL,
    article_num INTEGER NOT NULL,
    UNIQUE(newsgroup_id, article_num)
);

-- Create index for performance
CREATE INDEX idx_spam_newsgroup_id ON spam(newsgroup_id);
CREATE INDEX idx_spam_article_num ON spam(article_num);
