-- go-pugleaf: Add BadBots configuration
-- Created: 2025-09-27
-- Adds BadBots list and BlockBadBots toggle for configurable bot detection

PRAGMA foreign_keys = ON;

-- Insert default configuration values for bot detection
-- BadBots: comma-separated list of user-agent patterns to block (case-insensitive)
INSERT OR IGNORE INTO config (key, value) VALUES ('BadBots', 'acunetix,ahref,alibaba,amazon,census,crawler,curl,deepseek,go-http,httrack,meta,mj12,paloalto,python,semrush,wget');

-- BlockBadBots: toggle to enable/disable bot blocking (default: enabled)
INSERT OR IGNORE INTO config (key, value) VALUES ('BlockBadBots', 'true');