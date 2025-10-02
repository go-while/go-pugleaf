-- Migration: Add APIEnabled config key
-- This controls whether API services are enabled or disabled

INSERT INTO config (key, value)
VALUES ('APIEnabled', 'false');
