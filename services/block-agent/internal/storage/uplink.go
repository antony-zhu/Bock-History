package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"block.local/block-agent/internal/uplink"
)

var (
	ErrUplinkDisabled         = errors.New("BDM uplink is disabled")
	ErrUplinkStorageExhausted = errors.New("BDM uplink storage is exhausted")
	ErrUplinkMessageTooLarge  = errors.New("BDM reliable message exceeds 64 KiB")
	ErrSyncConflict           = errors.New("sync revision conflicts with previously applied content")
	ErrInvalidSync            = errors.New("invalid BDM sync message")
	errCalibrationSuppressed  = errors.New("calibration snapshot suppressed at ordinary capacity")
)

const maxDirectReliableBytes = 64 * 1024

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type UplinkStreamState struct {
	StreamGeneration       string
	StreamEpoch            string
	EpochStartedAt         time.Time
	FirstAvailableSequence uint64
	LastProducedSequence   uint64
	LastAckedSequence      uint64
	OutboxPending          uint64
	StorageStatus          string
}

type OutboxRecord struct {
	Sequence        uint64
	MessageID       string
	StreamEpoch     string
	Channel         string
	OccurredAt      time.Time
	MessageJSON     []byte
	IdentityDigest  string
	LogicalBytes    int64
	PublishAttempts uint64
}

type SyncResult struct {
	Ignored       bool
	Duplicate     bool
	StateChanged  bool
	RequestRanges []uplink.Range
	State         UplinkStreamState
}

func (s *Store) initializeUplink(ctx context.Context) error {
	if !uuidPattern.MatchString(s.uplink.BootID) {
		return errors.New("bootId must be a lowercase RFC 4122 UUIDv4")
	}
	if _, err := uplink.ParseSequence("streamGeneration", s.uplink.StreamGeneration, true); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation string
	err = tx.QueryRowContext(ctx,
		"SELECT stream_generation FROM uplink_stream_state WHERE singleton_id = 1").Scan(&generation)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		epoch, err := uplink.NewUUID()
		if err != nil {
			return err
		}
		// PostgreSQL timestamptz has microsecond precision. Millisecond
		// normalization keeps the Hello identity byte-stable across both
		// databases and matches the frozen wire examples.
		epochStartedAt := s.now().UTC().Truncate(time.Millisecond)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO uplink_stream_state (
				singleton_id, stream_generation, stream_epoch, epoch_started_at,
				next_sequence, last_acked_sequence
			) VALUES (1, ?, ?, ?, 1, 0)`,
			s.uplink.StreamGeneration, epoch, uplink.FormatTime(epochStartedAt)); err != nil {
			return err
		}
	case err != nil:
		return err
	case generation != s.uplink.StreamGeneration:
		return fmt.Errorf(
			"configured streamGeneration %s differs from persisted generation %s; explicit stream reset is required",
			s.uplink.StreamGeneration, generation,
		)
	}

	var pending uint64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM uplink_outbox").Scan(&pending); err != nil {
		return err
	}
	var fingerprint string
	if err := tx.QueryRowContext(ctx,
		"SELECT last_snapshot_fingerprint FROM uplink_stream_state WHERE singleton_id = 1").Scan(&fingerprint); err != nil {
		return err
	}
	if pending == 0 && fingerprint == "" {
		record, err := loadSnapshotQuery(ctx, tx)
		if err == nil {
			if availabilityCode(record, s.now().UTC(), s.uplink.StaleAfter) == AvailabilityDataStale {
				record.Stale = true
			}
			if err := s.enqueueSnapshotChangesTx(ctx, tx, nil, record, s.now().UTC()); err != nil &&
				!errors.Is(err, errCalibrationSuppressed) {
				return err
			}
		} else if !errors.Is(err, ErrNoSnapshot) {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UplinkState(ctx context.Context) (UplinkStreamState, error) {
	if !s.uplink.Enabled {
		return UplinkStreamState{}, ErrUplinkDisabled
	}
	return uplinkStateQuery(ctx, s.db)
}

func uplinkStateQuery(ctx context.Context, queryer rowQueryer) (UplinkStreamState, error) {
	var (
		value       UplinkStreamState
		startedAt   string
		next, acked uint64
		first       sql.NullInt64
	)
	err := queryer.QueryRowContext(ctx, `
		SELECT stream_generation, stream_epoch, epoch_started_at,
		       next_sequence, last_acked_sequence,
		       (SELECT MIN(sequence) FROM uplink_outbox),
		       (SELECT COUNT(*) FROM uplink_outbox),
		       storage_status
		FROM uplink_stream_state WHERE singleton_id = 1`).Scan(
		&value.StreamGeneration, &value.StreamEpoch, &startedAt, &next,
		&acked, &first, &value.OutboxPending, &value.StorageStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return UplinkStreamState{}, ErrUplinkDisabled
	}
	if err != nil {
		return UplinkStreamState{}, err
	}
	value.EpochStartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return UplinkStreamState{}, err
	}
	value.LastAckedSequence = acked
	if next > 0 {
		value.LastProducedSequence = next - 1
	}
	if first.Valid {
		value.FirstAvailableSequence = uint64(first.Int64)
	} else {
		value.FirstAvailableSequence = next
	}
	if value.FirstAvailableSequence == 0 {
		value.FirstAvailableSequence = 1
	}
	return value, nil
}

func (s *Store) enqueueSnapshotChangesTx(
	ctx context.Context,
	tx *sql.Tx,
	previous *SnapshotRecord,
	current SnapshotRecord,
	now time.Time,
) error {
	if !s.uplink.Enabled {
		return nil
	}
	var previousView *uplink.SnapshotView
	if previous != nil {
		view := uplink.SnapshotView{State: previous.State, Meta: previous.Meta, Stale: previous.Stale}
		previousView = &view
	}
	currentView := uplink.SnapshotView{State: current.State, Meta: current.Meta, Stale: current.Stale}
	fingerprint, err := snapshotFingerprint(currentView)
	if err != nil {
		return err
	}
	var lastFingerprint, lastSnapshotAt string
	if err := tx.QueryRowContext(ctx, `
		SELECT last_snapshot_fingerprint, last_snapshot_at
		FROM uplink_stream_state WHERE singleton_id = 1`).Scan(&lastFingerprint, &lastSnapshotAt); err != nil {
		return err
	}
	calibration := false
	if fingerprint == lastFingerprint && lastSnapshotAt != "" {
		last, err := time.Parse(time.RFC3339Nano, lastSnapshotAt)
		if err != nil {
			return err
		}
		calibration = now.Sub(last) >= 30*time.Second
	}
	drafts := uplink.BuildDrafts(previousView, currentView, now, calibration)
	snapshotPersisted := false
	for _, draft := range drafts {
		err := s.enqueueDraftTx(ctx, tx, draft)
		if errors.Is(err, errCalibrationSuppressed) {
			continue
		}
		if err != nil {
			return err
		}
		if draft.Type == "device.snapshot" {
			snapshotPersisted = true
		}
	}
	if snapshotPersisted {
		if _, err := tx.ExecContext(ctx, `
			UPDATE uplink_stream_state
			SET last_snapshot_fingerprint = ?, last_snapshot_at = ?
			WHERE singleton_id = 1`, fingerprint, uplink.FormatTime(now)); err != nil {
			return err
		}
	}
	return nil
}

func snapshotFingerprint(view uplink.SnapshotView) (string, error) {
	payload := uplink.BuildSnapshot(view, view.State.UpdatedAt)
	payload.DataFreshness = 0
	contents, err := json.Marshal(struct {
		Quality string                 `json:"quality"`
		Payload uplink.SnapshotPayload `json:"payload"`
	}{
		Quality: uplink.SnapshotQuality(view),
		Payload: payload,
	})
	if err != nil {
		return "", err
	}
	canonical, err := uplink.CanonicalizeJSON(contents)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Store) enqueueDraftTx(ctx context.Context, tx *sql.Tx, draft uplink.Draft) error {
	exhausted, err := markSequenceExhaustedTx(ctx, tx)
	if err != nil {
		return err
	}
	if exhausted {
		// Reliable sequence exhaustion is an uplink failure, not a local
		// control-plane failure. Preserve the enclosing state/command commit.
		return nil
	}
	if err := s.flushPendingGapsTx(ctx, tx); err != nil {
		return err
	}
	usage, err := logicalUsageTx(ctx, tx)
	if err != nil {
		return err
	}
	estimate, encodedBytes, err := s.estimateDraftTx(ctx, tx, draft)
	if err != nil {
		return err
	}
	if draft.Calibration && usage+estimate > s.uplink.OrdinaryLimit {
		return errCalibrationSuppressed
	}
	var evicted []uplink.Range
	if usage+estimate > s.uplink.OrdinaryLimit {
		evicted, usage, err = s.evictSnapshotsTx(ctx, tx, usage, estimate)
		if err != nil {
			return err
		}
	}
	if len(evicted) > 0 {
		for _, sequenceRange := range evicted {
			if _, err := insertGapLedgerTx(ctx, tx, sequenceRange, "outbox_capacity"); err != nil {
				return err
			}
		}
		if err := s.flushPendingGapsTx(ctx, tx); err != nil {
			return err
		}
		usage, err = logicalUsageTx(ctx, tx)
		if err != nil {
			return err
		}
	}
	if encodedBytes > maxDirectReliableBytes || usage+estimate > s.uplink.HardLimit {
		return s.allocateDroppedSequenceTx(ctx, tx, "outbox_capacity")
	}
	if _, err = s.insertDraftTx(ctx, tx, draft); err != nil {
		return err
	}
	var unreported int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM uplink_gap_ledger
		WHERE accepted = 0 AND reported = 0`).Scan(&unreported); err != nil {
		return err
	}
	if unreported == 0 {
		_, err = tx.ExecContext(ctx, `
			UPDATE uplink_stream_state SET storage_status = 'OK' WHERE singleton_id = 1`)
	}
	return err
}

func (s *Store) estimateDraftTx(
	ctx context.Context,
	tx *sql.Tx,
	draft uplink.Draft,
) (logicalBytes int64, encodedBytes int, err error) {
	var epoch string
	var sequence uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT stream_epoch, next_sequence FROM uplink_stream_state WHERE singleton_id = 1`).Scan(
		&epoch, &sequence); err != nil {
		return 0, 0, err
	}
	message, err := uplink.NewReliable(s.uplink.Source, s.uplink.BootID, epoch, sequence, draft, s.now().UTC())
	if err != nil {
		return 0, 0, err
	}
	contents, err := json.Marshal(message)
	if err != nil {
		return 0, 0, err
	}
	return int64(len(contents) + len(draft.Channel) + 256), len(contents), nil
}

func (s *Store) insertDraftTx(ctx context.Context, tx *sql.Tx, draft uplink.Draft) (OutboxRecord, error) {
	var epoch string
	var sequence uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT stream_epoch, next_sequence FROM uplink_stream_state WHERE singleton_id = 1`).Scan(
		&epoch, &sequence); err != nil {
		return OutboxRecord{}, err
	}
	if sequence == 0 || sequence > uplink.MaxSafeSequence {
		return OutboxRecord{}, ErrUplinkStorageExhausted
	}
	message, err := uplink.NewReliable(
		s.uplink.Source, s.uplink.BootID, epoch, sequence, draft, s.now().UTC())
	if err != nil {
		return OutboxRecord{}, err
	}
	contents, err := json.Marshal(message)
	if err != nil {
		return OutboxRecord{}, err
	}
	if len(contents) > maxDirectReliableBytes {
		return OutboxRecord{}, ErrUplinkMessageTooLarge
	}
	digest, err := uplink.ReliableDigest(contents)
	if err != nil {
		return OutboxRecord{}, err
	}
	logicalBytes := int64(len(contents) + len(draft.Channel) + 256)
	usage, err := logicalUsageTx(ctx, tx)
	if err != nil {
		return OutboxRecord{}, err
	}
	if usage+logicalBytes > s.uplink.HardLimit {
		return OutboxRecord{}, ErrUplinkStorageExhausted
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO uplink_outbox (
			sequence, message_id, stream_epoch, channel, occurred_at,
			message_json, identity_digest, retention_class, calibration, logical_bytes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sequence, message.MessageID, epoch, draft.Channel, message.OccurredAt,
		contents, digest, draft.RetentionClass, boolInt(draft.Calibration), logicalBytes); err != nil {
		return OutboxRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE uplink_stream_state SET next_sequence = ? WHERE singleton_id = 1`,
		sequence+1); err != nil {
		return OutboxRecord{}, err
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, message.OccurredAt)
	return OutboxRecord{
		Sequence: sequence, MessageID: message.MessageID, StreamEpoch: epoch,
		Channel: draft.Channel, OccurredAt: occurredAt, MessageJSON: contents,
		IdentityDigest: digest, LogicalBytes: logicalBytes,
	}, nil
}

func logicalUsageTx(ctx context.Context, queryer rowQueryer) (int64, error) {
	var value int64
	err := queryer.QueryRowContext(ctx, `
		SELECT
			COALESCE((SELECT SUM(logical_bytes) FROM uplink_outbox), 0) +
			COALESCE((SELECT SUM(logical_bytes) FROM uplink_gap_ledger WHERE accepted = 0), 0)`).Scan(&value)
	return value, err
}

func (s *Store) evictSnapshotsTx(
	ctx context.Context,
	tx *sql.Tx,
	usage int64,
	required int64,
) ([]uplink.Range, int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT sequence, logical_bytes FROM uplink_outbox
		WHERE retention_class = 'snapshot'
		ORDER BY calibration DESC, sequence ASC`)
	if err != nil {
		return nil, usage, err
	}
	defer rows.Close()
	type candidate struct {
		sequence uint64
		bytes    int64
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.sequence, &item.bytes); err != nil {
			return nil, usage, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, usage, err
	}
	var removed []uint64
	for _, item := range candidates {
		if usage+required <= s.uplink.OrdinaryLimit {
			break
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM uplink_outbox WHERE sequence = ?", item.sequence); err != nil {
			return nil, usage, err
		}
		usage -= item.bytes
		removed = append(removed, item.sequence)
	}
	return contiguousRanges(removed), usage, nil
}

func contiguousRanges(sequences []uint64) []uplink.Range {
	if len(sequences) == 0 {
		return nil
	}
	sort.Slice(sequences, func(left, right int) bool { return sequences[left] < sequences[right] })
	ranges := make([]uplink.Range, 0)
	from, to := sequences[0], sequences[0]
	for _, sequence := range sequences[1:] {
		if sequence == to+1 {
			to = sequence
			continue
		}
		ranges = append(ranges, uplink.Range{From: uplink.FormatSequence(from), To: uplink.FormatSequence(to)})
		from, to = sequence, sequence
	}
	return append(ranges, uplink.Range{From: uplink.FormatSequence(from), To: uplink.FormatSequence(to)})
}

func insertGapLedgerTx(
	ctx context.Context,
	tx *sql.Tx,
	sequenceRange uplink.Range,
	reason string,
) (int64, error) {
	from, err := uplink.ParseSequence("gap.from", sequenceRange.From, true)
	if err != nil {
		return 0, err
	}
	to, err := uplink.ParseSequence("gap.to", sequenceRange.To, true)
	if err != nil || from > to {
		return 0, errors.New("invalid gap range")
	}
	logicalBytes, err := gapLogicalBytes(sequenceRange)
	if err != nil {
		return 0, err
	}
	var epoch string
	if err := tx.QueryRowContext(ctx,
		"SELECT stream_epoch FROM uplink_stream_state WHERE singleton_id = 1").Scan(&epoch); err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO uplink_gap_ledger (
			stream_epoch, from_sequence, to_sequence, reason, reported, logical_bytes
		) VALUES (?, ?, ?, ?, 0, ?)`,
		epoch, from, to, reason, logicalBytes)
	return logicalBytes, err
}

func gapLogicalBytes(sequenceRange uplink.Range) (int64, error) {
	canonical, err := uplink.CanonicalizeJSON([]byte(
		fmt.Sprintf(`{"from":"%s","to":"%s"}`, sequenceRange.From, sequenceRange.To)))
	if err != nil {
		return 0, err
	}
	return int64(len(canonical) + 128), nil
}

func (s *Store) allocateDroppedSequenceTx(ctx context.Context, tx *sql.Tx, reason string) error {
	var sequence uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT next_sequence FROM uplink_stream_state WHERE singleton_id = 1`).Scan(&sequence); err != nil {
		return err
	}
	if sequence == 0 || sequence > uplink.MaxSafeSequence {
		_, err := tx.ExecContext(ctx, `
			UPDATE uplink_stream_state SET storage_status = 'UPLINK_STORAGE_EXHAUSTED'
			WHERE singleton_id = 1`)
		return err
	}
	sequenceRange := uplink.Range{
		From: uplink.FormatSequence(sequence), To: uplink.FormatSequence(sequence),
	}
	gapBytes, err := gapLogicalBytes(sequenceRange)
	if err != nil {
		return err
	}
	usage, err := logicalUsageTx(ctx, tx)
	if err != nil {
		return err
	}
	if usage+gapBytes > s.uplink.HardLimit {
		_, err := tx.ExecContext(ctx, `
			UPDATE uplink_stream_state SET storage_status = 'UPLINK_STORAGE_EXHAUSTED'
			WHERE singleton_id = 1`)
		return err
	}
	if _, err := insertGapLedgerTx(ctx, tx, sequenceRange, reason); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE uplink_stream_state
		SET next_sequence = ?, storage_status = 'UPLINK_GAP_PENDING'
		WHERE singleton_id = 1`, sequence+1); err != nil {
		return err
	}
	return s.flushPendingGapsTx(ctx, tx)
}

func (s *Store) flushPendingGapsTx(ctx context.Context, tx *sql.Tx) error {
	exhausted, err := markSequenceExhaustedTx(ctx, tx)
	if err != nil || exhausted {
		return err
	}
	var reason string
	err = tx.QueryRowContext(ctx, `
		SELECT reason FROM uplink_gap_ledger
		WHERE accepted = 0 AND reported = 0
		ORDER BY from_sequence ASC LIMIT 1`).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, from_sequence, to_sequence
		FROM uplink_gap_ledger
		WHERE accepted = 0 AND reported = 0 AND reason = ?
		ORDER BY from_sequence ASC LIMIT 32`, reason)
	if err != nil {
		return err
	}
	var (
		ids    []int64
		ranges []uplink.Range
	)
	for rows.Next() {
		var id int64
		var from, to uint64
		if err := rows.Scan(&id, &from, &to); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
		ranges = append(ranges, uplink.Range{
			From: uplink.FormatSequence(from), To: uplink.FormatSequence(to),
		})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(ranges) == 0 {
		return nil
	}
	state, err := uplinkStateQuery(ctx, tx)
	if err != nil {
		return err
	}
	gapDraft := uplink.Draft{
		Type: "data.gap.detected", Channel: "event", OccurredAt: s.now().UTC(),
		Quality: "GOOD", RetentionClass: "gap",
		Payload: uplink.GapPayload{
			StreamEpoch: state.StreamEpoch, Ranges: ranges, Reason: reason,
		},
	}
	estimate, encodedBytes, err := s.estimateDraftTx(ctx, tx, gapDraft)
	if err != nil {
		return err
	}
	usage, err := logicalUsageTx(ctx, tx)
	if err != nil {
		return err
	}
	if encodedBytes > maxDirectReliableBytes || usage+estimate > s.uplink.HardLimit {
		return nil
	}
	if _, err := s.insertDraftTx(ctx, tx, gapDraft); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE uplink_gap_ledger SET reported = 1 WHERE id = ?", id); err != nil {
			return err
		}
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM uplink_gap_ledger
		WHERE accepted = 0 AND reported = 0`).Scan(&pending); err != nil {
		return err
	}
	status := "OK"
	if pending > 0 {
		status = "UPLINK_GAP_PENDING"
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE uplink_stream_state SET storage_status = ? WHERE singleton_id = 1`, status)
	return err
}

func markSequenceExhaustedTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var next uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT next_sequence FROM uplink_stream_state WHERE singleton_id = 1`).Scan(&next); err != nil {
		return false, err
	}
	if next <= uplink.MaxSafeSequence {
		return false, nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE uplink_stream_state SET storage_status = 'UPLINK_STORAGE_EXHAUSTED'
		WHERE singleton_id = 1`)
	return true, err
}

func (s *Store) NextOutbox(ctx context.Context, limit int) ([]OutboxRecord, error) {
	if !s.uplink.Enabled {
		return nil, ErrUplinkDisabled
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("outbox limit must be between 1 and 100")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, message_id, stream_epoch, channel, occurred_at,
		       message_json, identity_digest, logical_bytes, publish_attempts
		FROM uplink_outbox ORDER BY sequence ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []OutboxRecord
	for rows.Next() {
		record, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// CurrentSnapshotOutbox returns a reliable full snapshot representing the
// current local state. It reuses an identical unacknowledged snapshot when
// possible; otherwise it atomically allocates a new reliable snapshot without
// changing the local device state.
func (s *Store) CurrentSnapshotOutbox(
	ctx context.Context,
	at time.Time,
) (*OutboxRecord, error) {
	if !s.uplink.Enabled {
		return nil, ErrUplinkDisabled
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	record, err := loadSnapshotQuery(ctx, tx)
	if errors.Is(err, ErrNoSnapshot) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at = at.UTC()
	if availabilityCode(record, at, s.uplink.StaleAfter) == AvailabilityDataStale {
		record.Stale = true
	}
	view := uplink.SnapshotView{State: record.State, Meta: record.Meta, Stale: record.Stale}
	fingerprint, err := snapshotFingerprint(view)
	if err != nil {
		return nil, err
	}
	var persistedFingerprint string
	if err := tx.QueryRowContext(ctx, `
		SELECT last_snapshot_fingerprint
		FROM uplink_stream_state WHERE singleton_id = 1`).Scan(&persistedFingerprint); err != nil {
		return nil, err
	}
	if persistedFingerprint == fingerprint {
		existing, err := latestSnapshotOutboxQuery(ctx, tx, 0)
		if err == nil {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return &existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	var next uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT next_sequence FROM uplink_stream_state WHERE singleton_id = 1`).Scan(&next); err != nil {
		return nil, err
	}
	draft := uplink.Draft{
		Type: "device.snapshot", Channel: "snapshot", OccurredAt: record.State.UpdatedAt,
		Quality: uplink.SnapshotQuality(view), Payload: uplink.BuildSnapshot(view, at),
		RetentionClass: "snapshot",
	}
	if err := s.enqueueDraftTx(ctx, tx, draft); err != nil {
		return nil, err
	}
	current, err := latestSnapshotOutboxQuery(ctx, tx, next)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE uplink_stream_state
		SET last_snapshot_fingerprint = ?, last_snapshot_at = ?
		WHERE singleton_id = 1`, fingerprint, uplink.FormatTime(at)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &current, nil
}

func latestSnapshotOutboxQuery(
	ctx context.Context,
	queryer rowQueryer,
	minimumSequence uint64,
) (OutboxRecord, error) {
	row := queryer.QueryRowContext(ctx, `
		SELECT sequence, message_id, stream_epoch, channel, occurred_at,
		       message_json, identity_digest, logical_bytes, publish_attempts
		FROM uplink_outbox
		WHERE channel = 'snapshot' AND sequence >= ?
		ORDER BY sequence DESC LIMIT 1`, minimumSequence)
	return scanOutbox(row)
}

func (s *Store) NextOutboxDue(
	ctx context.Context,
	at time.Time,
	retryAfter time.Duration,
	limit int,
) ([]OutboxRecord, error) {
	if !s.uplink.Enabled {
		return nil, ErrUplinkDisabled
	}
	if limit < 1 || limit > 100 || retryAfter <= 0 {
		return nil, errors.New("outbox due query has invalid limit or retry interval")
	}
	cutoff := at.UTC().Add(-retryAfter)
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, message_id, stream_epoch, channel, occurred_at,
		       message_json, identity_digest, logical_bytes, publish_attempts
		FROM uplink_outbox
		WHERE last_publish_at IS NULL OR julianday(last_publish_at) <= julianday(?)
		ORDER BY sequence ASC LIMIT ?`, uplink.FormatTime(cutoff), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []OutboxRecord
	for rows.Next() {
		record, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) ResetOutboxPublishSchedule(ctx context.Context) error {
	if !s.uplink.Enabled {
		return ErrUplinkDisabled
	}
	_, err := s.db.ExecContext(ctx, "UPDATE uplink_outbox SET last_publish_at = NULL")
	return err
}

type rowScanner interface {
	Scan(...any) error
}

func scanOutbox(scanner rowScanner) (OutboxRecord, error) {
	var record OutboxRecord
	var occurredAt string
	if err := scanner.Scan(
		&record.Sequence, &record.MessageID, &record.StreamEpoch, &record.Channel,
		&occurredAt, &record.MessageJSON, &record.IdentityDigest,
		&record.LogicalBytes, &record.PublishAttempts,
	); err != nil {
		return OutboxRecord{}, err
	}
	var err error
	record.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt)
	return record, err
}

func (s *Store) PrepareDirectAttempt(
	ctx context.Context,
	record OutboxRecord,
	at time.Time,
) ([]byte, error) {
	if !s.uplink.Enabled {
		return nil, ErrUplinkDisabled
	}
	contents, err := rewriteAttempt(record.MessageJSON, at, false)
	if err != nil {
		return nil, err
	}
	digest, err := uplink.ReliableDigest(contents)
	if err != nil {
		return nil, err
	}
	if digest != record.IdentityDigest {
		return nil, errors.New("outbox identity digest mismatch")
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE uplink_outbox
		SET publish_attempts = publish_attempts + 1, last_publish_at = ?
		WHERE sequence = ?`, uplink.FormatTime(at), record.Sequence); err != nil {
		return nil, err
	}
	return contents, nil
}

func rewriteAttempt(contents []byte, at time.Time, replayed bool) ([]byte, error) {
	if _, err := uplink.ReliableDigest(contents); err != nil {
		return nil, fmt.Errorf("validate reliable message before attempt: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	fields := make(map[string]json.RawMessage)
	if err := decoder.Decode(&fields); err != nil {
		return nil, err
	}
	if err := requireDecoderEOF(decoder); err != nil {
		return nil, err
	}
	sentAt, _ := json.Marshal(uplink.FormatTime(at))
	replay, _ := json.Marshal(replayed)
	fields["sentAt"] = sentAt
	fields["replayed"] = replay
	return json.Marshal(fields)
}

func requireDecoderEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func (s *Store) BuildReplay(
	ctx context.Context,
	ranges []uplink.Range,
	at time.Time,
) ([]byte, error) {
	batches, err := s.BuildReplayBatches(ctx, ranges, at)
	if err != nil {
		return nil, err
	}
	if len(batches) == 0 {
		return nil, errors.New("requested replay range contains no available messages")
	}
	return batches[0], nil
}

func (s *Store) BuildReplayBatches(
	ctx context.Context,
	ranges []uplink.Range,
	at time.Time,
) ([][]byte, error) {
	if !s.uplink.Enabled {
		return nil, ErrUplinkDisabled
	}
	parsed, err := validateRanges("ranges", ranges, uplink.MaxSafeSequence, true)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, message_id, stream_epoch, channel, occurred_at,
		       message_json, identity_digest, logical_bytes, publish_attempts
		FROM uplink_outbox ORDER BY sequence ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []OutboxRecord
	for rows.Next() {
		record, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		if inRanges(record.Sequence, parsed) {
			records = append(records, record)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	state, err := s.UplinkState(ctx)
	if err != nil {
		return nil, err
	}
	var batches [][]byte
	for _, record := range records {
		message, err := rewriteAttempt(record.MessageJSON, at, true)
		if err != nil {
			return nil, err
		}
		digest, err := uplink.ReliableDigest(message)
		if err != nil || digest != record.IdentityDigest {
			return nil, errors.New("replay identity digest mismatch")
		}
		if len(batches) == 0 {
			batches = append(batches, nil)
		}
		var batch uplink.ReplayBatch
		if batches[len(batches)-1] == nil {
			batchID, err := uplink.NewUUID()
			if err != nil {
				return nil, err
			}
			batch = uplink.ReplayBatch{
				SchemaVersion: uplink.SchemaVersion, BatchID: batchID, Source: s.uplink.Source,
				BootID: s.uplink.BootID, SentAt: uplink.FormatTime(at), StreamEpoch: state.StreamEpoch,
			}
		} else if err := json.Unmarshal(batches[len(batches)-1], &batch); err != nil {
			return nil, err
		}
		candidate := append(batch.Messages, uplink.ReplayItem{
			Channel: record.Channel, Message: json.RawMessage(message),
		})
		batch.Messages = candidate
		encoded, err := json.Marshal(batch)
		if err != nil {
			return nil, err
		}
		if len(encoded) > uplink.MaxReplayBytes || len(batch.Messages) > uplink.MaxReplayMessages {
			batch.Messages = batch.Messages[:len(batch.Messages)-1]
			if len(batch.Messages) == 0 {
				return nil, errors.New("one requested message exceeds replay payload limit")
			}
			previous, err := json.Marshal(batch)
			if err != nil {
				return nil, err
			}
			batches[len(batches)-1] = previous
			batchID, err := uplink.NewUUID()
			if err != nil {
				return nil, err
			}
			next := uplink.ReplayBatch{
				SchemaVersion: uplink.SchemaVersion, BatchID: batchID, Source: s.uplink.Source,
				BootID: s.uplink.BootID, SentAt: uplink.FormatTime(at), StreamEpoch: state.StreamEpoch,
				Messages: []uplink.ReplayItem{{
					Channel: record.Channel, Message: json.RawMessage(message),
				}},
			}
			nextEncoded, err := json.Marshal(next)
			if err != nil {
				return nil, err
			}
			if len(nextEncoded) > uplink.MaxReplayBytes {
				return nil, errors.New("one requested message exceeds replay payload limit")
			}
			batches = append(batches, nextEncoded)
		} else {
			batches[len(batches)-1] = encoded
		}
	}
	return batches, nil
}

type parsedRange struct {
	from uint64
	to   uint64
}

func validateRanges(name string, ranges []uplink.Range, produced uint64, requireNonEmpty bool) ([]parsedRange, error) {
	if (requireNonEmpty && len(ranges) == 0) || len(ranges) > 32 {
		return nil, fmt.Errorf("%s has invalid item count", name)
	}
	parsed := make([]parsedRange, 0, len(ranges))
	var previousTo uint64
	for index, item := range ranges {
		from, err := uplink.ParseSequence(name+".from", item.From, true)
		if err != nil {
			return nil, err
		}
		to, err := uplink.ParseSequence(name+".to", item.To, true)
		if err != nil {
			return nil, err
		}
		if from > to || to > produced || (index > 0 && from <= previousTo) {
			return nil, fmt.Errorf("%s must be ordered, non-overlapping and no later than produced sequence", name)
		}
		parsed = append(parsed, parsedRange{from: from, to: to})
		previousTo = to
	}
	return parsed, nil
}

func inRanges(sequence uint64, ranges []parsedRange) bool {
	for _, item := range ranges {
		if sequence >= item.from && sequence <= item.to {
			return true
		}
	}
	return false
}

func (s *Store) ApplySyncJSON(ctx context.Context, contents []byte) (SyncResult, error) {
	if !s.uplink.Enabled {
		return SyncResult{}, ErrUplinkDisabled
	}
	canonical, err := uplink.CanonicalizeJSON(contents)
	if err != nil {
		return SyncResult{}, fmt.Errorf("%w: %v", ErrInvalidSync, err)
	}
	digestValue := sha256.Sum256(canonical)
	digest := hex.EncodeToString(digestValue[:])
	decoder := json.NewDecoder(bytes.NewReader(contents))
	var message uplink.Sync
	if err := decoder.Decode(&message); err != nil {
		return SyncResult{}, fmt.Errorf("%w: %v", ErrInvalidSync, err)
	}
	if err := requireDecoderEOF(decoder); err != nil {
		return SyncResult{}, fmt.Errorf("%w: %v", ErrInvalidSync, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SyncResult{}, err
	}
	defer tx.Rollback()
	state, err := uplinkStateQuery(ctx, tx)
	if err != nil {
		return SyncResult{}, err
	}
	revision, highest, err := validateSync(message, s.uplink.Source, state)
	if err != nil {
		return SyncResult{}, fmt.Errorf("%w: %v", ErrInvalidSync, err)
	}
	var lastRevision uint64
	var lastID, lastDigest string
	if err := tx.QueryRowContext(ctx, `
		SELECT last_sync_revision, last_sync_id, last_sync_digest
		FROM uplink_stream_state WHERE singleton_id = 1`).Scan(
		&lastRevision, &lastID, &lastDigest); err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{RequestRanges: message.Ranges}
	if message.SyncID == lastID && lastID != "" && digest != lastDigest {
		return SyncResult{}, ErrSyncConflict
	}
	switch {
	case revision < lastRevision:
		result.Ignored = true
		result.State = state
		return result, nil
	case revision == lastRevision && revision != 0:
		if digest != lastDigest {
			return SyncResult{}, ErrSyncConflict
		}
		result.Duplicate = true
		result.State = state
		return result, nil
	}
	if highest < state.LastAckedSequence {
		return SyncResult{}, fmt.Errorf("%w: acknowledgement moves backward", ErrInvalidSync)
	}
	if err := validateAcceptedGapsTx(ctx, tx, message.AcceptedGapRanges, state.StreamEpoch, state.LastProducedSequence); err != nil {
		return SyncResult{}, fmt.Errorf("%w: %v", ErrInvalidSync, err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM uplink_outbox WHERE stream_epoch = ? AND sequence <= ?",
		state.StreamEpoch, highest); err != nil {
		return SyncResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE uplink_stream_state SET last_acked_sequence = ?
		WHERE singleton_id = 1`, highest); err != nil {
		return SyncResult{}, err
	}
	gapStateChanged, err := acceptGapsTx(
		ctx, tx, message.AcceptedGapRanges, state.StreamEpoch, highest,
	)
	if err != nil {
		return SyncResult{}, err
	}
	if err := s.flushPendingGapsTx(ctx, tx); err != nil {
		return SyncResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE uplink_stream_state SET
			last_sync_revision = ?, last_sync_id = ?, last_sync_digest = ?, last_sync_json = ?
		WHERE singleton_id = 1`,
		revision, message.SyncID, digest, contents); err != nil {
		return SyncResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SyncResult{}, err
	}
	result.StateChanged = highest != state.LastAckedSequence || gapStateChanged
	result.State, err = s.UplinkState(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	_ = s.CheckpointPassive(ctx)
	return result, nil
}

func validateSync(message uplink.Sync, source uplink.Source, state UplinkStreamState) (uint64, uint64, error) {
	if message.SchemaVersion != uplink.SchemaVersion || !uuidPattern.MatchString(message.SyncID) {
		return 0, 0, errors.New("schemaVersion or syncId is invalid")
	}
	revision, err := uplink.ParseSequence("syncRevision", message.SyncRevision, true)
	if err != nil {
		return 0, 0, err
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, message.IssuedAt)
	if err != nil || !strings.HasSuffix(message.IssuedAt, "Z") || issuedAt.Location() != time.UTC {
		return 0, 0, errors.New("issuedAt must be a UTC timestamp ending in Z")
	}
	if message.Target.SiteID != source.SiteID || message.Target.BlockID != source.BlockID ||
		message.Target.DeviceID != source.DeviceID {
		return 0, 0, errors.New("sync target does not match this Block")
	}
	if message.StreamEpoch != state.StreamEpoch {
		return 0, 0, errors.New("sync streamEpoch does not match the current stream")
	}
	highest, err := uplink.ParseSequence("highestContiguousSequence", message.HighestContiguousSequence, false)
	if err != nil || highest > state.LastProducedSequence {
		return 0, 0, errors.New("highestContiguousSequence is later than produced data")
	}
	switch message.Action {
	case "ACK":
		if len(message.Ranges) != 0 {
			return 0, 0, errors.New("ACK ranges must be empty")
		}
	case "REQUEST_RANGE":
		ranges, err := validateRanges("ranges", message.Ranges, state.LastProducedSequence, true)
		if err != nil {
			return 0, 0, err
		}
		for _, item := range ranges {
			if item.from <= highest {
				return 0, 0, errors.New("requested ranges must be later than highestContiguousSequence")
			}
		}
	default:
		return 0, 0, errors.New("action must be ACK or REQUEST_RANGE")
	}
	accepted, err := validateRanges(
		"acceptedGapRanges", message.AcceptedGapRanges, state.LastProducedSequence, false)
	if err != nil {
		return 0, 0, err
	}
	for _, item := range accepted {
		if item.to > highest {
			return 0, 0, errors.New("accepted gap range is later than highestContiguousSequence")
		}
	}
	return revision, highest, nil
}

func validateAcceptedGapsTx(
	ctx context.Context,
	tx *sql.Tx,
	ranges []uplink.Range,
	epoch string,
	produced uint64,
) error {
	parsed, err := validateRanges("acceptedGapRanges", ranges, produced, false)
	if err != nil {
		return err
	}
	for _, item := range parsed {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM uplink_gap_ledger
			WHERE stream_epoch = ?
			  AND from_sequence = ? AND to_sequence = ?`,
			epoch, item.from, item.to).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("accepted gap %d-%d was not declared by Block", item.from, item.to)
		}
	}
	return nil
}

func acceptGapsTx(
	ctx context.Context,
	tx *sql.Tx,
	ranges []uplink.Range,
	epoch string,
	highest uint64,
) (bool, error) {
	parsed, err := validateRanges("acceptedGapRanges", ranges, uplink.MaxSafeSequence, false)
	if err != nil {
		return false, err
	}
	changed := false
	for _, item := range parsed {
		result, err := tx.ExecContext(ctx, `
			UPDATE uplink_gap_ledger SET accepted = 1
			WHERE stream_epoch = ? AND accepted = 0
			  AND from_sequence = ? AND to_sequence = ?`,
			epoch, item.from, item.to)
		if err != nil {
			return false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		changed = changed || affected > 0
	}
	// Advancing the contiguous waterline implicitly accepts every complete
	// local gap below it. Rows remain as accepted tombstones so a later
	// paginated BDM receipt can still cross-check the exact range idempotently.
	result, err := tx.ExecContext(ctx, `
		UPDATE uplink_gap_ledger SET accepted = 1
		WHERE stream_epoch = ? AND accepted = 0 AND to_sequence <= ?`, epoch, highest)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected > 0 {
		changed = true
	}
	return changed, nil
}

func (s *Store) CheckpointPassive(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	return s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(
		&busy, &logFrames, &checkpointed)
}
