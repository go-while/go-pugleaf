-- go-pugleaf: hash session tokens at rest and give the login lockout its own clock
--
-- users.session_id now stores hex(sha256(token)); the cookie keeps the raw token.
-- Existing rows hold raw tokens, so every session is cleared: each user logs in once
-- after the upgrade (sessions only last an hour anyway).
--
-- The login lockout window used users.updated_at, which logout and the 15-minute
-- CleanupExpiredSessions also touch, so those session writes restarted a running
-- lockout. login_attempt_at is written only by the lockout functions.
--
-- No PRAGMA lines: migrations run inside one transaction.

ALTER TABLE users ADD COLUMN login_attempt_at DATETIME;

UPDATE users SET login_attempt_at = updated_at WHERE login_attempts > 0;

UPDATE users SET session_id = '', session_expires_at = NULL;
