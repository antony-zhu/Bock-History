package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/state"
	"block.local/block-agent/internal/uplink"
)

func TestBDMDisabledCreatesNoStreamOrOutboxWork(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store, err := Open(filepath.Join(t.TempDir(), "block.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveSnapshot(ctx, uplinkSampleRecord(now, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UplinkState(ctx); !errors.Is(err, ErrUplinkDisabled) {
		t.Fatalf("UplinkState error = %v, want ErrUplinkDisabled", err)
	}
	for _, table := range []string{"uplink_stream_state", "uplink_outbox", "uplink_gap_ledger"} {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s contains %d rows while BDM is disabled", table, count)
		}
	}
}

func TestFirstEnableQueuesExistingExpiredSnapshotAsStale(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "block.db")
	disabled, err := Open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := disabled.SaveSnapshot(ctx, uplinkSampleRecord(now, 1)); err != nil {
		t.Fatal(err)
	}
	if err := disabled.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	enabled, err := OpenWithOptions(path, func() time.Time { return now }, UplinkOptions{
		Enabled: true,
		Source: uplink.Source{
			SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
		},
		BootID:           "20000000-0000-4000-8000-000000000001",
		StreamGeneration: "1", StaleAfter: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer enabled.Close()
	records, err := enabled.NextOutbox(ctx, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("initial outbox = %#v, err = %v", records, err)
	}
	var message struct {
		Quality string `json:"quality"`
		Payload struct {
			Connectivity string `json:"connectivity"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(records[0].MessageJSON, &message); err != nil {
		t.Fatal(err)
	}
	if message.Quality != "STALE" || message.Payload.Connectivity != "DEGRADED" {
		t.Fatalf("expired initial snapshot = %+v", message)
	}
}

func TestInitialPLCAndFirstEnableQueueAllActiveAlarmsBeforeSnapshot(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	t.Run("first PLC snapshot", func(t *testing.T) {
		store := openUplinkStore(t, &now, 0, 0)
		defer store.Close()
		snapshot := sampleSnapshot(now, 1, 1)
		snapshot.Points.Alarms = []plccontract.Alarm{
			{AlarmID: "9", Level: "warning", Code: "W9", Text: "later", Active: true, OccurredAt: now},
			{AlarmID: "2", Level: "critical", Code: "E2", Text: "first", Active: true, OccurredAt: now},
		}
		if _, err := store.SavePLC(ctx, snapshot, now, time.Minute); err != nil {
			t.Fatal(err)
		}
		assertInitialAlarmOutbox(t, store)
	})

	t.Run("existing snapshot at first enable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "block.db")
		disabled, err := Open(path, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		record := uplinkSampleRecord(now, 1)
		record.State.Alarms = []state.Alarm{
			{ID: 9, Level: "warning", Code: "W9", Text: "later", OccurredAt: now},
			{ID: 2, Level: "critical", Code: "E2", Text: "first", OccurredAt: now},
		}
		if err := disabled.SaveSnapshot(ctx, record); err != nil {
			t.Fatal(err)
		}
		if err := disabled.Close(); err != nil {
			t.Fatal(err)
		}
		enabled, err := OpenWithOptions(path, func() time.Time { return now }, UplinkOptions{
			Enabled: true,
			Source: uplink.Source{
				SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
			},
			BootID:           "20000000-0000-4000-8000-000000000001",
			StreamGeneration: "1", StaleAfter: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer enabled.Close()
		assertInitialAlarmOutbox(t, enabled)
	})
}

func assertInitialAlarmOutbox(t *testing.T, store *Store) {
	t.Helper()
	records, err := store.NextOutbox(context.Background(), 10)
	if err != nil || len(records) != 3 {
		t.Fatalf("initial alarm outbox = %#v, err = %v", records, err)
	}
	if records[0].Channel != "alarm" || records[1].Channel != "alarm" ||
		records[2].Channel != "snapshot" {
		t.Fatalf("initial channels/order = %q, %q, %q",
			records[0].Channel, records[1].Channel, records[2].Channel)
	}
	var alarmIDs []string
	for _, record := range records[:2] {
		var message struct {
			Payload uplink.AlarmRaisedPayload `json:"payload"`
		}
		if err := json.Unmarshal(record.MessageJSON, &message); err != nil {
			t.Fatal(err)
		}
		alarmIDs = append(alarmIDs, message.Payload.AlarmID)
	}
	if alarmIDs[0] != "2" || alarmIDs[1] != "9" {
		t.Fatalf("initial alarm ids = %v", alarmIDs)
	}
}

func TestEpochStartedAtIsMillisecondStableAcrossRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 123_456_789, time.UTC)
	path := filepath.Join(t.TempDir(), "block.db")
	options := UplinkOptions{
		Enabled: true,
		Source: uplink.Source{
			SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
		},
		BootID:           "20000000-0000-4000-8000-000000000001",
		StreamGeneration: "1", StaleAfter: 5 * time.Second,
	}
	first, err := OpenWithOptions(path, func() time.Time { return now }, options)
	if err != nil {
		t.Fatal(err)
	}
	firstState, err := first.UplinkState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	second, err := OpenWithOptions(path, func() time.Time { return now }, options)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondState, err := second.UplinkState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantStartedAt := time.Date(2026, 7, 24, 2, 0, 0, 123_000_000, time.UTC)
	if firstState.StreamEpoch != secondState.StreamEpoch ||
		!firstState.EpochStartedAt.Equal(wantStartedAt) ||
		uplink.FormatTime(firstState.EpochStartedAt) != uplink.FormatTime(secondState.EpochStartedAt) {
		t.Fatalf("epoch identity changed: first=%+v second=%+v", firstState, secondState)
	}
}

func TestOutboxSurvivesPUBACKAttemptAndOnlyApplicationACKDeletes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store := openUplinkStore(t, &now, 0, 0)
	defer store.Close()
	if err := store.SaveSnapshot(ctx, uplinkSampleRecord(now, 1)); err != nil {
		t.Fatal(err)
	}
	records, err := store.NextOutbox(ctx, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("outbox = %#v, err = %v", records, err)
	}
	firstAttempt, err := store.PrepareDirectAttempt(ctx, records[0], now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	secondAttempt, err := store.PrepareDirectAttempt(ctx, records[0], now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, _ := uplink.ReliableDigest(firstAttempt)
	secondDigest, _ := uplink.ReliableDigest(secondAttempt)
	if firstDigest != records[0].IdentityDigest || secondDigest != records[0].IdentityDigest {
		t.Fatal("retry changed reliable message identity")
	}
	if pending, _ := store.NextOutbox(ctx, 10); len(pending) != 1 {
		t.Fatalf("transport publish attempt deleted application data: %d rows", len(pending))
	}

	stream, err := store.UplinkState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	syncBody := syncJSON(t, stream, "40000000-0000-4000-8000-000000000001", "1", "ACK", "1", nil)
	result, err := store.ApplySyncJSON(ctx, syncBody)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.LastAckedSequence != 1 || result.State.OutboxPending != 0 {
		t.Fatalf("application ACK state = %+v", result.State)
	}
	duplicate, err := store.ApplySyncJSON(ctx, syncBody)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate sync result = %+v, err = %v", duplicate, err)
	}
	conflict := syncJSON(t, stream, "40000000-0000-4000-8000-000000000002", "1", "ACK", "1", nil)
	if _, err := store.ApplySyncJSON(ctx, conflict); !errors.Is(err, ErrSyncConflict) {
		t.Fatalf("same revision conflict error = %v", err)
	}
}

func TestPaginatedAcceptedGapReceiptsRemainIdempotentAfterWaterlineAdvance(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store := openUplinkStore(t, &now, 0, 0)
	defer store.Close()
	stream, err := store.UplinkState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ranges []uplink.Range
	for sequence := uint64(1); sequence <= 40; sequence++ {
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO uplink_gap_ledger (
				stream_epoch, from_sequence, to_sequence, reason,
				accepted, reported, logical_bytes
			) VALUES (?, ?, ?, 'local_retention', 0, 1, 64)`,
			stream.StreamEpoch, sequence, sequence); err != nil {
			t.Fatal(err)
		}
		ranges = append(ranges, uplink.Range{
			From: uplink.FormatSequence(sequence),
			To:   uplink.FormatSequence(sequence),
		})
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE uplink_stream_state SET next_sequence = 41 WHERE singleton_id = 1`); err != nil {
		t.Fatal(err)
	}
	stream, err = store.UplinkState(ctx)
	if err != nil || stream.LastProducedSequence != 40 {
		t.Fatalf("prepared gap stream = %+v, err=%v", stream, err)
	}
	buildSync := func(revision uint64, accepted []uplink.Range) []byte {
		t.Helper()
		body, err := json.Marshal(uplink.Sync{
			SchemaVersion: uplink.SchemaVersion,
			SyncID:        fmt.Sprintf("40000000-0000-4000-8000-%012d", revision),
			SyncRevision:  uplink.FormatSequence(revision),
			Target: uplink.SyncTarget{
				SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
			},
			IssuedAt: "2026-07-24T02:00:02Z", Action: "ACK",
			StreamEpoch: stream.StreamEpoch, HighestContiguousSequence: "40",
			Ranges: []uplink.Range{}, AcceptedGapRanges: accepted,
		})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	first, err := store.ApplySyncJSON(ctx, buildSync(1, ranges[:32]))
	if err != nil {
		t.Fatal(err)
	}
	if !first.StateChanged || first.State.LastAckedSequence != 40 {
		t.Fatalf("first paginated receipt state = %+v", first)
	}
	second, err := store.ApplySyncJSON(ctx, buildSync(2, ranges[32:]))
	if err != nil {
		t.Fatalf("second paginated receipt rejected accepted tombstones: %v", err)
	}
	if second.StateChanged {
		t.Fatalf("idempotent second receipt changed stream state: %+v", second)
	}
	var total, accepted, pending int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(accepted), 0),
		       COALESCE(SUM(CASE WHEN accepted = 0 THEN logical_bytes ELSE 0 END), 0)
		FROM uplink_gap_ledger WHERE stream_epoch = ?`,
		stream.StreamEpoch).Scan(&total, &accepted, &pending); err != nil {
		t.Fatal(err)
	}
	if total != 40 || accepted != 40 || pending != 0 {
		t.Fatalf("gap audit tombstones total=%d accepted=%d pendingBytes=%d", total, accepted, pending)
	}
}

func TestReplayIsOrderedBoundedAndPreservesIdentity(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store := openUplinkStore(t, &now, 0, 0)
	defer store.Close()
	first := uplinkSampleRecord(now, 1)
	if err := store.SaveSnapshot(ctx, first); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second := uplinkSampleRecord(now, 2)
	second.State.Running = true
	second.State.Output = 1
	second.State.Passed = 1
	if err := store.SaveSnapshot(ctx, second); err != nil {
		t.Fatal(err)
	}
	records, err := store.NextOutbox(ctx, 100)
	if err != nil || len(records) < 3 {
		t.Fatalf("outbox rows = %d, err = %v", len(records), err)
	}
	last := records[len(records)-1].Sequence
	replay, err := store.BuildReplay(ctx, []uplink.Range{{
		From: "1", To: uplink.FormatSequence(last),
	}}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) > uplink.MaxReplayBytes {
		t.Fatalf("replay size = %d", len(replay))
	}
	var batch uplink.ReplayBatch
	if err := json.Unmarshal(replay, &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != len(records) {
		t.Fatalf("replay messages = %d, want %d", len(batch.Messages), len(records))
	}
	for index, item := range batch.Messages {
		var envelope struct {
			Sequence string `json:"sequence"`
			Replayed bool   `json:"replayed"`
		}
		if err := json.Unmarshal(item.Message, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Sequence != uplink.FormatSequence(records[index].Sequence) || !envelope.Replayed {
			t.Fatalf("replay item %d = %+v", index, envelope)
		}
		digest, err := uplink.ReliableDigest(item.Message)
		if err != nil || digest != records[index].IdentityDigest {
			t.Fatalf("replay identity %d changed: %s, %v", index, digest, err)
		}
	}
}

func TestCalibrationSuppressionDoesNotAllocateSequence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store := openUplinkStore(t, &now, 1, 16*1024)
	defer store.Close()
	record := uplinkSampleRecord(now, 1)
	if err := store.SaveSnapshot(ctx, record); err != nil {
		t.Fatal(err)
	}
	before, err := store.UplinkState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Second)
	if err := store.SaveSnapshot(ctx, record); err != nil {
		t.Fatal(err)
	}
	after, err := store.UplinkState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastProducedSequence != before.LastProducedSequence ||
		after.OutboxPending != before.OutboxPending {
		t.Fatalf("suppressed calibration allocated data: before=%+v after=%+v", before, after)
	}
}

func TestSyncRejectsMalformedFutureAndWrongTarget(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store := openUplinkStore(t, &now, 0, 0)
	defer store.Close()
	if err := store.SaveSnapshot(ctx, uplinkSampleRecord(now, 1)); err != nil {
		t.Fatal(err)
	}
	stream, _ := store.UplinkState(ctx)
	valid := syncJSON(t, stream, "40000000-0000-4000-8000-000000000001", "1", "ACK", "1", nil)
	cases := [][]byte{
		append(append([]byte{}, valid...), []byte(` {}`)...),
		[]byte(`{"schemaVersion":"1.0","schemaVersion":"1.0"}`),
		syncJSON(t, stream, "40000000-0000-4000-8000-000000000002", "2", "ACK", "2", nil),
	}
	var wrong map[string]any
	if err := json.Unmarshal(valid, &wrong); err != nil {
		t.Fatal(err)
	}
	wrong["target"].(map[string]any)["blockId"] = "other-block"
	wrongTarget, _ := json.Marshal(wrong)
	cases = append(cases, wrongTarget)
	for index, body := range cases {
		if _, err := store.ApplySyncJSON(ctx, body); !errors.Is(err, ErrInvalidSync) {
			t.Fatalf("case %d error = %v, want ErrInvalidSync", index, err)
		}
	}
}

func TestRequestRangeAdvancesApplicationWaterlineAndMustStartAfterIt(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store := openUplinkStore(t, &now, 0, 0)
	defer store.Close()
	first := uplinkSampleRecord(now, 1)
	if err := store.SaveSnapshot(ctx, first); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second := uplinkSampleRecord(now, 2)
	second.State.Output, second.State.Passed = 1, 1
	if err := store.SaveSnapshot(ctx, second); err != nil {
		t.Fatal(err)
	}
	stream, _ := store.UplinkState(ctx)
	if stream.LastProducedSequence < 3 {
		t.Fatalf("need multiple reliable rows, state=%+v", stream)
	}
	body := syncJSON(
		t, stream, "40000000-0000-4000-8000-000000000010", "1",
		"REQUEST_RANGE", "1",
		[]uplink.Range{{From: "2", To: uplink.FormatSequence(stream.LastProducedSequence)}},
	)
	result, err := store.ApplySyncJSON(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.LastAckedSequence != 1 ||
		result.State.OutboxPending != stream.OutboxPending-1 {
		t.Fatalf("REQUEST_RANGE did not apply contiguous ACK: before=%+v after=%+v", stream, result.State)
	}
	invalid := syncJSON(
		t, result.State, "40000000-0000-4000-8000-000000000011", "2",
		"REQUEST_RANGE", "1", []uplink.Range{{From: "1", To: "2"}},
	)
	if _, err := store.ApplySyncJSON(ctx, invalid); !errors.Is(err, ErrInvalidSync) {
		t.Fatalf("range at/below waterline error = %v", err)
	}
}

func TestCapacityExhaustionPreservesLocalStateAndNeverEvictsCriticalRows(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store := openUplinkStore(t, &now, 1, 3000)
	defer store.Close()
	for revision := uint64(1); revision <= 30; revision++ {
		record := uplinkSampleRecord(now, revision)
		record.State.Output = int(revision)
		record.State.Passed = int(revision)
		if err := store.SaveSnapshot(ctx, record); err != nil {
			t.Fatalf("local state revision %d failed at uplink capacity: %v", revision, err)
		}
		now = now.Add(time.Second)
	}
	loaded, err := store.LoadSnapshot(ctx)
	if err != nil || loaded.State.Revision != 30 {
		t.Fatalf("local snapshot did not continue: %+v, %v", loaded, err)
	}
	stream, err := store.UplinkState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stream.StorageStatus != "UPLINK_STORAGE_EXHAUSTED" &&
		stream.StorageStatus != "UPLINK_GAP_PENDING" {
		t.Fatalf("capacity status = %q", stream.StorageStatus)
	}
	var gaps, production int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM uplink_gap_ledger").Scan(&gaps); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM uplink_outbox WHERE retention_class = 'production'").Scan(&production); err != nil {
		t.Fatal(err)
	}
	if gaps == 0 || production == 0 {
		t.Fatalf("capacity evidence gaps=%d production=%d", gaps, production)
	}
}

func TestSequenceExhaustionDoesNotRollbackPLCOrLocalCommand(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store := openUplinkStore(t, &now, 0, 0)
	defer store.Close()
	if _, err := store.SavePLC(ctx, sampleSnapshot(now, 1, 1), now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE uplink_stream_state SET next_sequence = ?
		WHERE singleton_id = 1`, uplink.MaxSafeSequence+1); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Second)
	if _, err := store.SavePLC(ctx, sampleSnapshot(now, 2, 2), now, time.Minute); err != nil {
		t.Fatalf("PLC state rolled back at sequence exhaustion: %v", err)
	}
	loaded, err := store.LoadSnapshot(ctx)
	if err != nil || loaded.State.Revision != 2 {
		t.Fatalf("PLC state after sequence exhaustion = %+v, %v", loaded, err)
	}

	command := plccontract.Command{CommandID: "sequence-exhausted-command", Name: "pause"}
	meta := CommandMeta{Operator: "QA", RequestID: "sequence-exhausted-request"}
	if existing, err := store.BeginCommand(ctx, command, meta); err != nil || existing.Exists {
		t.Fatalf("begin command = %+v, %v", existing, err)
	}
	now = now.Add(time.Second)
	readback := sampleSnapshot(now, 3, 3)
	if _, err := store.CompleteCommand(
		ctx,
		plccontract.CommandResult{
			CommandID: command.CommandID, Name: command.Name,
			Status: plccontract.CommandExecuted, ControlRevision: 3,
		},
		&readback, now, time.Minute,
		OperationInput{Level: "info", Code: "0001", Text: "设备已暂停", At: now},
		AuditInput{
			Operator: meta.Operator, RequestID: meta.RequestID,
			Action: "command.pause", Message: "设备已暂停", Revision: 3,
		},
	); err != nil {
		t.Fatalf("local command rolled back at sequence exhaustion: %v", err)
	}
	commandRecord, err := store.BeginCommand(ctx, command, meta)
	if err != nil || !commandRecord.Exists ||
		commandRecord.Result.Status != plccontract.CommandExecuted {
		t.Fatalf("command record after sequence exhaustion = %+v, %v", commandRecord, err)
	}
	loaded, err = store.LoadSnapshot(ctx)
	if err != nil || loaded.State.Revision != 3 {
		t.Fatalf("command readback after sequence exhaustion = %+v, %v", loaded, err)
	}
	stream, err := store.UplinkState(ctx)
	if err != nil || stream.StorageStatus != "UPLINK_STORAGE_EXHAUSTED" {
		t.Fatalf("uplink exhaustion state = %+v, %v", stream, err)
	}
}

func TestReplayRequestIsSplitIntoContractBoundedBatches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	store := openUplinkStore(t, &now, 0, 0)
	defer store.Close()
	for revision := uint64(1); revision <= 55; revision++ {
		record := uplinkSampleRecord(now, revision)
		record.State.Output, record.State.Passed = int(revision), int(revision)
		if err := store.SaveSnapshot(ctx, record); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	stream, _ := store.UplinkState(ctx)
	batches, err := store.BuildReplayBatches(ctx, []uplink.Range{{
		From: "1", To: uplink.FormatSequence(stream.LastProducedSequence),
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) < 2 {
		t.Fatalf("replay was not split: %d batch(es)", len(batches))
	}
	var previous uint64
	for batchIndex, body := range batches {
		if len(body) > uplink.MaxReplayBytes {
			t.Fatalf("batch %d is %d bytes", batchIndex, len(body))
		}
		var batch uplink.ReplayBatch
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch.Messages) == 0 || len(batch.Messages) > uplink.MaxReplayMessages {
			t.Fatalf("batch %d item count = %d", batchIndex, len(batch.Messages))
		}
		for _, item := range batch.Messages {
			var envelope struct {
				Sequence string `json:"sequence"`
			}
			if err := json.Unmarshal(item.Message, &envelope); err != nil {
				t.Fatal(err)
			}
			sequence, err := uplink.ParseSequence("sequence", envelope.Sequence, true)
			if err != nil || sequence <= previous {
				t.Fatalf("replay order %d after %d, err=%v", sequence, previous, err)
			}
			previous = sequence
		}
	}
}

func openUplinkStore(t *testing.T, now *time.Time, ordinary, hard int64) *Store {
	t.Helper()
	store, err := OpenWithOptions(
		filepath.Join(t.TempDir(), "block.db"),
		func() time.Time { return *now },
		UplinkOptions{
			Enabled: true,
			Source: uplink.Source{
				SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
			},
			BootID:           "20000000-0000-4000-8000-000000000001",
			StreamGeneration: "1", StaleAfter: 5 * time.Second,
			OrdinaryLimit: ordinary, HardLimit: hard,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func uplinkSampleRecord(at time.Time, revision uint64) SnapshotRecord {
	return SnapshotRecord{
		State: state.Model{
			Revision: revision, UpdatedAt: at, Mode: "auto", Target: 100,
			Cycle: 10, ToolLimit: 100, InspectInterval: 5,
		},
		Meta: state.SourceMeta{
			SimulatorSessionID: "session-1", SampleSequence: revision,
			Quality: plccontract.QualityGood, PLCConnected: true, ReceivedAt: at,
		},
	}
}

func syncJSON(
	t *testing.T,
	stream UplinkStreamState,
	syncID, revision, action, highest string,
	ranges []uplink.Range,
) []byte {
	t.Helper()
	if ranges == nil {
		ranges = []uplink.Range{}
	}
	body, err := json.Marshal(uplink.Sync{
		SchemaVersion: uplink.SchemaVersion, SyncID: syncID, SyncRevision: revision,
		Target: uplink.SyncTarget{
			SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
		},
		IssuedAt: "2026-07-24T02:00:02Z", Action: action,
		StreamEpoch: stream.StreamEpoch, HighestContiguousSequence: highest,
		Ranges: ranges, AcceptedGapRanges: []uplink.Range{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
