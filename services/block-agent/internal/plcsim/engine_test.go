package plcsim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"block.local/block-agent/internal/config"
	"block.local/block-agent/internal/plccontract"
)

type fakeClock struct {
	value time.Time
}

func (c *fakeClock) Now() time.Time                 { return c.value }
func (c *fakeClock) Advance(duration time.Duration) { c.value = c.value.Add(duration) }

func TestDeterministicProductionAndControlRevision(t *testing.T) {
	clock := &fakeClock{value: time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)}
	engine := newTestEngine(t, clock)
	revision := uint64(1)
	result, err := engine.ApplyCommand(plccontract.Command{CommandID: "start-1", Name: "start", ExpectedControlRevision: &revision})
	if err != nil || result.Status != plccontract.CommandExecuted {
		t.Fatalf("start result = %+v, err = %v", result, err)
	}
	for range 5 {
		clock.Advance(time.Second)
		if _, err := engine.Tick(); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := engine.Snapshot()
	if snapshot.SampleSequence != 5 || snapshot.Points.Output != 5 {
		t.Fatalf("unexpected production snapshot: %+v", snapshot)
	}
	if snapshot.Points.Passed+snapshot.Points.NG != snapshot.Points.Inspected || snapshot.Points.Inspected > snapshot.Points.Output {
		t.Fatalf("inspection counters are inconsistent: %+v", snapshot.Points)
	}
	if snapshot.Points.ControlRevision != 2 {
		t.Fatalf("sampling changed controlRevision: %d", snapshot.Points.ControlRevision)
	}

	second := newTestEngineWithPath(t, clock, filepath.Join(t.TempDir(), "second.json"))
	if _, err := second.ApplyCommand(plccontract.Command{CommandID: "start-1", Name: "start", ExpectedControlRevision: &revision}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		clock.Advance(time.Second)
		if _, err := second.Tick(); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := second.Snapshot().Points.Passed, snapshot.Points.Passed; got != want {
		t.Fatalf("fixed seed produced non-repeatable result: %d != %d", got, want)
	}
}

func TestPauseAndSafetyFaultsStopProductionAndRejectStart(t *testing.T) {
	clock := &fakeClock{value: time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)}
	engine := newTestEngine(t, clock)
	_, _ = engine.ApplyCommand(plccontract.Command{CommandID: "start", Name: "start"})
	clock.Advance(time.Second)
	_, _ = engine.Tick()
	before := engine.Snapshot().Points.Output
	_, _ = engine.ApplyCommand(plccontract.Command{CommandID: "pause", Name: "pause"})
	clock.Advance(time.Second)
	_, _ = engine.Tick()
	if got := engine.Snapshot().Points.Output; got != before {
		t.Fatalf("pause allowed production: %d -> %d", before, got)
	}

	enabled := true
	if _, err := engine.SetFault(plccontract.FaultRequest{Fault: "emergency_stop", Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.ApplyCommand(plccontract.Command{CommandID: "unsafe-start-1", Name: "start"})
	if err != nil || result.Status != plccontract.CommandRejected {
		t.Fatalf("emergency stop start result = %+v, err = %v", result, err)
	}
	if result.Code != plccontract.ResultCodeSafetyInterlock {
		t.Fatalf("emergency stop code = %q", result.Code)
	}
	if _, err := engine.SetFault(plccontract.FaultRequest{Fault: "emergency_stop", Enabled: boolPointer(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.SetFault(plccontract.FaultRequest{Fault: "guard_door_open", Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	result, err = engine.ApplyCommand(plccontract.Command{CommandID: "unsafe-start-2", Name: "start"})
	if err != nil || result.Status != plccontract.CommandRejected {
		t.Fatalf("open guard start result = %+v, err = %v", result, err)
	}
	clock.Advance(time.Second)
	_, _ = engine.Tick()
	if got := engine.Snapshot().Points.Output; got != before {
		t.Fatalf("safety fault allowed production: %d -> %d", before, got)
	}
}

func TestCycleSecondsAndInspectionConservation(t *testing.T) {
	clock := &fakeClock{value: time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "cycle-state.json")
	cfg := testSimulatorConfig(path)
	cfg.InitialCycleSeconds = 30
	cfg.InitialInspectInterval = 2
	engine, err := Open(cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApplyCommand(plccontract.Command{CommandID: "cycle-start", Name: "start"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(29 * time.Second)
	if _, err := engine.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := engine.Snapshot().Points.Output; got != 0 {
		t.Fatalf("30 second cycle produced after 29 seconds: %d", got)
	}
	clock.Advance(time.Second)
	if _, err := engine.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := engine.Snapshot().Points.Output; got != 1 {
		t.Fatalf("first 30 second cycle output = %d", got)
	}
	clock.Advance(30 * time.Second)
	if _, err := engine.Tick(); err != nil {
		t.Fatal(err)
	}
	points := engine.Snapshot().Points
	if points.Output != 2 || points.Inspected != 1 || points.Passed+points.NG != points.Inspected {
		t.Fatalf("automatic inspection invariant failed: %+v", points)
	}
	manual, err := engine.ApplyCommand(plccontract.Command{CommandID: "manual-inspect", Name: "inspect"})
	if err != nil || manual.Status != plccontract.CommandExecuted {
		t.Fatalf("manual inspect = %+v, %v", manual, err)
	}
	points = engine.Snapshot().Points
	if points.Inspected != 2 || points.Passed+points.NG != 2 || points.Inspected > points.Output {
		t.Fatalf("manual inspection invariant failed: %+v", points)
	}
	rejected, err := engine.ApplyCommand(plccontract.Command{CommandID: "manual-inspect-extra", Name: "inspect"})
	if err != nil || rejected.Status != plccontract.CommandRejected || rejected.Code != plccontract.ResultCodeCommandRejected {
		t.Fatalf("extra inspection was not rejected: %+v, %v", rejected, err)
	}
}

func TestAlarmNotFoundAndRevisionConflictHaveStableCodes(t *testing.T) {
	clock := &fakeClock{value: time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)}
	engine := newTestEngine(t, clock)
	missing, err := engine.ApplyCommand(plccontract.Command{CommandID: "missing-alarm", Name: "acknowledge_alarm", AlarmID: "999"})
	if err != nil || missing.Code != plccontract.ResultCodeAlarmNotFound {
		t.Fatalf("missing alarm = %+v, %v", missing, err)
	}
	revision := uint64(999)
	conflict, err := engine.ApplyCommand(plccontract.Command{CommandID: "wrong-revision", Name: "pause", ExpectedControlRevision: &revision})
	if err != nil || conflict.Code != plccontract.ResultCodeRevisionConflict {
		t.Fatalf("revision conflict = %+v, %v", conflict, err)
	}
}

func TestSettingsReadbackAndCommandDeduplication(t *testing.T) {
	clock := &fakeClock{value: time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)}
	engine := newTestEngine(t, clock)
	command := plccontract.Command{
		CommandID: "settings-1", Name: "update_settings",
		Settings: &plccontract.Settings{Target: 77, ToolLimit: 44, InspectInterval: 7},
	}
	first, err := engine.ApplyCommand(command)
	if err != nil || first.Status != plccontract.CommandExecuted || !first.SimulationOnly {
		t.Fatalf("settings result = %+v, err = %v", first, err)
	}
	second, err := engine.ApplyCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	if second.ControlRevision != first.ControlRevision || engine.Snapshot().Points.Target != 77 {
		t.Fatalf("duplicate command executed again: first=%+v second=%+v snapshot=%+v", first, second, engine.Snapshot())
	}
	conflict := command
	conflict.Settings = &plccontract.Settings{Target: 999, ToolLimit: 44, InspectInterval: 7}
	conflicting, err := engine.ApplyCommand(conflict)
	if err != nil || conflicting.Status != plccontract.CommandRejected || conflicting.Code != plccontract.ResultCodeIdempotencyConflict {
		t.Fatalf("conflicting commandId result = %+v, err = %v", conflicting, err)
	}
	if engine.Snapshot().Points.Target != 77 {
		t.Fatalf("conflicting command changed state: %+v", engine.Snapshot().Points)
	}
}

func TestPersistenceRestartAndCorruptionHandling(t *testing.T) {
	directory := t.TempDir()
	stateFile := filepath.Join(directory, "state.json")
	clock := &fakeClock{value: time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)}
	engine := newTestEngineWithPath(t, clock, stateFile)
	first, err := engine.ApplyCommand(plccontract.Command{CommandID: "pause-once", Name: "pause"})
	if err != nil || first.Status != plccontract.CommandExecuted {
		t.Fatalf("initial persisted command = %+v, %v", first, err)
	}
	if _, ok := engine.state.Processed["pause-once"]; !ok {
		t.Fatal("processed command missing before restart")
	}
	clock.Advance(time.Second)
	_, _ = engine.Tick()
	before := engine.Snapshot()

	reopened, err := Open(testSimulatorConfig(stateFile), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	after := reopened.Snapshot()
	if after.SimulatorSessionID == before.SimulatorSessionID {
		t.Fatal("simulator restart did not create a new session id")
	}
	if after.SampleSequence != before.SampleSequence || after.Points.ControlRevision != before.Points.ControlRevision {
		t.Fatalf("restart lost persisted counters: before=%+v after=%+v", before, after)
	}
	if _, ok := reopened.state.Processed["pause-once"]; !ok {
		t.Fatal("processed command missing after restart")
	}
	duplicate, err := reopened.ApplyCommand(plccontract.Command{CommandID: "pause-once", Name: "pause"})
	if err != nil || duplicate.ControlRevision != before.Points.ControlRevision {
		t.Fatalf("processed command was not recovered: %+v, %v", duplicate, err)
	}
	conflicting, err := reopened.ApplyCommand(plccontract.Command{CommandID: "pause-once", Name: "start"})
	if err != nil || conflicting.Code != plccontract.ResultCodeIdempotencyConflict || conflicting.Status != plccontract.CommandRejected {
		t.Fatalf("restart idempotency conflict = %+v, %v", conflicting, err)
	}

	corruptPath := filepath.Join(directory, "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(testSimulatorConfig(corruptPath), clock.Now); err == nil {
		t.Fatal("corrupt simulator state was silently accepted")
	}
}

func TestPersistedStateRejectsInvalidCriticalSettings(t *testing.T) {
	clock := &fakeClock{value: time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)}
	basePath := filepath.Join(t.TempDir(), "base.json")
	engine := newTestEngineWithPath(t, clock, basePath)
	base := clonePersisted(engine.state)
	tests := []struct {
		name   string
		mutate func(*persistedState)
	}{
		{
			name: "zero cycle",
			mutate: func(value *persistedState) {
				value.Points.CycleSeconds = 0
			},
		},
		{
			name: "negative cycle",
			mutate: func(value *persistedState) {
				value.Points.CycleSeconds = -1
			},
		},
		{
			name: "zero target",
			mutate: func(value *persistedState) {
				value.Points.Target = 0
			},
		},
		{
			name: "zero tool limit",
			mutate: func(value *persistedState) {
				value.Points.ToolLimit = 0
			},
		},
		{
			name: "zero inspection interval",
			mutate: func(value *persistedState) {
				value.Points.InspectInterval = 0
			},
		},
		{
			name: "invalid mode",
			mutate: func(value *persistedState) {
				value.Points.Mode = "unexpected"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := clonePersisted(base)
			test.mutate(&state)
			contents, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(testSimulatorConfig(path), clock.Now); err == nil {
				t.Fatalf("invalid persisted setting %q was accepted", test.name)
			}
		})
	}
}

func newTestEngine(t *testing.T, clock *fakeClock) *Engine {
	t.Helper()
	return newTestEngineWithPath(t, clock, filepath.Join(t.TempDir(), "state.json"))
}

func newTestEngineWithPath(t *testing.T, clock *fakeClock, path string) *Engine {
	t.Helper()
	engine, err := Open(testSimulatorConfig(path), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func testSimulatorConfig(path string) config.Simulator {
	return config.Simulator{
		IOSocket: filepath.Join(filepath.Dir(path), "io.sock"), IOSocketGroup: "test-io",
		ControlSocket: filepath.Join(filepath.Dir(path), "control.sock"), ControlSocketGroup: "test-control",
		StateFile:    path,
		SamplePeriod: "1s", Seed: 42, PassRate: 0.8,
		FaultInjectionEnabled: true,
		BinCapacities:         []int{100, 100, 20},
		InitialTarget:         50, InitialCycleSeconds: 1, InitialToolLimit: 100, InitialInspectInterval: 2,
	}
}

func boolPointer(value bool) *bool { return &value }
