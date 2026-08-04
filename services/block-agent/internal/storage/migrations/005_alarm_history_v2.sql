CREATE TABLE IF NOT EXISTS alarm_history_v2 (
    history_cursor INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_record_id TEXT NOT NULL UNIQUE CHECK (length(alarm_record_id) > 0),
    alarm_id TEXT NOT NULL CHECK (length(alarm_id) > 0),
    event_kind TEXT NOT NULL CHECK (event_kind IN ('RAISED', 'CLEARED')),
    code TEXT NOT NULL CHECK (length(code) > 0),
    severity TEXT NOT NULL CHECK (length(severity) > 0),
    text TEXT NOT NULL CHECK (length(text) > 0),
    occurred_at TEXT NOT NULL,
    details_json TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS alarm_history_v2_occurred_cursor
    ON alarm_history_v2(occurred_at, history_cursor);
