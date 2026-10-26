-- Migration: Update NULL lastpost_unix values to 0
-- This migration ensures all lastpost_unix fields have a default value of 0 instead of NULL

UPDATE users SET lastpost_unix = 0 WHERE lastpost_unix IS NULL;
