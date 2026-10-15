-- go-pugleaf: monotonic article numbers per group (INT-5, AUDIT-2026-10)
-- Runs lazily on the first open of every group database: one tiny table, one trigger, no table rebuild.
--
-- article_num is an INTEGER PRIMARY KEY without AUTOINCREMENT, so SQLite hands out max(rowid)+1:
-- after the highest-numbered articles were deleted (expire-news), new articles reused their numbers
-- and inherited thread caches, spam flags and cached bodies. The writers now insert
-- article_num = (SELECT last_num + 1 FROM article_num_seq WHERE id = 1); the trigger keeps last_num
-- at the highest number ever inserted, whoever inserted it (also an explicit number).
CREATE TABLE IF NOT EXISTS article_num_seq (id INTEGER PRIMARY KEY CHECK (id = 1), last_num INTEGER NOT NULL);
INSERT OR IGNORE INTO article_num_seq (id, last_num) SELECT 1, COALESCE(MAX(article_num), 0) FROM articles;
CREATE TRIGGER IF NOT EXISTS articles_num_seq_ai AFTER INSERT ON articles
BEGIN
    UPDATE article_num_seq SET last_num = NEW.article_num WHERE id = 1 AND NEW.article_num > last_num;
END;
