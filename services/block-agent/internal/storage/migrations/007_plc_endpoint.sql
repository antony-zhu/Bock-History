CREATE TABLE IF NOT EXISTS plc_endpoint (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    host TEXT NOT NULL CHECK (length(host) > 0),
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    unit_id INTEGER NOT NULL CHECK (unit_id BETWEEN 1 AND 247)
);
