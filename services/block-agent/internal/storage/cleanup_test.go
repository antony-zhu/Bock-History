package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

var retiredDeliveryTables = []string{
	"mqtt_outbound_inflight",
	"uplink_gap_ledger",
	"uplink_outbox",
	"uplink_stream_state",
}

func TestCleanupMigrationDropsRetiredDeliveryTablesAndPreservesBusinessData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "block.db")
	first, err := Open(path, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	for _, statement := range []string{
		`INSERT INTO current_snapshot (
			singleton_id, state_json, simulator_session_id, sample_sequence, control_revision,
			source_generated_at, received_at, quality, plc_connected, stale
		) VALUES (1, '{}', 'session-1', 1, 7, '2026-08-08T00:00:00Z', '2026-08-08T00:00:00Z', 'GOOD', 1, 0)`,
		`INSERT INTO local_accounts (username, password_hash, role) VALUES ('operator-1', 'hash', 'OPERATOR')`,
		`UPDATE local_system_settings SET idle_timeout_seconds = 240 WHERE singleton_id = 1`,
		`INSERT INTO command_records (command_id, fingerprint, name, status, operator, request_id, result_json, created_at, updated_at)
			VALUES ('command-1', 'fingerprint-1', 'pause', 'EXECUTED', 'operator-1', 'request-1', '{}', '2026-08-08T00:00:00Z', '2026-08-08T00:00:00Z')`,
		`INSERT INTO alarm_history_v2 (alarm_record_id, alarm_id, event_kind, code, severity, text, occurred_at, details_json)
			VALUES ('record-1', 'alarm-1', 'RAISED', 'E_STOP', 'danger', 'Emergency stop', '2026-08-08T00:00:00Z', '{}')`,
		`INSERT INTO alarm_history (alarm_id, occurred_at, data_json)
			VALUES (1, '2026-08-08T00:00:00Z', '{}')`,
	} {
		if _, err := first.db.ExecContext(ctx, statement); err != nil {
			_ = first.Close()
			t.Fatal(err)
		}
	}
	for _, table := range retiredDeliveryTables {
		if _, err := first.db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
			_ = first.Close()
			t.Fatal(err)
		}
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	for _, table := range retiredDeliveryTables {
		if cleanupTestTableExists(t, ctx, second, table) {
			t.Fatalf("retired table %s still exists", table)
		}
	}

	var revision int
	if err := second.db.QueryRowContext(ctx, `SELECT control_revision FROM current_snapshot WHERE singleton_id = 1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("control revision = %d, want 7", revision)
	}

	var username, role string
	if err := second.db.QueryRowContext(ctx, `SELECT username, role FROM local_accounts`).Scan(&username, &role); err != nil {
		t.Fatal(err)
	}
	if username != "operator-1" || role != "OPERATOR" {
		t.Fatalf("account = %q/%q, want operator-1/OPERATOR", username, role)
	}

	var idleTimeout int
	if err := second.db.QueryRowContext(ctx, `SELECT idle_timeout_seconds FROM local_system_settings WHERE singleton_id = 1`).Scan(&idleTimeout); err != nil {
		t.Fatal(err)
	}
	if idleTimeout != 240 {
		t.Fatalf("idle timeout = %d, want 240", idleTimeout)
	}

	var fingerprint, status string
	if err := second.db.QueryRowContext(ctx, `SELECT fingerprint, status FROM command_records WHERE command_id = 'command-1'`).Scan(&fingerprint, &status); err != nil {
		t.Fatal(err)
	}
	if fingerprint != "fingerprint-1" || status != "EXECUTED" {
		t.Fatalf("command = %q/%q, want fingerprint-1/EXECUTED", fingerprint, status)
	}

	var alarmRecordID string
	if err := second.db.QueryRowContext(ctx, `SELECT alarm_record_id FROM alarm_history_v2 WHERE alarm_id = 'alarm-1'`).Scan(&alarmRecordID); err != nil {
		t.Fatal(err)
	}
	if alarmRecordID != "record-1" {
		t.Fatalf("alarm record = %q, want record-1", alarmRecordID)
	}

	var alarmHistoryCount int
	if err := second.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alarm_history WHERE alarm_id = 1`).Scan(&alarmHistoryCount); err != nil {
		t.Fatal(err)
	}
	if alarmHistoryCount != 1 {
		t.Fatalf("alarm history count = %d, want 1", alarmHistoryCount)
	}
}

func TestNewStoreDoesNotCreateRetiredDeliveryTables(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "block.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, table := range retiredDeliveryTables {
		if cleanupTestTableExists(t, ctx, store, table) {
			t.Fatalf("new database created retired table %s", table)
		}
	}
	for _, table := range []string{"current_snapshot", "local_accounts", "alarm_history_v2"} {
		if !cleanupTestTableExists(t, ctx, store, table) {
			t.Fatalf("new database is missing business table %s", table)
		}
	}
}

func cleanupTestTableExists(t *testing.T, ctx context.Context, store *Store, table string) bool {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count != 0
}
