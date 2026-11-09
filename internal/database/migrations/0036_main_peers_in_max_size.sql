-- go-pugleaf: per-peer limit for the articles a peer feeds us (bytes; 0 = the server limit,
-- nntp-server -maxartsize). nntp-server enforces it on IHAVE/TAKETHIS of that peer, and the
-- peering snippets on /admin/peers announce it (INN newsfeeds <size, Diablo maxsize).
ALTER TABLE peers ADD COLUMN in_max_size INTEGER NOT NULL DEFAULT 0;
