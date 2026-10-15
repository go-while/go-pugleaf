-- go-pugleaf: repair newsgroups rows created by the old batch upsert (A-db-1, AUDIT-2026-10)
UPDATE newsgroups SET hierarchy = CASE WHEN INSTR(name, '.') > 0 THEN SUBSTR(name, 1, INSTR(name, '.') - 1) ELSE name END
 WHERE hierarchy IS NULL;
UPDATE newsgroups SET last_article = 0 WHERE last_article IS NULL;
UPDATE newsgroups SET message_count = 0 WHERE message_count IS NULL;
