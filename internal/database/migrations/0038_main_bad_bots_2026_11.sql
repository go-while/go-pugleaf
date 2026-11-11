-- go-pugleaf: more User-Agent patterns for the bad-bot list (2026-11-11)
-- anticlone, censys, claudebot, infrawatch and twitterbot are appended to BadBots unless the list
-- already holds them (the admin may have added some by hand). Patterns are matched lowercase as
-- substrings of the User-Agent (config.UpdateBadBots). 0037 is taken by the peers header_check
-- migration of plan e2e-gap-tests.

UPDATE config SET value = CASE WHEN TRIM(value) = '' THEN 'anticlone' ELSE value || ',anticlone' END
 WHERE key = 'BadBots' AND instr(',' || lower(REPLACE(value, ' ', '')) || ',', ',anticlone,') = 0;
UPDATE config SET value = CASE WHEN TRIM(value) = '' THEN 'censys' ELSE value || ',censys' END
 WHERE key = 'BadBots' AND instr(',' || lower(REPLACE(value, ' ', '')) || ',', ',censys,') = 0;
UPDATE config SET value = CASE WHEN TRIM(value) = '' THEN 'claudebot' ELSE value || ',claudebot' END
 WHERE key = 'BadBots' AND instr(',' || lower(REPLACE(value, ' ', '')) || ',', ',claudebot,') = 0;
UPDATE config SET value = CASE WHEN TRIM(value) = '' THEN 'infrawatch' ELSE value || ',infrawatch' END
 WHERE key = 'BadBots' AND instr(',' || lower(REPLACE(value, ' ', '')) || ',', ',infrawatch,') = 0;
UPDATE config SET value = CASE WHEN TRIM(value) = '' THEN 'twitterbot' ELSE value || ',twitterbot' END
 WHERE key = 'BadBots' AND instr(',' || lower(REPLACE(value, ' ', '')) || ',', ',twitterbot,') = 0;
