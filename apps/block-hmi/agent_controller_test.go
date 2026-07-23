package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentControllerUsesUnixSocketAndForwardsIdempotency(t *testing.T) {
	var mu sync.Mutex
	seenCommandID := ""
	seenOperator := ""
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Block-Source-Kind", "simulator")
		w.Header().Set("X-Block-Simulation", "true")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/v1/source":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schemaVersion": "block-local-private/v1",
				"source":        map[string]any{"kind": "simulator", "simulation": true},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": HMIState{Revision: 1, Mode: "auto", UpdatedAt: time.Now()}})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/settings":
			mu.Lock()
			seenCommandID = r.Header.Get("Idempotency-Key")
			seenOperator = r.Header.Get("X-Operator")
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"state": HMIState{Revision: 2, Mode: "auto", Target: 55}, "message": "saved"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/audit":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []AuditEntry{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": "missing"}})
		}
	})
	socket, stop := startUnixTestServer(t, handler)
	defer stop()
	controller, err := newAgentController(socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	source, err := controller.SourceInfo(context.Background())
	if err != nil || !source.Simulation || source.Kind != "simulator" {
		t.Fatalf("source = %+v, err = %v", source, err)
	}
	state, err := controller.State(context.Background())
	if err != nil || state.Revision != 1 {
		t.Fatalf("state = %+v, err = %v", state, err)
	}
	expected := uint64(1)
	state, message, err := controller.UpdateSettings(context.Background(), Parameters{Target: 55, ToolLimit: 10, InspectInterval: 2}, MutationMeta{
		Operator: "QA", RequestID: "request-1", CommandID: "stable-command-1",
	}, &expected)
	if err != nil || state.Target != 55 || message != "saved" {
		t.Fatalf("settings = %+v %q, err = %v", state, message, err)
	}
	mu.Lock()
	commandID, operator := seenCommandID, seenOperator
	mu.Unlock()
	if commandID != "stable-command-1" || operator != "QA" {
		t.Fatalf("forwarded headers commandId=%q operator=%q", commandID, operator)
	}
}

func TestAgentControllerFailsClosedWhenSourceChanges(t *testing.T) {
	var mu sync.RWMutex
	kind := "simulator"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		current := kind
		mu.RUnlock()
		simulation := current == "simulator"
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Block-Source-Kind", current)
		w.Header().Set("X-Block-Simulation", fmt.Sprintf("%t", simulation))
		if r.URL.Path == "/internal/v1/source" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schemaVersion": "block-local-private/v1",
				"source":        map[string]any{"kind": current, "simulation": simulation},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": HMIState{Revision: 1, UpdatedAt: time.Now()}})
	})
	socket, stop := startUnixTestServer(t, handler)
	defer stop()
	controller, err := newAgentController(socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if source, err := controller.SourceInfo(context.Background()); err != nil || !source.Simulation {
		t.Fatalf("initial source = %+v, %v", source, err)
	}
	mu.Lock()
	kind = "disabled"
	mu.Unlock()
	if _, err := controller.State(context.Background()); !errors.Is(err, errSourceMismatch) {
		t.Fatalf("source change error = %v", err)
	}
}

func TestAgentControllerSourceInfoRequiresCompleteConsistentHeaders(t *testing.T) {
	tests := []struct {
		name             string
		bodyKind         string
		bodySimulation   bool
		headerKind       string
		headerSimulation string
	}{
		{
			name:             "missing source kind header",
			bodyKind:         "simulator",
			bodySimulation:   true,
			headerSimulation: "true",
		},
		{
			name:           "missing simulation header",
			bodyKind:       "simulator",
			bodySimulation: true,
			headerKind:     "simulator",
		},
		{
			name:             "kind differs from body",
			bodyKind:         "simulator",
			bodySimulation:   true,
			headerKind:       "disabled",
			headerSimulation: "true",
		},
		{
			name:             "simulation differs from body",
			bodyKind:         "simulator",
			bodySimulation:   true,
			headerKind:       "simulator",
			headerSimulation: "false",
		},
		{
			name:             "incomplete body",
			bodyKind:         "",
			bodySimulation:   false,
			headerKind:       "disabled",
			headerSimulation: "false",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if test.headerKind != "" {
					w.Header().Set("X-Block-Source-Kind", test.headerKind)
				}
				if test.headerSimulation != "" {
					w.Header().Set("X-Block-Simulation", test.headerSimulation)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"schemaVersion": "block-local-private/v1",
					"source": map[string]any{
						"kind":       test.bodyKind,
						"simulation": test.bodySimulation,
					},
				})
			})
			socket, stop := startUnixTestServer(t, handler)
			defer stop()
			controller, err := newAgentController(socket, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			if source, err := controller.SourceInfo(context.Background()); err == nil {
				t.Fatalf("inconsistent source metadata was accepted: %+v", source)
			}
		})
	}
}

func TestAcceptedAgentMutationTimeoutReturnsOutcomeUnknown(t *testing.T) {
	accepted := make(chan string, 1)
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Block-Source-Kind", "simulator")
		w.Header().Set("X-Block-Simulation", "true")
		if r.URL.Path == "/internal/v1/source" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schemaVersion": "block-local-private/v1",
				"source":        map[string]any{"kind": "simulator", "simulation": true},
			})
			return
		}
		if r.URL.Path == "/api/v1/commands" {
			accepted <- r.Header.Get("Idempotency-Key")
			<-release
			_ = json.NewEncoder(w).Encode(map[string]any{
				"state": HMIState{Revision: 2, UpdatedAt: time.Now()},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	socket, stop := startUnixTestServer(t, handler)
	defer stop()
	defer close(release)
	controller, err := newAgentController(socket, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if _, err := controller.SourceInfo(context.Background()); err != nil {
		t.Fatalf("source handshake: %v", err)
	}

	response := performJSON(t, newAPIHandler(controller), http.MethodPost, "/api/v1/commands", map[string]any{
		"command": "start", "expectedRevision": 1,
	}, map[string]string{
		"X-Operator":      "QA",
		"Idempotency-Key": "accepted-timeout-1",
	})
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504: %s", response.StatusCode, readBody(t, response))
	}
	var payload errorEnvelope
	decodeResponse(t, response, &payload)
	if payload.Error.Code != "command_outcome_unknown" {
		t.Fatalf("error code = %q, want command_outcome_unknown", payload.Error.Code)
	}
	select {
	case commandID := <-accepted:
		if commandID != "accepted-timeout-1" {
			t.Fatalf("accepted Idempotency-Key = %q", commandID)
		}
	default:
		t.Fatal("Agent did not accept the mutation before HMI timeout")
	}
}

func TestAgentControllerDisconnectAndUnavailableAreErrors(t *testing.T) {
	directory, err := os.MkdirTemp("", "hmi-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	missing := filepath.Join(directory, "missing.sock")
	controller, err := newAgentController(missing, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.State(context.Background()); err == nil {
		t.Fatal("missing Agent socket was treated as online")
	}
	controller.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"backend_unavailable","message":"stale"}}`))
	})
	socket, stop := startUnixTestServer(t, handler)
	defer stop()
	controller, err = newAgentController(socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if _, err := controller.State(context.Background()); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("503 error = %v", err)
	}
}

func TestSimulationBadgeIsServerRenderedAndStaleCopyIsPresent(t *testing.T) {
	controller, err := newMemoryController("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHandlerWithOptions(controller, defaultBasePath, true)
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(handler, http.MethodGet, "/", nil, nil)
	body := readBody(t, response)
	for _, required := range []string{
		`data-simulation-mode="true"`,
		`>模拟数据</span>`,
		`数据已过期 ${ageSeconds} 秒 / 设备连接中断`,
		`error.code === "data_stale"`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("rendered HMI does not contain %q", required)
		}
	}
}

func startUnixTestServer(t *testing.T, handler http.Handler) (string, func()) {
	t.Helper()
	directory, err := os.MkdirTemp("", "hmi-uds-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(done)
	}()
	return socket, func() {
		_ = server.Close()
		_ = listener.Close()
		<-done
		_ = os.RemoveAll(directory)
	}
}
