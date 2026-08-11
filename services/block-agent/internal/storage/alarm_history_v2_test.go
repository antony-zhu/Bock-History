package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"block.local/block-agent/internal/alarmhistory"
)

func TestAlarmHistoryV2AppendAndList(t *testing.T) {
	ctx := context.Background()
	store := openAlarmHistoryV2Store(t)
	base := time.Date(2026, 8, 5, 9, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))

	raised, err := store.Append(ctx, alarmhistory.Record{
		AlarmRecordID: "alarm-record-raised",
		AlarmID:       "alarm-7",
		EventKind:     "RAISED",
		Code:          "E_STOP",
		Severity:      "danger",
		Text:          "Emergency stop",
		OccurredAt:    base,
		Details:       map[string]any{"source": "plc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := store.Append(ctx, alarmhistory.Record{
		AlarmRecordID: "alarm-record-cleared",
		AlarmID:       "alarm-7",
		EventKind:     "CLEARED",
		Code:          "E_STOP",
		Severity:      "danger",
		Text:          "Emergency stop cleared",
		OccurredAt:    base.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if raised.HistoryCursor == 0 || cleared.HistoryCursor != raised.HistoryCursor+1 {
		t.Fatalf("stored cursors = %d, %d", raised.HistoryCursor, cleared.HistoryCursor)
	}
	if got, want := raised.OccurredAt.Location(), time.UTC; got != want {
		t.Fatalf("raised occurredAt location = %s, want UTC", got)
	}

	records, hasMore, err := store.List(ctx, alarmhistory.Query{
		FromOccurredAt: base.Add(-time.Second),
		ToOccurredAt:   base.Add(2 * time.Second),
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(records) != 2 {
		t.Fatalf("list = %+v, hasMore = %v", records, hasMore)
	}
	if records[0].HistoryCursor != raised.HistoryCursor ||
		records[1].HistoryCursor != cleared.HistoryCursor ||
		!reflect.DeepEqual(records[0].Details, map[string]any{"source": "plc"}) {
		t.Fatalf("listed records = %+v", records)
	}

	for _, table := range []string{"active_alarms", "current_snapshot"} {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want 0", table, count)
		}
	}
	var v2Tables int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name LIKE 'alarm_%_v2'`,
	).Scan(&v2Tables); err != nil {
		t.Fatal(err)
	}
	if v2Tables != 1 {
		t.Fatalf("alarm v2 table count = %d, want 1", v2Tables)
	}
}

func TestAlarmHistoryV2ListUsesTimeRangeAndCursor(t *testing.T) {
	ctx := context.Background()
	store := openAlarmHistoryV2Store(t)
	base := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)

	first := appendAlarmHistoryV2(t, store, "first", "RAISED", base)
	second := appendAlarmHistoryV2(t, store, "second", "CLEARED", base.Add(100*time.Millisecond))
	third := appendAlarmHistoryV2(t, store, "third", "RAISED", base.Add(900*time.Millisecond))
	_ = appendAlarmHistoryV2(t, store, "outside-range", "CLEARED", base.Add(time.Second))

	firstPage, hasMore, err := store.List(ctx, alarmhistory.Query{
		FromOccurredAt: base,
		ToOccurredAt:   base.Add(time.Second),
		Limit:          2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(firstPage) != 2 ||
		firstPage[0].HistoryCursor != first.HistoryCursor ||
		firstPage[1].HistoryCursor != second.HistoryCursor {
		t.Fatalf("first page = %+v, hasMore = %v", firstPage, hasMore)
	}

	secondPage, hasMore, err := store.List(ctx, alarmhistory.Query{
		FromOccurredAt: base,
		ToOccurredAt:   base.Add(time.Second),
		AfterCursor:    firstPage[len(firstPage)-1].HistoryCursor,
		Limit:          2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(secondPage) != 1 || secondPage[0].HistoryCursor != third.HistoryCursor {
		t.Fatalf("second page = %+v, hasMore = %v", secondPage, hasMore)
	}
}

func TestAlarmHistoryV2RejectsInvalidRecordAndQuery(t *testing.T) {
	ctx := context.Background()
	store := openAlarmHistoryV2Store(t)
	if _, err := store.Append(ctx, alarmhistory.Record{
		AlarmRecordID: "invalid-event",
		AlarmID:       "alarm-1",
		EventKind:     "ACTIVE",
		Code:          "E_STOP",
		Severity:      "danger",
		Text:          "invalid",
		OccurredAt:    time.Now(),
	}); !errors.Is(err, ErrInvalidAlarmHistoryRecord) {
		t.Fatalf("append invalid record error = %v", err)
	}
	if _, _, err := store.List(ctx, alarmhistory.Query{Limit: 1}); !errors.Is(err, alarmhistory.ErrInvalidQuery) {
		t.Fatalf("list invalid query error = %v", err)
	}
}

func TestOpenMigratesLegacyAlarmHistoryV2Schema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-alarm-history.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := "CREATE TABLE alarm_history_v2 (" +
		"history_cursor INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"alarm_record_id TEXT NOT NULL UNIQUE, " +
		"site_id TEXT NOT NULL, " +
		"block_id TEXT NOT NULL, " +
		"device_id TEXT NOT NULL, " +
		"alarm_id TEXT NOT NULL, " +
		"event_kind TEXT NOT NULL CHECK (event_kind IN ('RAISED', 'CLEARED')), " +
		"code TEXT NOT NULL, " +
		"severity TEXT NOT NULL, " +
		"text TEXT NOT NULL, " +
		"occurred_at TEXT NOT NULL, " +
		"occurred_unix_nano INTEGER NOT NULL, " +
		"recorded_at TEXT NOT NULL, " +
		"quality TEXT NOT NULL, " +
		"details_json TEXT NOT NULL DEFAULT '{}')"
	if _, err := legacy.ExecContext(ctx, legacySchema); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 8, 11, 8, 30, 0, 0, time.UTC)
	if _, err := legacy.ExecContext(ctx,
		"INSERT INTO alarm_history_v2 ("+
			"history_cursor, alarm_record_id, site_id, block_id, device_id, alarm_id, "+
			"event_kind, code, severity, text, occurred_at, occurred_unix_nano, "+
			"recorded_at, quality, details_json"+
			") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		41, "legacy-record", "site-lab", "block-001", "device-001", "alarm-1",
		"RAISED", "E_STOP", "CRITICAL", "legacy alarm",
		base.Format(alarmHistoryTimeLayout), base.UnixNano(),
		base.Format(alarmHistoryTimeLayout), "good", "{\"source\":\"legacy\"}",
	); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	columns, err := alarmHistoryV2TableColumns(ctx, store.db)
	if err != nil {
		t.Fatal(err)
	}
	if !alarmHistoryV2SchemaMatches(columns) {
		t.Fatalf("migrated alarm history schema = %+v", columns)
	}

	var (
		archiveSite, archiveBlock, archiveDevice string
		archiveOccurredUnixNano                  int64
		archiveRecordedAt, archiveQuality        string
	)
	if err := store.db.QueryRowContext(ctx,
		"SELECT site_id, block_id, device_id, occurred_unix_nano, recorded_at, quality "+
			"FROM "+alarmHistoryV2LegacyTable+" WHERE history_cursor = ?", 41,
	).Scan(
		&archiveSite, &archiveBlock, &archiveDevice, &archiveOccurredUnixNano,
		&archiveRecordedAt, &archiveQuality,
	); err != nil {
		t.Fatalf("read legacy alarm archive: %v", err)
	}
	if archiveSite != "site-lab" || archiveBlock != "block-001" || archiveDevice != "device-001" ||
		archiveOccurredUnixNano != base.UnixNano() || archiveRecordedAt != base.Format(alarmHistoryTimeLayout) ||
		archiveQuality != "good" {
		t.Fatalf("legacy archive fields were not preserved: site=%q block=%q device=%q unixNano=%d recordedAt=%q quality=%q",
			archiveSite, archiveBlock, archiveDevice, archiveOccurredUnixNano, archiveRecordedAt, archiveQuality)
	}

	records, hasMore, err := store.List(ctx, alarmhistory.Query{
		FromOccurredAt: base.Add(-time.Second),
		ToOccurredAt:   base.Add(time.Second),
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(records) != 1 {
		t.Fatalf("migrated records = %+v, hasMore = %v", records, hasMore)
	}
	if records[0].HistoryCursor != 41 ||
		records[0].AlarmRecordID != "legacy-record" ||
		records[0].Text != "legacy alarm" ||
		records[0].Details["source"] != "legacy" {
		t.Fatalf("legacy record was not preserved: %+v", records[0])
	}

	appended := appendAlarmHistoryV2(t, store, "after-legacy-migration", "CLEARED", base.Add(time.Millisecond))
	if appended.HistoryCursor != 42 {
		t.Fatalf("next history cursor = %d, want 42", appended.HistoryCursor)
	}
}

func openAlarmHistoryV2Store(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "block.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := migrations.ReadFile("migrations/005_alarm_history_v2.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), string(migration)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func appendAlarmHistoryV2(
	t *testing.T,
	store *Store,
	recordID string,
	eventKind string,
	occurredAt time.Time,
) alarmhistory.Record {
	t.Helper()
	record, err := store.Append(context.Background(), alarmhistory.Record{
		AlarmRecordID: recordID,
		AlarmID:       "alarm-1",
		EventKind:     eventKind,
		Code:          "E_STOP",
		Severity:      "danger",
		Text:          recordID,
		OccurredAt:    occurredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}
