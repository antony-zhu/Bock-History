package agent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"block.local/block-agent/internal/adapter"
	"block.local/block-agent/internal/config"
	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/plcsim"
	"block.local/block-agent/internal/storage"
)

type samplingAdapter struct {
	snapshot plccontract.Snapshot
	err      error
}

func (a samplingAdapter) Read(context.Context) (plccontract.Snapshot, error) {
	return a.snapshot, a.err
}

func (samplingAdapter) Execute(context.Context, plccontract.Command) (plccontract.CommandResult, error) {
	return plccontract.CommandResult{}, errors.New("not implemented")
}

func (samplingAdapter) Close() {}

func TestSampleOnceClassifiesSourceFailures(t *testing.T) {
	now := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	valid := runtimeSnapshot(now)
	tests := []struct {
		name     string
		device   samplingAdapter
		wantCode string
	}{
		{
			name: "transport unavailable",
			device: samplingAdapter{
				err: errors.Join(adapter.ErrUnavailable, errors.New("socket closed")),
			},
			wantCode: storage.AvailabilityDeviceUnavailable,
		},
		{
			name: "adapter malformed data",
			device: samplingAdapter{
				err: errors.Join(adapter.ErrBadData, errors.New("malformed JSON")),
			},
			wantCode: storage.AvailabilityBadQuality,
		},
		{
			name: "unclassified invalid snapshot",
			device: samplingAdapter{
				snapshot: func() plccontract.Snapshot {
					value := valid
					value.GeneratedAt = time.Time{}
					return value
				}(),
			},
			wantCode: storage.AvailabilityBadQuality,
		},
		{
			name: "reported BAD quality",
			device: samplingAdapter{
				snapshot: func() plccontract.Snapshot {
					value := valid
					value.Quality = plccontract.QualityBad
					return value
				}(),
			},
			wantCode: storage.AvailabilityBadQuality,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := storage.Open(filepath.Join(t.TempDir(), "block.db"), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			runtime := &Runtime{
				device: adapter.NewCoordinator(test.device), store: store, commandTimeout: time.Second,
				staleAfter: time.Minute, now: func() time.Time { return now },
			}
			_ = runtime.SampleOnce(context.Background())
			available, code := store.SourceAvailability()
			if available || code != test.wantCode {
				t.Fatalf("availability = %v, %q, want false, %q", available, code, test.wantCode)
			}
		})
	}
}

func TestSampleOnceKeepsStorageFailureClassifiedAsBackendUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	store, err := storage.Open(filepath.Join(t.TempDir(), "block.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{
		device: adapter.NewCoordinator(samplingAdapter{snapshot: runtimeSnapshot(now)}), store: store,
		commandTimeout: time.Second, staleAfter: time.Minute,
		now: func() time.Time { return now },
	}
	if err := runtime.SampleOnce(context.Background()); err == nil {
		t.Fatal("closed storage unexpectedly accepted a sample")
	}
	available, code := store.SourceAvailability()
	if available || code != storage.AvailabilityBackendUnavailable {
		t.Fatalf("storage failure availability = %v, %q", available, code)
	}
}

func TestUnreachableBDMDoesNotStopLocalRuntime(t *testing.T) {
	directory, err := os.MkdirTemp(os.Getenv("TEMP"), "bdm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	cfg := config.Agent{
		SiteID: "site-test", BlockID: "block-test", DeviceID: "device-test",
		Adapter:             config.AgentAdapter{Type: "disabled"},
		LocalAPISocket:      filepath.Join(directory, "agent.sock"),
		LocalAPISocketGroup: "test-hmi",
		DatabasePath:        filepath.Join(directory, "block.db"),
		SamplePeriod:        "20ms", StaleAfter: "100ms", CommandTimeout: "20ms",
		BDM: config.BDM{
			Enabled: true, Endpoint: "mqtts://192.168.1.105:8883",
			Principal:       "blk-0123456789abcdef0123456789abcdef",
			CAFile:          filepath.Join(directory, "missing-ca.crt"),
			ClientCertFile:  filepath.Join(directory, "missing-client.crt"),
			ClientKeyFile:   filepath.Join(directory, "missing-client.key"),
			SoftwareVersion: "test", OSVersion: "test", Architecture: "test",
			HardwareModel: "test", StreamGeneration: "1",
		},
	}
	runtime, err := Open(cfg, func() time.Time { return now })
	if err != nil {
		t.Fatalf("BDM certificate outage prevented local open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("local runtime stopped because BDM was unavailable: %v", err)
	default:
	}
	if _, err := runtime.Store().UplinkState(context.Background()); err != nil {
		t.Fatalf("local SQLite/uplink state unavailable during BDM outage: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("local runtime shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("local runtime did not shut down")
	}
}

func TestUnreachableBDMPreservesLocalOperationAlarmHistoryAndOutbox(t *testing.T) {
	directory, err := os.MkdirTemp(os.Getenv("TEMP"), "b-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	simulatorConfig := config.Simulator{
		IOSocket: filepath.Join(directory, "i"), IOSocketGroup: "test-io",
		ControlSocket: filepath.Join(directory, "c"), ControlSocketGroup: "test-control",
		StateFile: filepath.Join(directory, "s.json"), SamplePeriod: "20ms",
		Seed: 23, PassRate: 1, FaultInjectionEnabled: true,
		BinCapacities: []int{100, 100, 20}, InitialTarget: 50,
		InitialCycleSeconds: 1, InitialToolLimit: 100, InitialInspectInterval: 5,
	}
	engine, err := plcsim.Open(simulatorConfig, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	simulatorCancel, simulatorDone := startSimulator(t, engine, simulatorConfig)

	cfg := config.Agent{
		SiteID: "site-test", BlockID: "block-test", DeviceID: "device-test",
		Adapter:        config.AgentAdapter{Type: "simulator", IOSocket: simulatorConfig.IOSocket},
		LocalAPISocket: filepath.Join(directory, "a"), LocalAPISocketGroup: "test-hmi",
		DatabasePath: filepath.Join(directory, "b.db"),
		SamplePeriod: "20ms", StaleAfter: "100ms", CommandTimeout: "100ms",
		BDM: config.BDM{
			Enabled: true, Endpoint: "mqtts://192.168.1.105:8883",
			Principal:       "blk-0123456789abcdef0123456789abcdef",
			CAFile:          filepath.Join(directory, "missing-ca.crt"),
			ClientCertFile:  filepath.Join(directory, "missing-client.crt"),
			ClientKeyFile:   filepath.Join(directory, "missing-client.key"),
			SoftwareVersion: "test", OSVersion: "test", Architecture: "arm64",
			HardwareModel: "test", StreamGeneration: "1",
		},
	}
	runtime, err := Open(cfg, time.Now)
	if err != nil {
		t.Fatalf("BDM certificate outage prevented local open: %v", err)
	}
	runContext, runCancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- runtime.Run(runContext) }()
	client := unixClient(cfg.LocalAPISocket, 500*time.Millisecond)
	waitHTTPStatus(t, client, "/api/v1/state", http.StatusOK, 3*time.Second)

	response := unixRequest(
		t, client, http.MethodPost, "/api/v1/commands",
		[]byte(`{"command":"start","expectedRevision":1}`),
		map[string]string{
			"Content-Type":    "application/json",
			"X-Operator":      "QA",
			"Idempotency-Key": "bdm-outage-local-start",
		},
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf(
			"local start failed during BDM outage: status=%d body=%s",
			response.StatusCode, readResponse(t, response),
		)
	}
	_ = response.Body.Close()
	enabled := true
	if _, err := engine.SetFault(plccontract.FaultRequest{
		Fault: "severe_alarm", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		record, loadErr := runtime.Store().LoadSnapshot(context.Background())
		if loadErr == nil && len(record.State.Alarms) > 0 && len(record.State.History) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"local alarm/history did not persist during BDM outage: record=%+v err=%v",
				record, loadErr,
			)
		}
		time.Sleep(20 * time.Millisecond)
	}
	audit, err := runtime.Store().Audit(context.Background(), 10, nil)
	if err != nil || len(audit.Items) == 0 {
		t.Fatalf("local audit unavailable during BDM outage: %+v, %v", audit, err)
	}
	stream, err := runtime.Store().UplinkState(context.Background())
	if err != nil || stream.OutboxPending == 0 {
		t.Fatalf("reliable Outbox did not accumulate during BDM outage: %+v, %v", stream, err)
	}
	select {
	case err := <-runDone:
		t.Fatalf("local runtime stopped because BDM was unavailable: %v", err)
	default:
	}

	runCancel()
	if err := <-runDone; err != nil {
		t.Fatalf("local runtime shutdown: %v", err)
	}
	simulatorCancel()
	if err := <-simulatorDone; err != nil {
		t.Fatalf("simulator shutdown: %v", err)
	}
}

func runtimeSnapshot(now time.Time) plccontract.Snapshot {
	return plccontract.Snapshot{
		SchemaVersion: plccontract.SchemaVersion, SimulatorSessionID: "runtime-test",
		SampleSequence: 1, GeneratedAt: now, Quality: plccontract.QualityGood,
		Points: plccontract.Points{
			ControlRevision: 1, Mode: "auto", SafetyReady: true,
			GuardDoorClosed: true, PLCConnected: true, Target: 100,
			CycleSeconds: 1, ToolLimit: 100, InspectInterval: 10,
		},
	}
}
