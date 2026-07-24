CREATE TABLE IF NOT EXISTS mqtt_outbound_inflight (
    packet_id INTEGER PRIMARY KEY CHECK (packet_id BETWEEN 1 AND 65535),
    topic TEXT NOT NULL CHECK (length(topic) > 0),
    payload BLOB NOT NULL,
    retain INTEGER NOT NULL CHECK (retain IN (0, 1)),
    application_key TEXT NOT NULL DEFAULT '',
    send_order INTEGER NOT NULL UNIQUE CHECK (send_order >= 1),
    ever_sent INTEGER NOT NULL CHECK (ever_sent IN (0, 1))
);

CREATE INDEX IF NOT EXISTS mqtt_outbound_inflight_order
    ON mqtt_outbound_inflight(send_order);
