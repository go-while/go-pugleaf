-- go-pugleaf: Create system status table for tracking application state

CREATE TABLE IF NOT EXISTS system_status (
    id INTEGER PRIMARY KEY CHECK (id = 1), -- Singleton table, only one row allowed
    shutdown_state TEXT NOT NULL DEFAULT 'running', -- 'running', 'shutting_down', 'clean_shutdown', 'crashed'
    shutdown_started_at DATETIME,
    shutdown_completed_at DATETIME,
    app_version TEXT,
    pid INTEGER,
    hostname TEXT,
    last_heartbeat DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Create index for efficient queries
CREATE INDEX IF NOT EXISTS idx_system_status_shutdown_state ON system_status(shutdown_state);

-- Insert initial record if not exists
INSERT OR IGNORE INTO system_status (id, shutdown_state, app_version, pid, hostname)
VALUES (1, 'running', '', 0, '');
