package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"block.local/block-agent/internal/adapter"
	"block.local/block-agent/internal/plccontract"
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
