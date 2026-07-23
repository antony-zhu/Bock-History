package adapter

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"block.local/block-agent/internal/config"
	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/plcsim"
)

func TestSimulatorAdapterFaultsAndAppliedTimeout(t *testing.T) {
	directory, err := os.MkdirTemp("", "blk-adapter-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	cfg := config.Simulator{
		IOSocket: filepath.Join(directory, "io.sock"), IOSocketGroup: "test-io",
		ControlSocket: filepath.Join(directory, "control.sock"), ControlSocketGroup: "test-control",
		StateFile: filepath.Join(directory, "state.json"), SamplePeriod: "1s", Seed: 1, PassRate: 1,
		FaultInjectionEnabled: true, BinCapacities: []int{20, 20, 10}, InitialTarget: 10,
		InitialCycleSeconds: 1, InitialToolLimit: 10, InitialInspectInterval: 2,
	}
	engine, err := plcsim.Open(cfg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- plcsim.NewServer(engine, cfg.IOSocket, cfg.IOSocketGroup, cfg.ControlSocket, cfg.ControlSocketGroup).Serve(ctx)
	}()
	waitSocket(t, cfg.IOSocket)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("simulator shutdown: %v", err)
		}
	}()

	client := NewSimulator(cfg.IOSocket, 200*time.Millisecond)
	defer client.Close()
	snapshot, err := client.Read(context.Background())
	if err != nil || snapshot.Quality != plccontract.QualityGood {
		t.Fatalf("initial read = %+v, %v", snapshot, err)
	}
	if _, err := engine.SetFault(plccontract.FaultRequest{Fault: "quality", Quality: plccontract.QualityUncertain}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = client.Read(context.Background())
	if err != nil || snapshot.Quality != plccontract.QualityUncertain {
		t.Fatalf("uncertain read = %+v, %v", snapshot, err)
	}

	delay := 300
	if _, err := engine.SetFault(plccontract.FaultRequest{Fault: "response_delay", DelayMillis: &delay}); err != nil {
		t.Fatal(err)
	}
	shortClient := NewSimulator(cfg.IOSocket, 30*time.Millisecond)
	if _, err := shortClient.Read(context.Background()); err == nil {
		t.Fatal("response delay did not trigger timeout")
	}
	shortClient.Close()
	delay = 0
	_, _ = engine.SetFault(plccontract.FaultRequest{Fault: "response_delay", DelayMillis: &delay})
	_, _ = engine.SetFault(plccontract.FaultRequest{Fault: "quality", Quality: ""})

	enabled := true
	_, _ = engine.SetFault(plccontract.FaultRequest{Fault: "command_reject", Enabled: &enabled})
	result, err := client.Execute(context.Background(), plccontract.Command{CommandID: "rejected", Name: "pause"})
	if err != nil || result.Status != plccontract.CommandRejected {
		t.Fatalf("reject fault result = %+v, %v", result, err)
	}
	_, _ = engine.SetFault(plccontract.FaultRequest{Fault: "command_reject", Enabled: boolPtr(false)})
	_, _ = engine.SetFault(plccontract.FaultRequest{Fault: "command_fail", Enabled: &enabled})
	result, err = client.Execute(context.Background(), plccontract.Command{CommandID: "failed", Name: "pause"})
	if err != nil || result.Status != plccontract.CommandFailed {
		t.Fatalf("fail fault result = %+v, %v", result, err)
	}
	_, _ = engine.SetFault(plccontract.FaultRequest{Fault: "command_fail", Enabled: boolPtr(false)})

	_, _ = engine.SetFault(plccontract.FaultRequest{Fault: "applied_response_timeout", Enabled: &enabled})
	unknownClient := NewSimulator(cfg.IOSocket, 40*time.Millisecond)
	command := plccontract.Command{CommandID: "applied-timeout", Name: "pause"}
	if _, err := unknownClient.Execute(context.Background(), command); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("applied timeout error = %v", err)
	}
	unknownClient.Close()
	appliedRevision := engine.Snapshot().Points.ControlRevision
	_, _ = engine.SetFault(plccontract.FaultRequest{Fault: "applied_response_timeout", Enabled: boolPtr(false)})
	result, err = client.Execute(context.Background(), command)
	if err != nil || result.Status != plccontract.CommandExecuted || result.ControlRevision != appliedRevision {
		t.Fatalf("dedup readback after applied timeout = %+v, %v", result, err)
	}
}

func TestDisabledAdapterFailsClosedWithoutNetworkFallback(t *testing.T) {
	device := Disabled{}
	if _, err := device.Read(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled read error = %v", err)
	}
	if _, err := device.Read(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled read was not classified unavailable: %v", err)
	}
	if _, err := device.Execute(context.Background(), plccontract.Command{CommandID: "disabled", Name: "start"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled command error = %v", err)
	}
}

func TestSimulatorReadClassifiesTransportAndBadPayloads(t *testing.T) {
	missing := NewSimulator(filepath.Join(t.TempDir(), "missing.sock"), 30*time.Millisecond)
	defer missing.Close()
	if _, err := missing.Read(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing socket classification = %v", err)
	}

	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"snapshot":`},
		{name: "wrong schema", body: `{"snapshot":{"schemaVersion":"wrong","simulatorSessionId":"session","generatedAt":"2026-07-22T00:00:00Z","quality":"GOOD","points":{}}}`},
		{name: "zero timestamp", body: `{"snapshot":{"schemaVersion":"block-plc-private/v1","simulatorSessionId":"session","generatedAt":"0001-01-01T00:00:00Z","quality":"GOOD","points":{}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			socket := startSnapshotPayloadServer(t, test.body)
			client := NewSimulator(socket, 200*time.Millisecond)
			defer client.Close()
			if _, err := client.Read(context.Background()); !errors.Is(err, ErrBadData) {
				t.Fatalf("bad payload classification = %v", err)
			}
		})
	}
}

func startSnapshotPayloadServer(t *testing.T, body string) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "blk-payload-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "payload.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("payload server shutdown: %v", err)
		}
	})
	return socket
}

func waitSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("unix", path, 20*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Unix socket did not become ready")
}

func boolPtr(value bool) *bool { return &value }
