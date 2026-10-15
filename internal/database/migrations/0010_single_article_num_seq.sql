-- go-pugleaf: monotonic article numbers per group (INT-5, AUDIT-2026-10)
-- Runs lazily on the first open of every group database: one tiny table, one trigger, no table rebuild.
--
-- article_num is an INTEGER PRIMARY KEY without AUTOINCREMENT, so SQLite hands out max(rowid)+1:
-- after the highest-numbered articles were deleted (expire-news), new articles reused their numbers
-- and inherited thread caches, spam flags and cached bodies. The writers now insert
-- article_num = (SELECT MAX(last_num, COALESCE((SELECT MAX(article_num) FROM articles), 0)) + 1
-- FROM article_num_seq WHERE id = 1); the trigger keeps last_num at the highest number ever
-- inserted, whoever inserted it (also an explicit number).
--
-- The seed is the highest number this group database still mentions anywhere: articles, but also
-- the thread tables, which may still hold rows of top articles that were expired before this
-- migration existed (handing those numbers out again would be INT-5 once more). Every table
-- below is created by 0001_single_db_schema.sql; each MAX is an index or rowid lookup, and
-- COALESCE keeps an empty table from turning the multi-argument max() into NULL.
CREATE TABLE IF NOT EXISTS article_num_seq (id INTEGER PRIMARY KEY CHECK (id = 1), last_num INTEGER NOT NULL);
INSERT OR IGNORE INTO article_num_seq (id, last_num) SELECT 1, MAX(
    COALESCE((SELECT MAX(article_num) FROM articles), 0),
    COALESCE((SELECT MAX(root_article) FROM threads), 0),
    COALESCE((SELECT MAX(parent_article) FROM threads), 0),
    COALESCE((SELECT MAX(child_article) FROM threads), 0),
    COALESCE((SELECT MAX(thread_root) FROM thread_cache), 0),
    COALESCE((SELECT MAX(last_child_number) FROM thread_cache), 0),
    COALESCE((SELECT MAX(thread_root) FROM cached_trees), 0),
    COALESCE((SELECT MAX(thread_root) FROM tree_stats), 0)
);
CREATE TRIGGER IF NOT EXISTS articles_num_seq_ai AFTER INSERT ON articles
BEGIN
    UPDATE article_num_seq SET last_num = NEW.article_num WHERE id = 1 AND NEW.article_num > last_num;
END;
