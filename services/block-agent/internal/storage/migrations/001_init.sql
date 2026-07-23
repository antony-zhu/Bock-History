CREATE TABLE IF NOT EXISTS current_snapshot (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    state_json TEXT NOT NULL,
    simulator_session_id TEXT NOT NULL,
    sample_sequence INTEGER NOT NULL,
    control_revision INTEGER NOT NULL,
    source_generated_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    quality TEXT NOT NULL,
    plc_connected INTEGER NOT NULL,
    stale INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS active_alarms (
    alarm_id INTEGER PRIMARY KEY,
    data_json TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    acknowledged INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS alarm_history (
    alarm_id INTEGER NOT NULL,
    occurred_at TEXT NOT NULL,
    data_json TEXT NOT NULL,
    PRIMARY KEY (alarm_id, occurred_at)
);

CREATE TABLE IF NOT EXISTS operation_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL,
    command_id TEXT NOT NULL,
    action TEXT NOT NULL,
    status TEXT NOT NULL,
    message TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS command_records (
    command_id TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    operator TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    result_json TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL,
    operator TEXT NOT NULL,
    action TEXT NOT NULL,
    message TEXT NOT NULL,
    revision INTEGER NOT NULL,
    request_id TEXT,
    details_json TEXT
);
