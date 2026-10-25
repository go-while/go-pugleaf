-- go-pugleaf: COMPRESS DEFLATE (RFC 8054) per provider
-- Created: 2026-10-25
-- The NNTP client sends COMPRESS DEFLATE after the greeting/AUTHINFO when this flag is set;
-- a refusal (403/500/501/502/503) is logged once and the connection stays uncompressed.

ALTER TABLE providers ADD COLUMN compress BOOLEAN NOT NULL DEFAULT false;
