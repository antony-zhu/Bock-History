package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	// alarmHistoryV2LegacyTable is retained after migration as a read-only
	// archive so the historical identity, timing, and quality fields are not lost.
	alarmHistoryV2LegacyTable = "alarm_history_v2_legacy"
	alarmHistoryV2CreateTable = "CREATE TABLE alarm_history_v2 (" +
		"history_cursor INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"alarm_record_id TEXT NOT NULL UNIQUE CHECK (length(alarm_record_id) > 0), " +
		"alarm_id TEXT NOT NULL CHECK (length(alarm_id) > 0), " +
		"event_kind TEXT NOT NULL CHECK (event_kind IN ('RAISED', 'CLEARED')), " +
		"code TEXT NOT NULL CHECK (length(code) > 0), " +
		"severity TEXT NOT NULL CHECK (length(severity) > 0), " +
		"text TEXT NOT NULL CHECK (length(text) > 0), " +
		"occurred_at TEXT NOT NULL, " +
		"details_json TEXT NOT NULL)"
	// The legacy table may retain alarm_history_v2_occurred_cursor after rename,
	// so the rebuilt current table uses a distinct index name.
	alarmHistoryV2CreateIndex = "CREATE INDEX IF NOT EXISTS alarm_history_v2_current_occurred_cursor " +
		"ON alarm_history_v2(occurred_at, history_cursor)"
)

type alarmHistoryV2Column struct {
	name       string
	columnType string
	notNull    bool
	primaryKey int
}

var currentAlarmHistoryV2Columns = []alarmHistoryV2Column{
	{name: "history_cursor", columnType: "INTEGER", primaryKey: 1},
	{name: "alarm_record_id", columnType: "TEXT", notNull: true},
	{name: "alarm_id", columnType: "TEXT", notNull: true},
	{name: "event_kind", columnType: "TEXT", notNull: true},
	{name: "code", columnType: "TEXT", notNull: true},
	{name: "severity", columnType: "TEXT", notNull: true},
	{name: "text", columnType: "TEXT", notNull: true},
	{name: "occurred_at", columnType: "TEXT", notNull: true},
	{name: "details_json", columnType: "TEXT", notNull: true},
}

type alarmHistoryV2TableInfoQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) ensureAlarmHistoryV2Schema(ctx context.Context) error {
	columns, err := alarmHistoryV2TableColumns(ctx, s.db)
	if err != nil {
		return err
	}
	if alarmHistoryV2SchemaMatches(columns) {
		return nil
	}
	if !alarmHistoryV2ContainsCommonColumns(columns) {
		return fmt.Errorf("alarm_history_v2 has incompatible columns and cannot be migrated without losing history")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	columns, err = alarmHistoryV2TableColumns(ctx, tx)
	if err != nil {
		return err
	}
	if alarmHistoryV2SchemaMatches(columns) {
		return tx.Commit()
	}
	if !alarmHistoryV2ContainsCommonColumns(columns) {
		return fmt.Errorf("alarm_history_v2 has incompatible columns and cannot be migrated without losing history")
	}

	if _, err := tx.ExecContext(ctx, "ALTER TABLE alarm_history_v2 RENAME TO "+alarmHistoryV2LegacyTable); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, alarmHistoryV2CreateTable); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO alarm_history_v2 ("+
			"history_cursor, alarm_record_id, alarm_id, event_kind, code, severity, "+
			"text, occurred_at, details_json"+
			") SELECT "+
			"history_cursor, alarm_record_id, alarm_id, event_kind, code, severity, "+
			"text, occurred_at, details_json "+
			"FROM "+alarmHistoryV2LegacyTable+" ORDER BY history_cursor ASC",
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, alarmHistoryV2CreateIndex); err != nil {
		return err
	}
	return tx.Commit()
}

func alarmHistoryV2TableColumns(
	ctx context.Context,
	queryer alarmHistoryV2TableInfoQueryer,
) ([]alarmHistoryV2Column, error) {
	rows, err := queryer.QueryContext(ctx, "PRAGMA table_info(alarm_history_v2)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make([]alarmHistoryV2Column, 0, len(currentAlarmHistoryV2Columns))
	for rows.Next() {
		var (
			cid          int
			column       alarmHistoryV2Column
			defaultValue sql.NullString
		)
		if err := rows.Scan(&cid, &column.name, &column.columnType, &column.notNull, &defaultValue, &column.primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func alarmHistoryV2SchemaMatches(columns []alarmHistoryV2Column) bool {
	// This recognizes the known historical schema that had additional legacy
	// fields. It intentionally does not attempt to validate every SQL constraint
	// on a hand-authored same-shape table.
	if len(columns) != len(currentAlarmHistoryV2Columns) {
		return false
	}
	for index, expected := range currentAlarmHistoryV2Columns {
		actual := columns[index]
		if actual.name != expected.name ||
			!strings.EqualFold(strings.TrimSpace(actual.columnType), expected.columnType) ||
			actual.primaryKey != expected.primaryKey ||
			(expected.notNull && !actual.notNull) {
			return false
		}
	}
	return true
}

func alarmHistoryV2ContainsCommonColumns(columns []alarmHistoryV2Column) bool {
	found := make(map[string]bool, len(columns))
	for _, column := range columns {
		found[column.name] = true
	}
	for _, expected := range currentAlarmHistoryV2Columns {
		if !found[expected.name] {
			return false
		}
	}
	return true
}
