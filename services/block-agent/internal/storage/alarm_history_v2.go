package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"block.local/block-agent/internal/alarmhistory"
)

var (
	ErrInvalidAlarmHistoryRecord = errors.New("alarm history record is invalid")

	_ alarmhistory.Store = (*Store)(nil)
)

const alarmHistoryTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Append records one immutable alarm transition. It does not persist current
// active alarms and leaves notification to the alarm history service.
func (s *Store) Append(ctx context.Context, record alarmhistory.Record) (alarmhistory.Record, error) {
	if err := validateAlarmHistoryRecord(record); err != nil {
		return alarmhistory.Record{}, err
	}
	detailsJSON, err := json.Marshal(record.Details)
	if err != nil {
		return alarmhistory.Record{}, fmt.Errorf("encode alarm history details: %w", err)
	}

	occurredAt := record.OccurredAt.UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO alarm_history_v2 (
			alarm_record_id, alarm_id, event_kind, code, severity, text,
			occurred_at, details_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		record.AlarmRecordID, record.AlarmID, record.EventKind, record.Code,
		record.Severity, record.Text, occurredAt.Format(alarmHistoryTimeLayout), string(detailsJSON),
	)
	if err != nil {
		return alarmhistory.Record{}, fmt.Errorf("append alarm history: %w", err)
	}
	cursor, err := result.LastInsertId()
	if err != nil {
		return alarmhistory.Record{}, fmt.Errorf("read alarm history cursor: %w", err)
	}
	if cursor < 1 {
		return alarmhistory.Record{}, errors.New("persisted alarm history cursor is invalid")
	}

	record.HistoryCursor = uint64(cursor)
	record.OccurredAt = occurredAt
	return record, nil
}

// List returns immutable alarm-history records in stable cursor order. The
// caller derives nextCursor from the final record when hasMore is true.
func (s *Store) List(
	ctx context.Context,
	query alarmhistory.Query,
) ([]alarmhistory.Record, bool, error) {
	if err := query.Validate(); err != nil {
		return nil, false, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			history_cursor, alarm_record_id, alarm_id, event_kind, code, severity,
			text, occurred_at, details_json
		FROM alarm_history_v2
		WHERE occurred_at >= ? AND occurred_at < ? AND history_cursor > ?
		ORDER BY history_cursor ASC
		LIMIT ?`,
		query.FromOccurredAt.UTC().Format(alarmHistoryTimeLayout),
		query.ToOccurredAt.UTC().Format(alarmHistoryTimeLayout),
		query.AfterCursor,
		query.Limit+1,
	)
	if err != nil {
		return nil, false, fmt.Errorf("list alarm history: %w", err)
	}
	defer rows.Close()

	records := make([]alarmhistory.Record, 0, query.Limit+1)
	for rows.Next() {
		record, err := scanAlarmHistoryRecord(rows)
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(records) > query.Limit
	if hasMore {
		records = records[:query.Limit]
	}
	return records, hasMore, nil
}

func validateAlarmHistoryRecord(record alarmhistory.Record) error {
	if record.AlarmRecordID == "" ||
		record.AlarmID == "" ||
		record.Code == "" ||
		record.Severity == "" ||
		record.Text == "" ||
		record.OccurredAt.IsZero() {
		return ErrInvalidAlarmHistoryRecord
	}
	switch record.EventKind {
	case "RAISED", "CLEARED":
		return nil
	default:
		return ErrInvalidAlarmHistoryRecord
	}
}

func scanAlarmHistoryRecord(rows *sql.Rows) (alarmhistory.Record, error) {
	var (
		cursor     int64
		occurredAt string
		details    string
		record     alarmhistory.Record
	)
	if err := rows.Scan(
		&cursor, &record.AlarmRecordID, &record.AlarmID, &record.EventKind,
		&record.Code, &record.Severity, &record.Text, &occurredAt, &details,
	); err != nil {
		return alarmhistory.Record{}, err
	}
	if cursor < 1 {
		return alarmhistory.Record{}, errors.New("persisted alarm history cursor is invalid")
	}
	parsedOccurredAt, err := time.Parse(alarmHistoryTimeLayout, occurredAt)
	if err != nil {
		return alarmhistory.Record{}, fmt.Errorf("parse persisted alarm history time: %w", err)
	}
	if err := json.Unmarshal([]byte(details), &record.Details); err != nil {
		return alarmhistory.Record{}, fmt.Errorf("decode persisted alarm history details: %w", err)
	}
	record.HistoryCursor = uint64(cursor)
	record.OccurredAt = parsedOccurredAt.UTC()
	if err := validateAlarmHistoryRecord(record); err != nil {
		return alarmhistory.Record{}, fmt.Errorf("persisted %w", err)
	}
	return record, nil
}
