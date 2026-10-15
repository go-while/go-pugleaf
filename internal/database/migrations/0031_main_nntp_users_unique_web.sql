-- go-pugleaf: a web user owns at most one active NNTP account (plan followup-leftovers, D-C1)
-- The admin pages checked this before writing (check-then-write), so two concurrent requests
-- could still map one web user twice. Repair first: per web user the active NNTP account with
-- the lowest id keeps the mapping, the other active ones lose it (web_user_id NULL). Inactive
-- accounts keep theirs: the index below covers active accounts only.
-- To see what this changes, run before upgrading:
--   SELECT web_user_id, COUNT(*) FROM nntp_users
--    WHERE web_user_id IS NOT NULL AND is_active = 1 GROUP BY web_user_id HAVING COUNT(*) > 1;
UPDATE nntp_users SET web_user_id = NULL, updated_at = CURRENT_TIMESTAMP
 WHERE web_user_id IS NOT NULL AND is_active = 1
   AND id > (SELECT MIN(n2.id) FROM nntp_users n2
              WHERE n2.web_user_id = nntp_users.web_user_id AND n2.is_active = 1);

CREATE UNIQUE INDEX IF NOT EXISTS idx_nntp_users_web_user_active
    ON nntp_users(web_user_id) WHERE web_user_id IS NOT NULL AND is_active = 1;
