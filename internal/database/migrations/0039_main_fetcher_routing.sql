-- go-pugleaf: fetcher group routing (plan fetcher-group-routing)
--   providers.routing   newsgroup patterns this provider is used for by nntp-fetcher, in the syntax
--                       of peers.in_patterns (comma or white space separated, last match wins,
--                       "!" excludes; "@" is refused). Empty: no restriction (the behaviour before
--                       this migration: only the first enabled provider is fetched from).
--   group_routes        global pins: a group that an enabled route's patterns want is fetched only
--                       from that route's provider (several routes for one group: lower priority
--                       first, then the providers' priority), never from any other provider.
-- A provider that routes reference cannot be deleted (no CASCADE; DeleteProvider refuses).

ALTER TABLE providers ADD COLUMN routing TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS group_routes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    patterns TEXT NOT NULL,
    provider_id INTEGER NOT NULL REFERENCES providers(id),
    priority INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    notes TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_group_routes_provider ON group_routes(provider_id);
