-- go-pugleaf: NOCASE index for the newsgroup name search, drop the redundant idx_name
--
-- SearchNewsgroupsWithOptions / CountSearchNewsgroupsWithOptions use a prefix pattern
-- "name LIKE ? ESCAPE '\'". With case_sensitive_like off (the default), SQLite only turns
-- such a LIKE into an index range scan on an index with NOCASE collation.
CREATE INDEX IF NOT EXISTS idx_newsgroups_name_nocase ON newsgroups(name COLLATE NOCASE);

-- idx_name (0001_main_schema.sql) duplicates the automatic index of UNIQUE(name).
DROP INDEX IF EXISTS idx_name;
