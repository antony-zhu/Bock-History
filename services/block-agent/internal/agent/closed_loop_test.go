package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func TestUnixSocketClosedLoopDisconnectRecoveryAndAgentRestart(t *testing.T) {
	directory, err := os.MkdirTemp("", "blk-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	simulatorConfig := config.Simulator{
		IOSocket: filepath.Join(directory, "io.sock"), IOSocketGroup: "test-io",
		ControlSocket: filepath.Join(directory, "control.sock"), ControlSocketGroup: "test-control",
		StateFile: filepath.Join(directory, "simulator.json"), SamplePeriod: "20ms",
		Seed: 17, PassRate: 1, FaultInjectionEnabled: true,
		BinCapacities: []int{100, 100, 20}, InitialTarget: 50,
		InitialCycleSeconds: 1, InitialToolLimit: 100, InitialInspectInterval: 5,
	}
	engine, err := plcsim.Open(simulatorConfig, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	simulatorCancel, simulatorDone := startSimulator(t, engine, simulatorConfig)

	agentConfig := config.Agent{
		SiteID: "site-test", BlockID: "block-test", DeviceID: "device-test",
		Adapter:        config.AgentAdapter{Type: "simulator", IOSocket: simulatorConfig.IOSocket},
		LocalAPISocket: filepath.Join(directory, "agent.sock"), LocalAPISocketGroup: "test-hmi",
		DatabasePath: filepath.Join(directory, "block.db"),
		SamplePeriod: "20ms", StaleAfter: "100ms", CommandTimeout: "100ms",
	}
	runtime, err := Open(agentConfig, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	agentContext, agentCancel := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() { agentDone <- runtime.Run(agentContext) }()
	client := unixClient(agentConfig.LocalAPISocket, 500*time.Millisecond)
	waitHTTPStatus(t, client, "/api/v1/state", http.StatusOK, 3*time.Second)

	commandBody := []byte(`{"command":"start","expectedRevision":1}`)
	response := unixRequest(t, client, http.MethodPost, "/api/v1/commands", commandBody, map[string]string{
		"Content-Type": "application/json", "X-Operator": "QA", "Idempotency-Key": "start-e2e",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("start status = %d: %s", response.StatusCode, readResponse(t, response))
	}
	_ = response.Body.Close()
	time.Sleep(1100 * time.Millisecond)
	if _, err := engine.Tick(); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, client, 1, 3*time.Second)

	simulatorCancel()
	if err := <-simulatorDone; err != nil {
		t.Fatalf("simulator shutdown: %v", err)
	}
	waitHTTPStatus(t, client, "/api/v1/state", http.StatusServiceUnavailable, 3*time.Second)
	response = unixRequest(t, client, http.MethodPost, "/api/v1/commands", commandBody, map[string]string{
		"Content-Type": "application/json", "X-Operator": "QA", "Idempotency-Key": "start-e2e",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("offline idempotent replay status = %d: %s", response.StatusCode, readResponse(t, response))
	}
	_ = response.Body.Close()
	response = unixRequest(t, client, http.MethodPost, "/api/v1/commands", []byte(`{"command":"pause","expectedRevision":1}`), map[string]string{
		"Content-Type": "application/json", "X-Operator": "QA", "Idempotency-Key": "start-e2e",
	})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("offline idempotency conflict status = %d: %s", response.StatusCode, readResponse(t, response))
	}
	_ = response.Body.Close()
	select {
	case err := <-agentDone:
		t.Fatalf("block-agent exited when simulator disconnected: %v", err)
	default:
	}

	simulatorCancel, simulatorDone = startSimulator(t, engine, simulatorConfig)
	if _, err := engine.Tick(); err != nil {
		t.Fatal(err)
	}
	waitHTTPStatus(t, client, "/api/v1/state", http.StatusOK, 3*time.Second)
	agentCancel()
	if err := <-agentDone; err != nil {
		t.Fatalf("agent shutdown: %v", err)
	}

	restarted, err := Open(agentConfig, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restarted.Store().LoadSnapshot(context.Background())
	if err != nil || restored.State.Output < 1 {
		t.Fatalf("SQLite snapshot did not survive restart: %+v, %v", restored, err)
	}
	restartContext, restartCancel := context.WithCancel(context.Background())
	restartDone := make(chan error, 1)
	go func() { restartDone <- restarted.Run(restartContext) }()
	waitHTTPStatus(t, client, "/api/v1/state", http.StatusOK, 3*time.Second)
	enabled := true
	if _, err := engine.SetFault(plccontract.FaultRequest{Fault: "command_reject", Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	response = unixRequest(t, client, http.MethodPost, "/api/v1/commands", commandBody, map[string]string{
		"Content-Type": "application/json", "X-Operator": "QA", "Idempotency-Key": "start-e2e",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("persisted duplicate reached rejecting simulator: %d: %s", response.StatusCode, readResponse(t, response))
	}
	_ = response.Body.Close()
	restartCancel()
	if err := <-restartDone; err != nil {
		t.Fatalf("restarted agent shutdown: %v", err)
	}
	simulatorCancel()
	if err := <-simulatorDone; err != nil {
		t.Fatalf("simulator final shutdown: %v", err)
	}
}

func startSimulator(t *testing.T, engine *plcsim.Engine, cfg config.Simulator) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	server := plcsim.NewServer(engine, cfg.IOSocket, cfg.IOSocketGroup, cfg.ControlSocket, cfg.ControlSocketGroup)
	go func() { done <- server.Serve(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("simulator server stopped before socket was ready: %v", err)
		default:
		}
		connection, err := net.DialTimeout("unix", cfg.IOSocket, 20*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return cancel, done
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("simulator Unix socket did not become ready")
	return nil, nil
}

func unixClient(socket string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", socket)
		}},
	}
}

func unixRequest(t *testing.T, client *http.Client, method, path string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, "http://unix"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitHTTPStatus(t *testing.T, client *http.Client, path string, status int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus int
	for time.Now().Before(deadline) {
		response, err := client.Get("http://unix" + path)
		if err == nil {
			lastStatus = response.StatusCode
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if lastStatus == status {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not return %d; last status %d", path, status, lastStatus)
}

func waitOutput(t *testing.T, client *http.Client, minimum int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://unix/api/v1/state")
		if err == nil && response.StatusCode == http.StatusOK {
			var payload struct {
				State struct {
					Output int `json:"output"`
				} `json:"state"`
			}
			if json.NewDecoder(response.Body).Decode(&payload) == nil && payload.State.Output >= minimum {
				_ = response.Body.Close()
				return
			}
			_ = response.Body.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("output did not reach %d", minimum)
}

func readResponse(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
