package storage

import (
	"context"
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

	for _, table := range []string{"active_alarms", "current_snapshot", "uplink_outbox"} {
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
