-- go-pugleaf: MaxCrossPosts config key (admin settings page)
-- The most newsgroups one article may be filed into; the fetcher, the NNTP intake and the web
-- post form reject an article listing more. Was hardcoded to 9 in internal/processor.
-- Valid range 1..15 (the per-article process queue holds 16 entries); anything else reads as 9.

INSERT OR IGNORE INTO config (key, value) VALUES ('MaxCrossPosts', '9');
