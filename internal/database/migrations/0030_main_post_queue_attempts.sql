-- go-pugleaf: post_queue attempt counter (plan followup-leftovers D-A)
-- attempts counts the times every posting provider rejected the article (441); connection and
-- pool errors set last_error but do not count. A row with attempts >= PostQueueMaxAttempts is
-- parked: post-queue no longer selects it until an admin retries it or the key is raised.

ALTER TABLE post_queue ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_queue ADD COLUMN last_error TEXT;

INSERT OR IGNORE INTO config (key, value) VALUES ('PostQueueMaxAttempts', '5');
