package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/state"
)

func TestSQLiteWALSnapshotAuditAndDedupRecovery(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "block.db")
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	store, err := Open(databasePath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	mode, err := store.JournalMode(ctx)
	if err != nil || mode != "wal" {
		t.Fatalf("journal mode = %q, err = %v", mode, err)
	}
	record := sampleRecord(now)
	if err := store.SaveSnapshot(ctx, record); err != nil {
		t.Fatal(err)
	}
	command := plccontract.Command{CommandID: "command-1", Name: "pause"}
	meta := CommandMeta{Operator: "QA", RequestID: "request-1"}
	if existing, err := store.BeginCommand(ctx, command, meta); err != nil || existing.Exists {
		t.Fatalf("begin command = %+v, err = %v", existing, err)
	}
	result := plccontract.CommandResult{CommandID: command.CommandID, Name: command.Name, Status: plccontract.CommandExecuted, ControlRevision: 8}
	readback := plccontract.Snapshot{
		SchemaVersion: plccontract.SchemaVersion, SimulatorSessionID: "session-1", SampleSequence: 13,
		GeneratedAt: now, Quality: plccontract.QualityGood,
		Points: plccontract.Points{
			ControlRevision: 8, Running: false, Mode: "auto", SafetyReady: true,
			GuardDoorClosed: true, PLCConnected: true, Target: 100, Output: 5,
			CycleSeconds: 1, ToolLimit: 50, InspectInterval: 5,
			Alarms: []plccontract.Alarm{{AlarmID: "9001", Level: "danger", Code: "E_STOP", Text: "急停", Active: true, OccurredAt: now}},
		},
	}
	if _, err := store.CompleteCommand(ctx, result, &readback, now, time.Minute, OperationInput{
		Level: "info", Code: "0001", Text: "设备已暂停", At: now,
	}, AuditInput{
		Operator: "QA", Action: "command.pause", Message: "设备已暂停", Revision: 8,
		RequestID: "request-1", Details: map[string]any{"commandId": command.CommandID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(databasePath, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.LoadSnapshot(ctx)
	if err != nil || loaded.State.Running || loaded.State.Revision != 8 || len(loaded.State.Alarms) != 1 {
		t.Fatalf("restored snapshot = %+v, err = %v", loaded, err)
	}
	existing, err := reopened.BeginCommand(ctx, command, meta)
	if err != nil || !existing.Exists || existing.Result.Status != plccontract.CommandExecuted {
		t.Fatalf("dedup record = %+v, err = %v", existing, err)
	}
	page, err := reopened.Audit(ctx, 10, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].Operator != "QA" {
		t.Fatalf("audit page = %+v, err = %v", page, err)
	}
	for table, want := range map[string]int{"active_alarms": 1, "alarm_history": 1, "operation_history": 1, "command_records": 1, "audit_records": 1} {
		var count int
		if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
}

func TestPendingCommandBecomesUnknownAfterRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "block.db")
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	store, err := Open(databasePath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	command := plccontract.Command{CommandID: "crash-window", Name: "reset"}
	meta := CommandMeta{Operator: "recovery-operator", RequestID: "recovery-request"}
	if _, err := store.BeginCommand(ctx, command, meta); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	reopened, err := Open(databasePath, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	existing, err := reopened.BeginCommand(ctx, command, meta)
	if err != nil || !existing.Exists || existing.Result.Status != plccontract.CommandUnknown {
		t.Fatalf("pending recovery = %+v, err = %v", existing, err)
	}
	page, err := reopened.Audit(ctx, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Operator != meta.Operator ||
		page.Items[0].RequestID != meta.RequestID ||
		page.Items[0].Details["recovered"] != true {
		t.Fatalf("pending recovery audit = %+v", page.Items)
	}
	var operationStatus, operationMessage string
	if err := reopened.db.QueryRowContext(ctx, `
		SELECT status, message FROM operation_history WHERE command_id = ?`,
		command.CommandID).Scan(&operationStatus, &operationMessage); err != nil {
		t.Fatal(err)
	}
	if operationStatus != string(plccontract.CommandUnknown) || operationMessage == "" {
		t.Fatalf("pending recovery operation = %q, %q", operationStatus, operationMessage)
	}
}

func TestSavePLCAndCompleteCommandInterleaveWithoutStateRegression(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	store, err := Open(filepath.Join(t.TempDir(), "block.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveSnapshot(ctx, sampleRecord(now)); err != nil {
		t.Fatal(err)
	}
	command := plccontract.Command{CommandID: "interleaved-command", Name: "pause"}
	meta := CommandMeta{Operator: "QA", RequestID: "interleaved-request"}
	if _, err := store.BeginCommand(ctx, command, meta); err != nil {
		t.Fatal(err)
	}
	readback := sampleSnapshot(now.Add(time.Second), 13, 8)
	periodic := sampleSnapshot(now.Add(2*time.Second), 14, 9)
	start := make(chan struct{})
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, err := store.CompleteCommand(ctx, plccontract.CommandResult{
			CommandID: command.CommandID, Name: command.Name,
			Status: plccontract.CommandExecuted, ControlRevision: 8,
		}, &readback, now.Add(time.Second), time.Minute, OperationInput{
			Level: "info", Code: "0001", Text: "设备已暂停", At: now.Add(time.Second),
		}, AuditInput{
			Operator: meta.Operator, RequestID: meta.RequestID,
			Action: "command.pause", Message: "设备已暂停",
		})
		errorsChannel <- err
	}()
	go func() {
		defer wait.Done()
		<-start
		_, err := store.SavePLC(ctx, periodic, now.Add(2*time.Second), time.Minute)
		errorsChannel <- err
	}()
	close(start)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.LoadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Revision != 9 || loaded.Meta.SampleSequence != 14 {
		t.Fatalf("interleaved writers regressed state: %+v", loaded)
	}
	record, err := store.BeginCommand(ctx, command, meta)
	if err != nil || !record.Exists || record.Result.Status != plccontract.CommandExecuted {
		t.Fatalf("interleaved command record = %+v, %v", record, err)
	}
	page, err := store.Audit(ctx, 10, nil)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("interleaved audit = %+v, %v", page, err)
	}
}

func TestCompleteCommandRetryAfterUncertainCommitIsIdempotent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	store, err := Open(filepath.Join(t.TempDir(), "block.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveSnapshot(ctx, sampleRecord(now)); err != nil {
		t.Fatal(err)
	}
	command := plccontract.Command{CommandID: "uncertain-commit", Name: "pause"}
	meta := CommandMeta{Operator: "QA", RequestID: "uncertain-request"}
	if _, err := store.BeginCommand(ctx, command, meta); err != nil {
		t.Fatal(err)
	}
	result := plccontract.CommandResult{
		CommandID: command.CommandID, Name: command.Name,
		Status: plccontract.CommandExecuted, ControlRevision: 8,
	}
	readback := sampleSnapshot(now.Add(time.Second), 13, 8)
	operation := OperationInput{
		Level: "info", Code: "0001", Text: "设备已暂停", At: now.Add(time.Second),
	}
	audit := AuditInput{
		Operator: meta.Operator, RequestID: meta.RequestID,
		Action: "command.pause", Message: "设备已暂停",
	}
	injected := errors.New("commit acknowledgement lost")
	first := true
	store.afterCompleteCommit = func() error {
		if first {
			first = false
			return injected
		}
		return nil
	}
	if _, err := store.CompleteCommand(
		ctx, result, &readback, now.Add(time.Second), time.Minute, operation, audit,
	); !errors.Is(err, injected) {
		t.Fatalf("uncertain commit error = %v", err)
	}
	afterUncertainCommit, err := store.LoadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	historyCount := len(afterUncertainCommit.State.History)
	if historyCount == 0 {
		t.Fatal("uncertain commit did not durably append operation history")
	}
	retried, err := store.CompleteCommand(
		ctx, result, &readback, now.Add(time.Second), time.Minute, operation, audit,
	)
	if err != nil {
		t.Fatalf("idempotent completion retry = %v", err)
	}
	if retried == nil || retried.State.Revision != 8 ||
		len(retried.State.History) != historyCount {
		t.Fatalf("retried snapshot = %+v", retried)
	}
	if _, err := store.CompleteCommand(
		ctx, result, &readback, now.Add(time.Second), time.Minute, operation, audit,
	); err != nil {
		t.Fatalf("repeated terminal completion = %v", err)
	}
	for table, want := range map[string]int{
		"operation_history": 1,
		"audit_records":     1,
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE 1 = 1").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count after retry = %d, want %d", table, count, want)
		}
	}
	record, err := store.BeginCommand(ctx, command, meta)
	if err != nil || !record.Exists || record.Result.Status != plccontract.CommandExecuted {
		t.Fatalf("terminal command after retry = %+v, %v", record, err)
	}
}

func sampleSnapshot(generatedAt time.Time, sequence, revision uint64) plccontract.Snapshot {
	return plccontract.Snapshot{
		SchemaVersion: plccontract.SchemaVersion, SimulatorSessionID: "session-1",
		SampleSequence: sequence, GeneratedAt: generatedAt, Quality: plccontract.QualityGood,
		Points: plccontract.Points{
			ControlRevision: revision, Running: false, Mode: "auto",
			SafetyReady: true, GuardDoorClosed: true, PLCConnected: true,
			Target: 100, Output: 5, CycleSeconds: 1, ToolLimit: 50, InspectInterval: 5,
		},
	}
}

func sampleRecord(now time.Time) SnapshotRecord {
	return SnapshotRecord{
		State: state.Model{
			Revision: 7, UpdatedAt: now, Running: true, Mode: "auto",
			Target: 100, Output: 5, Cycle: 1, ToolLimit: 50, InspectInterval: 5,
			Alarms: []state.Alarm{{ID: 9001, Level: "danger", Code: "E_STOP", Text: "急停", OccurredAt: now}},
		},
		Meta: state.SourceMeta{
			SimulatorSessionID: "session-1", SampleSequence: 12,
			Quality: plccontract.QualityGood, PLCConnected: true, ReceivedAt: now,
		},
	}
}
