-- Migration: Add WebPostMaxArticleSize config key
-- This sets the maximum size limit for articles posted via web interface

INSERT INTO config (key, value)
VALUES ('WebPostMaxArticleSize', '32768');
