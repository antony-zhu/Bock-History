package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestRunExistingAdminExecutesE2EFlow(t *testing.T) {
	var mu sync.Mutex
	var initialAdmin map[string]string
	var login map[string]string
	var messages []map[string]json.RawMessage
	wsDone := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/initial-admin", func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		if err := json.NewDecoder(request.Body).Decode(&initialAdmin); err != nil {
			t.Error(err)
		}
		writer.WriteHeader(http.StatusConflict)
	})
	mux.HandleFunc("/api/auth/login", func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		if err := json.NewDecoder(request.Body).Decode(&login); err != nil {
			t.Error(err)
		}
		writer.WriteHeader(http.StatusOK)
	})
	mux.Handle("/ws", websocket.Server{Handler: websocket.Handler(func(connection *websocket.Conn) {
		defer close(wsDone)
		defer connection.Close()
		for {
			var message map[string]json.RawMessage
			if err := websocket.JSON.Receive(connection, &message); err != nil {
				return
			}
			mu.Lock()
			messages = append(messages, message)
			mu.Unlock()
			messageType := stringField(t, message, "type")
			switch messageType {
			case "runtime.configure":
				sendMessage(t, connection, map[string]any{"protocolVersion": "1.0", "type": "runtime.configured", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "scanIntervalMs": 50})
			case "plc.scan":
				sendMessage(t, connection, map[string]any{"protocolVersion": "1.0", "type": "plc.scan.result", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "success": true, "devices": []map[string]any{{"deviceId": "easy521://127.0.0.1:502?unitId=1", "name": "test", "address": "127.0.0.1", "state": "disconnected", "selected": false, "metadata": map[string]any{}}}})
			case "plc.connect":
				sendMessage(t, connection, map[string]any{"protocolVersion": "1.0", "type": "plc.connection.changed", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "state": "connected"})
				sendMessage(t, connection, map[string]any{"protocolVersion": "1.0", "type": "plc.connect.result", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "success": true, "deviceId": "easy521://127.0.0.1:502?unitId=1", "state": "connected"})
				sendSnapshot(t, connection)
			case "point.command":
				sendMessage(t, connection, map[string]any{"protocolVersion": "1.0", "type": "point.result", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "success": true, "pointId": stringField(t, message, "pointId"), "actualValue": false})
			case "points.snapshot.get":
				sendSnapshot(t, connection)
			case "plc.disconnect":
				sendMessage(t, connection, map[string]any{"protocolVersion": "1.0", "type": "plc.connection.changed", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "state": "disconnected"})
				sendMessage(t, connection, map[string]any{"protocolVersion": "1.0", "type": "plc.disconnect.result", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "success": true, "state": "disconnected"})
			default:
				t.Errorf("unexpected message type %q", messageType)
			}
		}
	})})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	caPath := writeServerCA(t, server)

	pointsPath := writePoints(t, `{
  "scanIntervalMs": 50,
  "points": [
    {"pointId":"pulse.point","writePoint":"pulse.point","write":{"mode":"pulse"}},
    {"pointId":"momentary.point","writePoint":"momentary.point","write":{"mode":"momentary"}},
    {"pointId":"toggle.point","writePoint":"toggle.point","write":{"mode":"toggle"}}
  ],
  "bindings": [{"displayPath":"ui.only"}],
  "layout": [{"displayPath":"ui.layout"}]
}`)
	var output bytes.Buffer
	err := run(context.Background(), options{
		baseURL: server.URL, caPath: caPath, pointsPath: pointsPath, scanCIDR: "127.0.0.1/32",
		observeScanDuration: 5 * time.Millisecond,
		username:            "admin", password: "do-not-print-this-password", output: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-wsDone:
	case <-time.After(time.Second):
		t.Fatal("WebSocket handler did not finish")
	}
	if initialAdmin["username"] != "admin" || initialAdmin["password"] != "do-not-print-this-password" || initialAdmin["confirmPassword"] != "do-not-print-this-password" {
		t.Fatalf("initial admin request was wrong: %#v", initialAdmin)
	}
	if login["username"] != "admin" || login["password"] != "do-not-print-this-password" || len(login) != 2 {
		t.Fatalf("login request was wrong: %#v", login)
	}
	if strings.Contains(output.String(), "do-not-print-this-password") {
		t.Fatal("JSONL output exposed the password")
	}
	results := assertJSONL(t, output.Bytes())
	assertStageOrder(t, results, "points.snapshot.initial", "plc.observe", "point.command")

	mu.Lock()
	defer mu.Unlock()
	if len(messages) != 10 {
		t.Fatalf("received %d WebSocket messages, want 10", len(messages))
	}
	assertEnvelope(t, messages[0])
	if stringField(t, messages[0], "type") != "runtime.configure" {
		t.Fatalf("first WebSocket message was %q", stringField(t, messages[0], "type"))
	}
	if _, exists := messages[0]["bindings"]; exists {
		t.Fatal("runtime.configure leaked bindings")
	}
	if _, exists := messages[0]["layout"]; exists {
		t.Fatal("runtime.configure leaked layout")
	}
	var configuration struct {
		Points []json.RawMessage `json:"points"`
	}
	if err := decodeMessage(messages[0], &configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.Points) != 3 {
		t.Fatalf("runtime.configure sent %d points, want 3", len(configuration.Points))
	}
	for _, message := range messages {
		assertEnvelope(t, message)
	}
	var actions []string
	for _, message := range messages {
		if stringField(t, message, "type") != "point.command" {
			continue
		}
		actions = append(actions, stringField(t, message, "action"))
	}
	if got, want := strings.Join(actions, ","), "pulse,press,release,toggle,toggle"; got != want {
		t.Fatalf("point actions = %q, want %q", got, want)
	}
}

func TestAuthenticateCreatesInitialAdmin(t *testing.T) {
	var loginCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/initial-admin", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/api/auth/login", func(http.ResponseWriter, *http.Request) { loginCalled = true })
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	base, err := parseBaseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := loadTLSConfig(writeServerCA(t, server), base.Hostname())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	workflow := &workflow{base: base, client: &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig}}, tls: tlsConfig, output: json.NewEncoder(&output)}
	if err := workflow.authenticate(context.Background(), "admin", "secret"); err != nil {
		t.Fatal(err)
	}
	if loginCalled {
		t.Fatal("login was called after initial-admin succeeded")
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatal("JSONL output exposed the password")
	}
	assertJSONL(t, output.Bytes())
}

func TestParseBaseURLRequiresHTTPS(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "ws://127.0.0.1:8444", "https:///missing-host"} {
		if _, err := parseBaseURL(raw); err == nil {
			t.Fatalf("parseBaseURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestLoadTLSConfigRejectsMissingCA(t *testing.T) {
	if _, err := loadTLSConfig(filepath.Join(t.TempDir(), "missing-ca.crt"), "127.0.0.1"); err == nil {
		t.Fatal("loadTLSConfig accepted a missing CA")
	}
}

func TestFailureResultPreservesServerContextWithoutPassword(t *testing.T) {
	secret := "do-not-print-this-password"
	failure := failureFrom(map[string]json.RawMessage{
		"type":      json.RawMessage(`"error"`),
		"requestId": json.RawMessage(`"runtime-configure-one"`),
		"error":     json.RawMessage(`{"code":"INVALID_REQUEST","message":"points[1].writePoint is required","details":{}}`),
	})
	line := failureResult(atStage("runtime.configure", failure))
	if line.Stage != "runtime.configure" || line.ErrorCode != "INVALID_REQUEST" || line.Message != "points[1].writePoint is required" || line.RequestID != "runtime-configure-one" || line.MessageType != "error" {
		t.Fatalf("failure result = %#v", line)
	}
	contents, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), secret) {
		t.Fatal("failure JSONL exposed the password")
	}
}

func sendSnapshot(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	sendMessage(t, connection, map[string]any{"protocolVersion": "1.0", "type": "points.snapshot", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "values": map[string]any{"pulse.point": map[string]any{"value": false, "quality": "good", "updatedAt": time.Now().UTC().Format(time.RFC3339Nano), "alarmActive": nil}}})
}

func sendMessage(t *testing.T, connection *websocket.Conn, value any) {
	t.Helper()
	if err := websocket.JSON.Send(connection, value); err != nil {
		t.Fatal(err)
	}
}

func writePoints(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "points.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeServerCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("TLS server has no certificate")
	}
	contents := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if len(contents) == 0 {
		t.Fatal("could not encode TLS server certificate")
	}
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func stringField(t *testing.T, message map[string]json.RawMessage, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(message[key], &value); err != nil {
		t.Fatalf("message %q: %v", key, err)
	}
	return value
}

func assertEnvelope(t *testing.T, message map[string]json.RawMessage) {
	t.Helper()
	if stringField(t, message, "protocolVersion") != "1.0" {
		t.Fatal("wrong protocol version")
	}
	if stringField(t, message, "type") == "" || stringField(t, message, "requestId") == "" {
		t.Fatal("request envelope is incomplete")
	}
	var timestamp string
	if err := json.Unmarshal(message["timestamp"], &timestamp); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
		t.Fatal(err)
	}
}

func assertJSONL(t *testing.T, contents []byte) []resultLine {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("expected JSONL output")
	}
	results := make([]resultLine, 0, len(lines))
	for _, line := range lines {
		var value resultLine
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", line, err)
		}
		if value.Stage == "" || value.Status == "" {
			t.Fatalf("incomplete JSONL result: %#v", value)
		}
		results = append(results, value)
	}
	return results
}

func assertStageOrder(t *testing.T, results []resultLine, stages ...string) {
	t.Helper()
	index := 0
	for _, result := range results {
		if index < len(stages) && result.Stage == stages[index] {
			index++
		}
	}
	if index != len(stages) {
		t.Fatalf("stage order %v was not found in %#v", stages, results)
	}
}
