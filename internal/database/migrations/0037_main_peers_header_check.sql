-- go-pugleaf: per-peer header check mode for the articles a peer feeds us (plan
-- e2e-gap-tests, decisions D1/D4). 'relay' (the default) applies the innd rules: no header
-- line length limit and a bare CR or LF inside a header line is kept verbatim and only
-- logged, so a relaying peer does not lose articles another server accepted; the colon of a
-- field name must still be followed by SP or HTAB, and NUL, a missing or duplicate core
-- header, a bad Message-ID and an unparseable Date are refused. 'strict' applies the nnrpd
-- rules nntp-server uses for POST: at most 998 octets per header line, exactly ": " after
-- the field name and a non-empty value.
ALTER TABLE peers ADD COLUMN header_check TEXT NOT NULL DEFAULT 'relay';

-- The Date limits of peer intake (IHAVE/TAKETHIS), in hours, like INN's artcutoff and
-- DATE_FUZZ; 0 = no limit. Editable on the admin settings page.
INSERT OR IGNORE INTO config (key, value) VALUES ('PeerArticleMaxAgeHours', '72');
INSERT OR IGNORE INTO config (key, value) VALUES ('PeerArticleMaxFutureHours', '24');
