package command

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"block.local/block-agent/internal/adapter"
	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/state"
	"block.local/block-agent/internal/storage"
)

type fakeAdapter struct {
	mu             sync.Mutex
	snapshot       plccontract.Snapshot
	active         int
	maxActive      int
	calls          int
	unknownOutcome bool
	blockFirst     bool
	executeStarted chan struct{}
	executeRelease chan struct{}
	qualityOnRead  plccontract.Quality
	resultHook     func(plccontract.Command, uint64) plccontract.CommandResult
}

func (f *fakeAdapter) Read(context.Context) (plccontract.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.qualityOnRead != "" {
		f.snapshot.Quality = f.qualityOnRead
	}
	return f.snapshot, nil
}

func (f *fakeAdapter) Execute(_ context.Context, command plccontract.Command) (plccontract.CommandResult, error) {
	f.mu.Lock()
	f.active++
	f.calls++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	block := f.blockFirst && f.calls == 1
	started, release := f.executeStarted, f.executeRelease
	f.mu.Unlock()
	if block {
		close(started)
		<-release
	}
	time.Sleep(5 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() { f.active-- }()
	f.snapshot.Points.ControlRevision++
	if command.Name == "pause" {
		f.snapshot.Points.Running = false
	}
	if f.unknownOutcome {
		return plccontract.CommandResult{}, adapter.ErrOutcomeUnknown
	}
	if f.resultHook != nil {
		return f.resultHook(command, f.snapshot.Points.ControlRevision), nil
	}
	return plccontract.CommandResult{
		CommandID: command.CommandID, Name: command.Name,
		Status: plccontract.CommandExecuted, ControlRevision: f.snapshot.Points.ControlRevision,
	}, nil
}

func (f *fakeAdapter) Close() {}

func TestQueueSerializesConcurrentWritesAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	store := openQueueStore(t, now)
	defer store.Close()
	device := newFakeAdapter(now)
	queue := New(adapter.NewCoordinator(device), store, time.Second, time.Minute, func() time.Time { return now })
	defer queue.Close()

	const operations = 20
	var wait sync.WaitGroup
	errorsChannel := make(chan error, operations)
	for index := range operations {
		wait.Add(1)
		go func(value int) {
			defer wait.Done()
			outcome, err := queue.Submit(ctx, plccontract.Command{CommandID: "inspect-" + time.Unix(int64(value), 0).Format("150405.000000000"), Name: "inspect"}, Meta{Operator: "QA"})
			if err != nil || outcome.Result.Status != plccontract.CommandExecuted {
				errorsChannel <- errors.New("command did not execute")
			}
		}(index)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
	device.mu.Lock()
	maxActive, calls := device.maxActive, device.calls
	device.mu.Unlock()
	if maxActive != 1 || calls != operations {
		t.Fatalf("writes were not strictly serialized: maxActive=%d calls=%d", maxActive, calls)
	}

	command := plccontract.Command{CommandID: "dedup-1", Name: "pause"}
	first, err := queue.Submit(ctx, command, Meta{Operator: "QA"})
	if err != nil || first.Result.Status != plccontract.CommandExecuted {
		t.Fatalf("first dedup command = %+v, %v", first, err)
	}
	device.mu.Lock()
	callsBefore := device.calls
	device.mu.Unlock()
	second, err := queue.Submit(ctx, command, Meta{Operator: "QA"})
	if err != nil || second.Result.Status != plccontract.CommandExecuted {
		t.Fatalf("second dedup command = %+v, %v", second, err)
	}
	device.mu.Lock()
	callsAfter := device.calls
	device.mu.Unlock()
	if callsAfter != callsBefore {
		t.Fatalf("duplicate command reached adapter: %d -> %d", callsBefore, callsAfter)
	}
	if err := store.MarkStale(ctx, storage.AvailabilityDataStale); err != nil {
		t.Fatal(err)
	}
	replayedOffline, err := queue.Submit(ctx, command, Meta{Operator: "QA"})
	if err != nil || replayedOffline.Result.Status != plccontract.CommandExecuted {
		t.Fatalf("offline replay = %+v, %v", replayedOffline, err)
	}
	device.mu.Lock()
	callsAfterOfflineReplay := device.calls
	device.mu.Unlock()
	if callsAfterOfflineReplay != callsAfter {
		t.Fatalf("offline duplicate reached adapter: %d -> %d", callsAfter, callsAfterOfflineReplay)
	}
	conflicting := command
	conflicting.Name = "start"
	if _, err := queue.Submit(ctx, conflicting, Meta{Operator: "QA"}); !errors.Is(err, storage.ErrIdempotencyConflict) {
		t.Fatalf("offline idempotency conflict = %v", err)
	}
}

func TestExecutedButResponseTimeoutIsUnknownAndNeverRetried(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	store := openQueueStore(t, now)
	defer store.Close()
	device := newFakeAdapter(now)
	device.unknownOutcome = true
	queue := New(adapter.NewCoordinator(device), store, 50*time.Millisecond, time.Minute, func() time.Time { return now })
	defer queue.Close()
	command := plccontract.Command{CommandID: "timeout-after-apply", Name: "reset"}
	first, err := queue.Submit(ctx, command, Meta{Operator: "QA"})
	if err != nil || first.Result.Status != plccontract.CommandUnknown {
		t.Fatalf("first timeout result = %+v, err = %v", first, err)
	}
	second, err := queue.Submit(ctx, command, Meta{Operator: "QA"})
	if err != nil || second.Result.Status != plccontract.CommandUnknown {
		t.Fatalf("dedup timeout result = %+v, err = %v", second, err)
	}
	device.mu.Lock()
	calls := device.calls
	device.mu.Unlock()
	if calls != 1 {
		t.Fatalf("unknown command was automatically retried %d times", calls)
	}
}

func TestQueuedCommandRechecksFreshnessAndQualityImmediatelyBeforeExecute(t *testing.T) {
	for _, test := range []struct {
		name     string
		makeBad  func(*fakeAdapter, *storage.Store, *atomicClock)
		wantCode string
	}{
		{
			name: "stale",
			makeBad: func(_ *fakeAdapter, _ *storage.Store, clock *atomicClock) {
				clock.Advance(2 * time.Minute)
			},
			wantCode: storage.AvailabilityDataStale,
		},
		{
			name: "bad quality",
			makeBad: func(device *fakeAdapter, _ *storage.Store, _ *atomicClock) {
				device.mu.Lock()
				device.qualityOnRead = plccontract.QualityBad
				device.mu.Unlock()
			},
			wantCode: storage.AvailabilityBadQuality,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			initial := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
			clock := newAtomicClock(initial)
			store := openQueueStoreWithClock(t, clock.Now)
			defer store.Close()
			device := newFakeAdapter(initial)
			device.blockFirst = true
			device.executeStarted = make(chan struct{})
			device.executeRelease = make(chan struct{})
			queue := New(adapter.NewCoordinator(device), store, time.Second, time.Minute, clock.Now)
			defer queue.Close()

			firstDone := make(chan error, 1)
			go func() {
				_, err := queue.Submit(context.Background(), plccontract.Command{
					CommandID: "blocking-first", Name: "pause",
				}, Meta{Operator: "QA"})
				firstDone <- err
			}()
			<-device.executeStarted
			secondDone := make(chan response, 1)
			go func() {
				outcome, err := queue.Submit(context.Background(), plccontract.Command{
					CommandID: "must-not-execute", Name: "start",
				}, Meta{Operator: "QA"})
				secondDone <- response{outcome: outcome, err: err}
			}()
			test.makeBad(device, store, clock)
			close(device.executeRelease)
			if err := <-firstDone; err != nil {
				t.Fatal(err)
			}
			second := <-secondDone
			if second.err != nil || second.outcome.Result.Status != plccontract.CommandRejected ||
				second.outcome.Result.Code != test.wantCode {
				t.Fatalf("queued command result = %+v, %v", second.outcome, second.err)
			}
			device.mu.Lock()
			calls := device.calls
			device.mu.Unlock()
			if calls != 1 {
				t.Fatalf("unavailable queued command reached adapter: calls=%d", calls)
			}
		})
	}
}

func TestReadinessAndExecuteExcludeConcurrentSourceTransition(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	store := openQueueStore(t, now)
	defer store.Close()
	device := newFakeAdapter(now)
	device.blockFirst = true
	device.executeStarted = make(chan struct{})
	device.executeRelease = make(chan struct{})
	coordinator := adapter.NewCoordinator(device)
	queue := New(coordinator, store, time.Second, time.Minute, func() time.Time { return now })
	defer queue.Close()

	readinessChecked := make(chan struct{})
	allowExecute := make(chan struct{})
	originalCheck := queue.checkAvailability
	queue.checkAvailability = func(ctx context.Context) (string, string) {
		code, reason := originalCheck(ctx)
		close(readinessChecked)
		<-allowExecute
		return code, reason
	}

	commandDone := make(chan response, 1)
	go func() {
		outcome, err := queue.Submit(context.Background(), plccontract.Command{
			CommandID: "atomic-check-execute", Name: "pause",
		}, Meta{Operator: "QA"})
		commandDone <- response{outcome: outcome, err: err}
	}()
	<-readinessChecked

	transitionAttempted := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		close(transitionAttempted)
		coordinator.Do(func(adapter.Adapter) {
			transitionDone <- store.MarkStale(context.Background(), storage.AvailabilityDataStale)
		})
	}()
	<-transitionAttempted
	close(allowExecute)
	select {
	case <-device.executeStarted:
		// The command entered Execute while still holding the coordinator.
	case err := <-transitionDone:
		t.Fatalf("source transitioned after readiness but before Execute: %v", err)
	}
	select {
	case err := <-transitionDone:
		t.Fatalf("source transition entered while Execute was in progress: %v", err)
	default:
	}
	close(device.executeRelease)
	completed := <-commandDone
	if completed.err != nil || completed.outcome.Result.Status != plccontract.CommandExecuted {
		t.Fatalf("atomic command outcome = %+v, %v", completed.outcome, completed.err)
	}
	if err := <-transitionDone; err != nil {
		t.Fatal(err)
	}
	available, code := store.SourceAvailability()
	if available || code != storage.AvailabilityDataStale {
		t.Fatalf("post-command transition = %v, %q", available, code)
	}
	device.mu.Lock()
	calls := device.calls
	device.mu.Unlock()
	if calls != 1 {
		t.Fatalf("atomic command adapter calls = %d", calls)
	}
}

func TestClientCancellationAfterAdmissionStillPersistsOneTerminalOutcome(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	store := openQueueStore(t, now)
	defer store.Close()
	device := newFakeAdapter(now)
	device.blockFirst = true
	device.executeStarted = make(chan struct{})
	device.executeRelease = make(chan struct{})
	queue := New(adapter.NewCoordinator(device), store, time.Second, time.Minute, func() time.Time { return now })
	defer queue.Close()

	ctx, cancel := context.WithCancel(context.Background())
	command := plccontract.Command{CommandID: "cancel-after-admission", Name: "pause"}
	submitDone := make(chan error, 1)
	go func() {
		_, err := queue.Submit(ctx, command, Meta{Operator: "cancel-user", RequestID: "cancel-request"})
		submitDone <- err
	}()
	<-device.executeStarted
	cancel()
	if err := <-submitDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled submit error = %v", err)
	}
	close(device.executeRelease)

	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := store.BeginCommand(context.Background(), command, storage.CommandMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if record.Exists && record.Result.Status == plccontract.CommandExecuted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal outcome was not persisted: %+v", record)
		}
		time.Sleep(10 * time.Millisecond)
	}
	device.mu.Lock()
	calls := device.calls
	device.mu.Unlock()
	if calls != 1 {
		t.Fatalf("canceled command executed %d times", calls)
	}
	page, err := store.Audit(context.Background(), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Operator != "cancel-user" ||
		page.Items[0].RequestID != "cancel-request" {
		t.Fatalf("canceled command audit = %+v", page.Items)
	}
}

func TestInvalidAdapterCommandResponsesBecomeUnknownWithoutStoppingQueue(t *testing.T) {
	tests := []struct {
		name string
		hook func(plccontract.Command, uint64) plccontract.CommandResult
	}{
		{
			name: "wrong command id",
			hook: func(command plccontract.Command, revision uint64) plccontract.CommandResult {
				return plccontract.CommandResult{
					CommandID: "different", Name: command.Name,
					Status: plccontract.CommandExecuted, ControlRevision: revision,
				}
			},
		},
		{
			name: "wrong command name",
			hook: func(command plccontract.Command, revision uint64) plccontract.CommandResult {
				return plccontract.CommandResult{
					CommandID: command.CommandID, Name: "different",
					Status: plccontract.CommandExecuted, ControlRevision: revision,
				}
			},
		},
		{
			name: "nonterminal status",
			hook: func(command plccontract.Command, revision uint64) plccontract.CommandResult {
				return plccontract.CommandResult{
					CommandID: command.CommandID, Name: command.Name,
					Status: plccontract.CommandPending, ControlRevision: revision,
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
			store := openQueueStore(t, now)
			defer store.Close()
			device := newFakeAdapter(now)
			device.resultHook = func(command plccontract.Command, revision uint64) plccontract.CommandResult {
				if command.CommandID == "bad-response" {
					return test.hook(command, revision)
				}
				return plccontract.CommandResult{
					CommandID: command.CommandID, Name: command.Name,
					Status: plccontract.CommandExecuted, ControlRevision: revision,
				}
			}
			queue := New(adapter.NewCoordinator(device), store, time.Second, time.Minute, func() time.Time { return now })
			defer queue.Close()

			badCommand := plccontract.Command{CommandID: "bad-response", Name: "pause"}
			first, err := queue.Submit(context.Background(), badCommand, Meta{Operator: "QA"})
			if err != nil || first.Result.Status != plccontract.CommandUnknown ||
				first.Result.CommandID != badCommand.CommandID || first.Result.Name != badCommand.Name {
				t.Fatalf("invalid response outcome = %+v, %v", first, err)
			}
			replay, err := queue.Submit(context.Background(), badCommand, Meta{Operator: "QA"})
			if err != nil || replay.Result.Status != plccontract.CommandUnknown {
				t.Fatalf("invalid response replay = %+v, %v", replay, err)
			}
			next, err := queue.Submit(context.Background(), plccontract.Command{
				CommandID: "queue-still-running", Name: "pause",
			}, Meta{Operator: "QA"})
			if err != nil || next.Result.Status != plccontract.CommandExecuted {
				t.Fatalf("queue stopped after invalid response: %+v, %v", next, err)
			}
			select {
			case fatal := <-queue.Errors():
				t.Fatalf("queue reported fatal error: %v", fatal)
			default:
			}
			device.mu.Lock()
			calls := device.calls
			device.mu.Unlock()
			if calls != 2 {
				t.Fatalf("adapter calls = %d, want invalid once plus next command", calls)
			}
		})
	}
}

func openQueueStore(t *testing.T, now time.Time) *storage.Store {
	t.Helper()
	return openQueueStoreWithClock(t, func() time.Time { return now })
}

func openQueueStoreWithClock(t *testing.T, now func() time.Time) *storage.Store {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "block.db"), now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := newFakeAdapter(now()).snapshot
	model, meta := state.FromPLC(snapshot, state.Model{})
	meta.ReceivedAt = now()
	if err := store.SaveSnapshot(context.Background(), storage.SnapshotRecord{State: model, Meta: meta}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store
}

type atomicClock struct {
	mu    sync.Mutex
	value time.Time
}

func newAtomicClock(value time.Time) *atomicClock {
	return &atomicClock{value: value}
}

func (c *atomicClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func (c *atomicClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.value = c.value.Add(duration)
	c.mu.Unlock()
}

func newFakeAdapter(now time.Time) *fakeAdapter {
	return &fakeAdapter{snapshot: plccontract.Snapshot{
		SchemaVersion: plccontract.SchemaVersion, SimulatorSessionID: "session-1",
		SampleSequence: 1, GeneratedAt: now, Quality: plccontract.QualityGood,
		Points: plccontract.Points{
			ControlRevision: 1, Running: true, Mode: "auto", SafetyReady: true,
			GuardDoorClosed: true, PLCConnected: true, Target: 100,
			CycleSeconds: 1, ToolLimit: 100, InspectInterval: 10,
		},
	}}
}
