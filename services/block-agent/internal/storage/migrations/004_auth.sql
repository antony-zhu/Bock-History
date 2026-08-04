CREATE TABLE IF NOT EXISTS local_accounts (
    username TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('VIEWER', 'OPERATOR', 'ADMIN'))
);

CREATE TABLE IF NOT EXISTS local_system_settings (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    idle_timeout_seconds INTEGER NOT NULL CHECK (idle_timeout_seconds BETWEEN 60 AND 3600)
);

INSERT OR IGNORE INTO local_system_settings (singleton_id, idle_timeout_seconds)
VALUES (1, 300);
