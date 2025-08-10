-- Create user spam flags tracking table to prevent duplicate flags
CREATE TABLE user_spam_flags (
    user_id INTEGER NOT NULL,
    newsgroup_id INTEGER NOT NULL,
    article_num INTEGER NOT NULL,
    flagged_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, newsgroup_id, article_num),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Create index for performance
CREATE INDEX idx_user_spam_flags_user_id ON user_spam_flags(user_id);
