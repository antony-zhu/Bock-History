package storage

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/state"
	"block.local/block-agent/internal/uplink"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

var (
	ErrNoSnapshot          = errors.New("no persisted snapshot")
	ErrIdempotencyConflict = errors.New("commandId is already bound to different command content")
	ErrStaleSnapshot       = errors.New("snapshot is older than the persisted source state")
)

const (
	AvailabilityBackendUnavailable = "backend_unavailable"
	AvailabilityDeviceUnavailable  = "device_unavailable"
	AvailabilityBadQuality         = "bad_quality"
	AvailabilityDataStale          = "data_stale"
)

type Store struct {
	db     *sql.DB
	now    func() time.Time
	uplink UplinkOptions

	snapshotMu sync.Mutex
	healthMu   sync.RWMutex
	available  bool
	healthCode string

	// afterCompleteCommit is a test failpoint used to model a commit that
	// reached durable storage but whose acknowledgement was lost.
	afterCompleteCommit func() error
}

type UplinkOptions struct {
	Enabled          bool
	Source           uplink.Source
	BootID           string
	StreamGeneration string
	StaleAfter       time.Duration
	OrdinaryLimit    int64
	HardLimit        int64
}

type SnapshotRecord struct {
	State state.Model
	Meta  state.SourceMeta
	Stale bool
}

type CommandRecord struct {
	Exists      bool
	Fingerprint string
	Result      plccontract.CommandResult
}

type CommandMeta struct {
	Operator  string
	RequestID string
}

type AuditInput struct {
	Operator  string
	Action    string
	Message   string
	Revision  uint64
	RequestID string
	Details   map[string]any
}

type OperationInput struct {
	Level string
	Code  string
	Text  string
	At    time.Time
}

type AuditEntry struct {
	ID        uint64         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Operator  string         `json:"operator"`
	Action    string         `json:"action"`
	Message   string         `json:"message"`
	Revision  uint64         `json:"revision"`
	RequestID string         `json:"requestId,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

type AuditPage struct {
	Items        []AuditEntry
	NextBeforeID *uint64
}

func Open(path string, now func() time.Time) (*Store, error) {
	return OpenWithOptions(path, now, UplinkOptions{})
}

func OpenWithOptions(path string, now func() time.Time, uplinkOptions UplinkOptions) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	if uplinkOptions.OrdinaryLimit == 0 {
		uplinkOptions.OrdinaryLimit = 2*1024*1024*1024 - 64*1024*1024
	}
	if uplinkOptions.HardLimit == 0 {
		uplinkOptions.HardLimit = 2 * 1024 * 1024 * 1024
	}
	if uplinkOptions.OrdinaryLimit < 1 || uplinkOptions.HardLimit <= uplinkOptions.OrdinaryLimit {
		return nil, errors.New("uplink storage limits are invalid")
	}
	if uplinkOptions.Enabled && uplinkOptions.StaleAfter <= 0 {
		return nil, errors.New("uplink stale threshold must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, now: now, uplink: uplinkOptions, healthCode: AvailabilityBackendUnavailable}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("enable SQLite WAL: %w", err)
	}
	if journalMode != "wal" {
		return fmt.Errorf("SQLite journal mode is %q, want wal", journalMode)
	}
	for _, migration := range []string{
		"001_init.sql", "002_uplink.sql", "003_mqtt_inflight.sql", "004_auth.sql",
	} {
		contents, err := migrations.ReadFile("migrations/" + migration)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply SQLite migration %s: %w", migration, err)
		}
	}
	if err := s.ensureCommandMetadataColumns(ctx); err != nil {
		return fmt.Errorf("migrate command metadata columns: %w", err)
	}
	if err := s.RecoverPending(ctx); err != nil {
		return err
	}
	if s.uplink.Enabled {
		if err := s.initializeUplink(ctx); err != nil {
			return fmt.Errorf("initialize BDM uplink: %w", err)
		}
	}
	return nil
}

func (s *Store) ensureCommandMetadataColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(command_records)")
	if err != nil {
		return err
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !columns["operator"] {
		if _, err := s.db.ExecContext(ctx, "ALTER TABLE command_records ADD COLUMN operator TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if !columns["request_id"] {
		if _, err := s.db.ExecContext(ctx, "ALTER TABLE command_records ADD COLUMN request_id TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error {
	s.SetSourceUnavailable(AvailabilityBackendUnavailable)
	return s.db.Close()
}

func (s *Store) JournalMode(ctx context.Context) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&value)
	return value, err
}

// SaveSnapshot is retained for focused storage tests. Production PLC updates
// use SavePLC or CompleteCommand so Load -> transform -> Save is serialized.
func (s *Store) SaveSnapshot(ctx context.Context, record SnapshotRecord) error {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	if err := s.saveSnapshotLocked(ctx, record); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return err
	}
	if record.Stale || !record.Meta.PLCConnected || record.Meta.Quality != plccontract.QualityGood {
		s.SetSourceUnavailable(availabilityCode(record, s.now().UTC(), time.Duration(1<<63-1)))
	} else {
		s.setSourceAvailable()
	}
	return nil
}

func (s *Store) saveSnapshotLocked(ctx context.Context, record SnapshotRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	previous, previousErr := loadSnapshotQuery(ctx, tx)
	if previousErr != nil && !errors.Is(previousErr, ErrNoSnapshot) {
		return previousErr
	}
	if err := saveSnapshotTx(ctx, tx, record); err != nil {
		return err
	}
	var previousPointer *SnapshotRecord
	if previousErr == nil {
		previousPointer = &previous
	}
	if err := s.enqueueSnapshotChangesTx(ctx, tx, previousPointer, record, s.now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// SavePLC is the single periodic sampling writer. It rejects a late read that
// would move the persisted control revision or same-session sequence backward.
func (s *Store) SavePLC(ctx context.Context, snapshot plccontract.Snapshot, receivedAt time.Time, staleAfter time.Duration) (SnapshotRecord, error) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return SnapshotRecord{}, err
	}
	defer tx.Rollback()
	previous, previousErr := loadSnapshotQuery(ctx, tx)
	if previousErr != nil && !errors.Is(previousErr, ErrNoSnapshot) {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return SnapshotRecord{}, previousErr
	}
	record, err := mergeSnapshot(snapshot, previous, previousErr == nil, receivedAt, staleAfter)
	if err != nil {
		return SnapshotRecord{}, err
	}
	if err := saveSnapshotTx(ctx, tx, record); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return SnapshotRecord{}, err
	}
	var previousPointer *SnapshotRecord
	if previousErr == nil {
		previousPointer = &previous
	}
	if err := s.enqueueSnapshotChangesTx(ctx, tx, previousPointer, record, receivedAt.UTC()); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return SnapshotRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return SnapshotRecord{}, err
	}
	s.setAvailabilityFromRecord(record, receivedAt, staleAfter)
	return record, nil
}

func saveSnapshotTx(ctx context.Context, tx *sql.Tx, record SnapshotRecord) error {
	stateJSON, err := json.Marshal(record.State)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO current_snapshot (
			singleton_id, state_json, simulator_session_id, sample_sequence,
			control_revision, source_generated_at, received_at, quality,
			plc_connected, stale
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(singleton_id) DO UPDATE SET
			state_json=excluded.state_json,
			simulator_session_id=excluded.simulator_session_id,
			sample_sequence=excluded.sample_sequence,
			control_revision=excluded.control_revision,
			source_generated_at=excluded.source_generated_at,
			received_at=excluded.received_at,
			quality=excluded.quality,
			plc_connected=excluded.plc_connected,
			stale=excluded.stale`,
		string(stateJSON), record.Meta.SimulatorSessionID, record.Meta.SampleSequence,
		record.State.Revision, record.State.UpdatedAt.Format(time.RFC3339Nano),
		record.Meta.ReceivedAt.Format(time.RFC3339Nano), string(record.Meta.Quality),
		boolInt(record.Meta.PLCConnected), boolInt(record.Stale))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM active_alarms"); err != nil {
		return err
	}
	for _, item := range record.State.Alarms {
		contents, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO active_alarms (alarm_id, data_json, occurred_at, acknowledged)
			VALUES (?, ?, ?, ?)`, item.ID, string(contents), item.OccurredAt.Format(time.RFC3339Nano), boolInt(item.Acknowledged)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO alarm_history (alarm_id, occurred_at, data_json)
			VALUES (?, ?, ?)`, item.ID, item.OccurredAt.Format(time.RFC3339Nano), string(contents)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) LoadSnapshot(ctx context.Context) (SnapshotRecord, error) {
	return loadSnapshotQuery(ctx, s.db)
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadSnapshotQuery(ctx context.Context, queryer rowQueryer) (SnapshotRecord, error) {
	var (
		stateJSON, generatedAt, receivedAt, quality string
		plcConnected, stale                         int
		record                                      SnapshotRecord
	)
	err := queryer.QueryRowContext(ctx, `
		SELECT state_json, simulator_session_id, sample_sequence,
		       source_generated_at, received_at, quality, plc_connected, stale
		FROM current_snapshot WHERE singleton_id = 1`).Scan(
		&stateJSON, &record.Meta.SimulatorSessionID, &record.Meta.SampleSequence,
		&generatedAt, &receivedAt, &quality, &plcConnected, &stale)
	if errors.Is(err, sql.ErrNoRows) {
		return SnapshotRecord{}, ErrNoSnapshot
	}
	if err != nil {
		return SnapshotRecord{}, err
	}
	if err := json.Unmarshal([]byte(stateJSON), &record.State); err != nil {
		return SnapshotRecord{}, fmt.Errorf("decode persisted snapshot: %w", err)
	}
	if record.State.UpdatedAt, err = time.Parse(time.RFC3339Nano, generatedAt); err != nil {
		return SnapshotRecord{}, err
	}
	if record.Meta.ReceivedAt, err = time.Parse(time.RFC3339Nano, receivedAt); err != nil {
		return SnapshotRecord{}, err
	}
	record.Meta.Quality = plccontract.Quality(quality)
	record.Meta.PLCConnected = plcConnected != 0
	record.Stale = stale != 0
	return record, nil
}

func mergeSnapshot(snapshot plccontract.Snapshot, previous SnapshotRecord, hasPrevious bool, receivedAt time.Time, staleAfter time.Duration) (SnapshotRecord, error) {
	if snapshot.SchemaVersion != plccontract.SchemaVersion || snapshot.SimulatorSessionID == "" || snapshot.GeneratedAt.IsZero() {
		return SnapshotRecord{}, errors.New("invalid PLC snapshot identity, schema or timestamp")
	}
	if hasPrevious {
		if snapshot.Points.ControlRevision < previous.State.Revision {
			return SnapshotRecord{}, ErrStaleSnapshot
		}
		if snapshot.SimulatorSessionID == previous.Meta.SimulatorSessionID &&
			snapshot.Points.ControlRevision == previous.State.Revision &&
			snapshot.SampleSequence < previous.Meta.SampleSequence {
			return SnapshotRecord{}, ErrStaleSnapshot
		}
		if snapshot.SimulatorSessionID != previous.Meta.SimulatorSessionID &&
			snapshot.Points.ControlRevision == previous.State.Revision &&
			snapshot.GeneratedAt.Before(previous.State.UpdatedAt) {
			return SnapshotRecord{}, ErrStaleSnapshot
		}
	}
	next, source := state.FromPLC(snapshot, previous.State)
	source.ReceivedAt = receivedAt.UTC()
	record := SnapshotRecord{State: next, Meta: source}
	record.Stale = availabilityCode(record, receivedAt.UTC(), staleAfter) != ""
	return record, nil
}

func (s *Store) MarkStale(ctx context.Context, code string) error {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return err
	}
	defer tx.Rollback()
	previous, err := loadSnapshotQuery(ctx, tx)
	if errors.Is(err, ErrNoSnapshot) {
		s.SetSourceUnavailable(code)
		return tx.Commit()
	}
	if err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return err
	}
	current := previous
	current.Stale = true
	if _, err := tx.ExecContext(ctx, "UPDATE current_snapshot SET stale = 1 WHERE singleton_id = 1"); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return err
	}
	if err := s.enqueueSnapshotChangesTx(ctx, tx, &previous, current, s.now().UTC()); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return err
	}
	if err := tx.Commit(); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return err
	}
	s.SetSourceUnavailable(code)
	return nil
}

func (s *Store) BeginCommand(ctx context.Context, command plccontract.Command, meta CommandMeta) (CommandRecord, error) {
	command = plccontract.NormalizeCommand(command)
	fingerprint := plccontract.CommandFingerprint(command)
	var storedFingerprint, status string
	var resultJSON sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT fingerprint, status, result_json FROM command_records WHERE command_id = ?", command.CommandID).Scan(&storedFingerprint, &status, &resultJSON)
	if err == nil {
		if storedFingerprint != fingerprint {
			return CommandRecord{}, ErrIdempotencyConflict
		}
		result := plccontract.CommandResult{CommandID: command.CommandID, Name: command.Name, Status: plccontract.CommandStatus(status)}
		if resultJSON.Valid && resultJSON.String != "" {
			if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
				return CommandRecord{}, fmt.Errorf("decode command record %s: %w", command.CommandID, err)
			}
		}
		return CommandRecord{Exists: true, Fingerprint: storedFingerprint, Result: result}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CommandRecord{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO command_records (
			command_id, fingerprint, name, status, operator, request_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		command.CommandID, fingerprint, command.Name, plccontract.CommandPending,
		meta.Operator, meta.RequestID, now, now)
	if err != nil {
		return CommandRecord{}, err
	}
	return CommandRecord{Fingerprint: fingerprint}, nil
}

// CompleteCommand atomically transitions one PENDING record to its terminal
// outcome with exactly one operation and audit record. Repeating the same
// completion after an uncertain commit is a read-only success. A late command
// readback never overwrites a newer periodic sample.
func (s *Store) CompleteCommand(ctx context.Context, result plccontract.CommandResult, readback *plccontract.Snapshot, receivedAt time.Time, staleAfter time.Duration, operation OperationInput, audit AuditInput) (*SnapshotRecord, error) {
	if !terminalStatus(result.Status) {
		return nil, fmt.Errorf("command result status %q is not terminal", result.Status)
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return nil, err
	}
	defer tx.Rollback()

	var storedName, storedStatus string
	var storedResultJSON sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT name, status, result_json FROM command_records WHERE command_id = ?`,
		result.CommandID).Scan(&storedName, &storedStatus, &storedResultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("command record %s does not exist", result.CommandID)
	}
	if err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return nil, err
	}
	storedCommandStatus := plccontract.CommandStatus(storedStatus)
	if storedCommandStatus != plccontract.CommandPending {
		if !terminalStatus(storedCommandStatus) {
			return nil, fmt.Errorf("command record %s has invalid status %q", result.CommandID, storedStatus)
		}
		if !storedResultJSON.Valid || storedResultJSON.String == "" {
			return nil, fmt.Errorf("terminal command record %s has no result", result.CommandID)
		}
		var storedResult plccontract.CommandResult
		if err := json.Unmarshal([]byte(storedResultJSON.String), &storedResult); err != nil {
			return nil, fmt.Errorf("decode terminal command record %s: %w", result.CommandID, err)
		}
		if storedResult.CommandID != result.CommandID || storedName != result.Name ||
			storedResult.Name != result.Name || storedResult.Status != result.Status {
			return nil, fmt.Errorf(
				"command record %s already completed as %s/%s",
				result.CommandID, storedResult.Name, storedResult.Status,
			)
		}
		if storedResult.Status != storedCommandStatus {
			return nil, fmt.Errorf("command record %s status and result disagree", result.CommandID)
		}
		current, currentErr := loadSnapshotQuery(ctx, tx)
		if errors.Is(currentErr, ErrNoSnapshot) {
			return nil, nil
		}
		if currentErr != nil {
			s.SetSourceUnavailable(AvailabilityBackendUnavailable)
			return nil, currentErr
		}
		s.setAvailabilityFromRecord(current, receivedAt, staleAfter)
		copy := current
		return &copy, nil
	}

	current, currentErr := loadSnapshotQuery(ctx, tx)
	hasCurrent := currentErr == nil
	if currentErr != nil && !errors.Is(currentErr, ErrNoSnapshot) {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return nil, currentErr
	}
	previous := current
	hadPrevious := hasCurrent
	acceptedReadback := false
	if readback != nil {
		merged, mergeErr := mergeSnapshot(*readback, current, hasCurrent, receivedAt, staleAfter)
		switch {
		case mergeErr == nil:
			current, hasCurrent, acceptedReadback = merged, true, true
		case errors.Is(mergeErr, ErrStaleSnapshot) && hasCurrent:
			// Preserve the newer state already committed by periodic sampling.
		default:
			return nil, mergeErr
		}
	}
	if hasCurrent && operation.Text != "" {
		current.State.AddOperation(operation.Level, operation.Code, operation.Text, operation.At)
	}
	if hasCurrent && (acceptedReadback || operation.Text != "") {
		if err := saveSnapshotTx(ctx, tx, current); err != nil {
			s.SetSourceUnavailable(AvailabilityBackendUnavailable)
			return nil, err
		}
		var previousPointer *SnapshotRecord
		if hadPrevious {
			previousPointer = &previous
		}
		if err := s.enqueueSnapshotChangesTx(ctx, tx, previousPointer, current, receivedAt.UTC()); err != nil {
			s.SetSourceUnavailable(AvailabilityBackendUnavailable)
			return nil, err
		}
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	updatedAt := s.now().UTC()
	update, err := tx.ExecContext(ctx, `
		UPDATE command_records SET status = ?, result_json = ?, updated_at = ?
		WHERE command_id = ? AND status = ?`,
		result.Status, string(resultJSON), updatedAt.Format(time.RFC3339Nano),
		result.CommandID, plccontract.CommandPending)
	if err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return nil, err
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return nil, fmt.Errorf("command record %s did not transition from PENDING", result.CommandID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO operation_history (occurred_at, command_id, action, status, message)
		VALUES (?, ?, ?, ?, ?)`, updatedAt.Format(time.RFC3339Nano), result.CommandID, result.Name, result.Status, audit.Message); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return nil, err
	}
	if hasCurrent {
		audit.Revision = current.State.Revision
	}
	if err := insertAudit(ctx, tx, updatedAt, audit); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		s.SetSourceUnavailable(AvailabilityBackendUnavailable)
		return nil, err
	}
	if s.afterCompleteCommit != nil {
		if err := s.afterCompleteCommit(); err != nil {
			s.SetSourceUnavailable(AvailabilityBackendUnavailable)
			return nil, err
		}
	}
	if acceptedReadback {
		s.setAvailabilityFromRecord(current, receivedAt, staleAfter)
	}
	if !hasCurrent {
		return nil, nil
	}
	copy := current
	return &copy, nil
}

func terminalStatus(status plccontract.CommandStatus) bool {
	switch status {
	case plccontract.CommandExecuted, plccontract.CommandRejected,
		plccontract.CommandFailed, plccontract.CommandUnknown:
		return true
	default:
		return false
	}
}

func insertAudit(ctx context.Context, tx *sql.Tx, at time.Time, audit AuditInput) error {
	detailsJSON, err := json.Marshal(audit.Details)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_records (
			occurred_at, operator, action, message, revision, request_id, details_json
		) VALUES (?, ?, ?, ?, ?, ?, ?)`, at.Format(time.RFC3339Nano), audit.Operator,
		audit.Action, audit.Message, audit.Revision, audit.RequestID, string(detailsJSON))
	return err
}

func (s *Store) RecoverPending(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT command_id, fingerprint, name, operator, request_id
		FROM command_records WHERE status = ?`, plccontract.CommandPending)
	if err != nil {
		return err
	}
	type pendingCommand struct {
		result      plccontract.CommandResult
		fingerprint string
		meta        CommandMeta
	}
	var pending []pendingCommand
	for rows.Next() {
		var item pendingCommand
		if err := rows.Scan(
			&item.result.CommandID, &item.fingerprint, &item.result.Name,
			&item.meta.Operator, &item.meta.RequestID,
		); err != nil {
			_ = rows.Close()
			return err
		}
		item.result.Status = plccontract.CommandUnknown
		item.result.Code = plccontract.ResultCodeOutcomeUnknown
		item.result.Reason = "agent restarted before command outcome was recorded"
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range pending {
		contents, err := json.Marshal(item.result)
		if err != nil {
			return err
		}
		recoveredAt := s.now().UTC()
		message := "UNKNOWN: " + item.result.Reason
		update, err := tx.ExecContext(ctx, `
			UPDATE command_records SET status = ?, result_json = ?, updated_at = ?
			WHERE command_id = ? AND status = ?`,
			item.result.Status, string(contents), recoveredAt.Format(time.RFC3339Nano),
			item.result.CommandID, plccontract.CommandPending)
		if err != nil {
			return err
		}
		if affected, _ := update.RowsAffected(); affected != 1 {
			return fmt.Errorf("pending command record %s changed during recovery", item.result.CommandID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO operation_history (occurred_at, command_id, action, status, message)
			VALUES (?, ?, ?, ?, ?)`,
			recoveredAt.Format(time.RFC3339Nano), item.result.CommandID,
			item.result.Name, item.result.Status, message); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, recoveredAt, AuditInput{
			Operator: item.meta.Operator, Action: "command." + item.result.Name,
			Message: message, RequestID: item.meta.RequestID,
			Details: map[string]any{
				"commandId": item.result.CommandID, "status": item.result.Status,
				"code": item.result.Code, "fingerprint": item.fingerprint, "recovered": true,
			},
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Audit(ctx context.Context, limit int, beforeID *uint64) (AuditPage, error) {
	query := `SELECT id, occurred_at, operator, action, message, revision, request_id, details_json
		FROM audit_records`
	args := []any{}
	if beforeID != nil {
		query += " WHERE id < ?"
		args = append(args, *beforeID)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()
	page := AuditPage{Items: make([]AuditEntry, 0, limit)}
	for rows.Next() {
		var item AuditEntry
		var occurredAt, detailsJSON string
		if err := rows.Scan(&item.ID, &occurredAt, &item.Operator, &item.Action, &item.Message, &item.Revision, &item.RequestID, &detailsJSON); err != nil {
			return AuditPage{}, err
		}
		item.Timestamp, err = time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return AuditPage{}, err
		}
		if detailsJSON != "" && detailsJSON != "null" {
			if err := json.Unmarshal([]byte(detailsJSON), &item.Details); err != nil {
				return AuditPage{}, err
			}
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		next := page.Items[len(page.Items)-1].ID
		page.NextBeforeID = &next
	}
	return page, rows.Err()
}

func (s *Store) SetSourceUnavailable(code string) {
	if code == "" {
		code = AvailabilityBackendUnavailable
	}
	s.healthMu.Lock()
	s.available = false
	s.healthCode = code
	s.healthMu.Unlock()
}

func (s *Store) SourceAvailability() (bool, string) {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	return s.available, s.healthCode
}

func (s *Store) setSourceAvailable() {
	s.healthMu.Lock()
	s.available = true
	s.healthCode = ""
	s.healthMu.Unlock()
}

func (s *Store) setAvailabilityFromRecord(record SnapshotRecord, now time.Time, staleAfter time.Duration) {
	if code := availabilityCode(record, now.UTC(), staleAfter); code != "" {
		s.SetSourceUnavailable(code)
		return
	}
	s.setSourceAvailable()
}

func Freshness(record SnapshotRecord, now time.Time, staleAfter time.Duration) (bool, string) {
	if code := availabilityCode(record, now.UTC(), staleAfter); code != "" {
		return false, code
	}
	return true, ""
}

func availabilityCode(record SnapshotRecord, now time.Time, staleAfter time.Duration) string {
	if !record.Meta.PLCConnected {
		return AvailabilityDeviceUnavailable
	}
	if record.Meta.Quality != plccontract.QualityGood {
		return AvailabilityBadQuality
	}
	if record.Stale || record.Meta.ReceivedAt.IsZero() || record.State.UpdatedAt.IsZero() ||
		record.Meta.ReceivedAt.After(now) || record.State.UpdatedAt.After(now) ||
		now.Sub(record.Meta.ReceivedAt) >= staleAfter || now.Sub(record.State.UpdatedAt) >= staleAfter {
		return AvailabilityDataStale
	}
	return ""
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
