-- go-pugleaf: drop redundant indexes on articles (per group database)
--
-- This migration runs lazily on the first open of every existing group database,
-- so it only drops indexes (cheap) and creates nothing.
--
-- idx_articles_message_id duplicates the automatic index of UNIQUE(message_id).
DROP INDEX IF EXISTS idx_articles_message_id;

-- idx_articles_hide(hide) is covered by idx_articles_hide_article_num(hide, article_num):
-- article_num is the rowid, which every index already carries.
DROP INDEX IF EXISTS idx_articles_hide;

-- idx_articles_spam(spam) is a prefix of idx_articles_spam_hide(spam, hide).
DROP INDEX IF EXISTS idx_articles_spam;
