-- go-pugleaf: NNTP peers (plan nntp-peering)
-- A peer is a news server that may feed us (IHAVE, MODE STREAM, CHECK, TAKETHIS) and that we may
-- feed. Before this table a peer was an nntp_users row with posting=1; feed commands are peer-only now.
--   path_token        the peer's Path identity (the leftmost element it writes)
--   hosts             comma list of hostnames, IPv4/IPv6 addresses and CIDRs (inbound match);
--                     empty: the peer is matched by AUTHINFO only
--   in_username, in_password_hash   optional AUTHINFO for the peer (bcrypt, like nntp_users)
--   in_patterns       wildmat list of groups the peer may send us (INN newsfeeds syntax, ! and @)
--   out_*             our outgoing feed to the peer; out_password is sent to the remote, so it is
--                     stored as given (like providers.password)
--   out_flags         INN newsfeeds flag subset (<N >N Hcount Gcount Nm Nu)
-- History rejects are kept HistoryRejectDays days (0 = as long as history).

CREATE TABLE IF NOT EXISTS peers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    path_token TEXT NOT NULL,
    hosts TEXT NOT NULL DEFAULT '',
    in_username TEXT NOT NULL DEFAULT '',
    in_password_hash TEXT NOT NULL DEFAULT '',
    in_patterns TEXT NOT NULL DEFAULT '*',
    out_enabled INTEGER NOT NULL DEFAULT 0,
    out_host TEXT NOT NULL DEFAULT '',
    out_port INTEGER NOT NULL DEFAULT 119,
    out_tls INTEGER NOT NULL DEFAULT 0,
    out_username TEXT NOT NULL DEFAULT '',
    out_password TEXT NOT NULL DEFAULT '',
    out_patterns TEXT NOT NULL DEFAULT '*,!control*,!junk',
    out_flags TEXT NOT NULL DEFAULT '',
    max_conns INTEGER NOT NULL DEFAULT 4,
    enabled INTEGER NOT NULL DEFAULT 1,
    notes TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- A peer login name is unique among peers (empty = no AUTHINFO).
CREATE UNIQUE INDEX IF NOT EXISTS idx_peers_in_username ON peers(in_username) WHERE in_username != '';

INSERT OR IGNORE INTO config (key, value) VALUES ('HistoryRejectDays', '30');
