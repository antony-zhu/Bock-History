CREATE TABLE IF NOT EXISTS uplink_stream_state (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    stream_generation TEXT NOT NULL,
    stream_epoch TEXT NOT NULL,
    epoch_started_at TEXT NOT NULL,
    next_sequence INTEGER NOT NULL CHECK (next_sequence >= 1),
    last_acked_sequence INTEGER NOT NULL DEFAULT 0,
    last_sync_revision INTEGER NOT NULL DEFAULT 0,
    last_sync_id TEXT NOT NULL DEFAULT '',
    last_sync_digest TEXT NOT NULL DEFAULT '',
    last_sync_json BLOB NOT NULL DEFAULT '',
    last_snapshot_fingerprint TEXT NOT NULL DEFAULT '',
    last_snapshot_at TEXT NOT NULL DEFAULT '',
    storage_status TEXT NOT NULL DEFAULT 'OK'
);

CREATE TABLE IF NOT EXISTS uplink_outbox (
    sequence INTEGER PRIMARY KEY CHECK (sequence >= 1),
    message_id TEXT NOT NULL UNIQUE,
    stream_epoch TEXT NOT NULL,
    channel TEXT NOT NULL CHECK (channel IN ('snapshot', 'event', 'alarm')),
    occurred_at TEXT NOT NULL,
    message_json BLOB NOT NULL,
    identity_digest TEXT NOT NULL,
    retention_class TEXT NOT NULL CHECK (retention_class IN ('snapshot', 'production', 'alarm', 'gap')),
    calibration INTEGER NOT NULL DEFAULT 0,
    logical_bytes INTEGER NOT NULL CHECK (logical_bytes > 0),
    publish_attempts INTEGER NOT NULL DEFAULT 0,
    last_publish_at TEXT
);

CREATE INDEX IF NOT EXISTS uplink_outbox_replay_order
    ON uplink_outbox(stream_epoch, sequence);
CREATE INDEX IF NOT EXISTS uplink_outbox_eviction
    ON uplink_outbox(retention_class, calibration DESC, sequence);

CREATE TABLE IF NOT EXISTS uplink_gap_ledger (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    stream_epoch TEXT NOT NULL,
    from_sequence INTEGER NOT NULL CHECK (from_sequence >= 1),
    to_sequence INTEGER NOT NULL CHECK (to_sequence >= from_sequence),
    reason TEXT NOT NULL CHECK (reason IN ('outbox_capacity', 'local_retention')),
    accepted INTEGER NOT NULL DEFAULT 0,
    reported INTEGER NOT NULL DEFAULT 0,
    logical_bytes INTEGER NOT NULL CHECK (logical_bytes > 0)
);

CREATE INDEX IF NOT EXISTS uplink_gap_pending_order
    ON uplink_gap_ledger(stream_epoch, accepted, from_sequence);
