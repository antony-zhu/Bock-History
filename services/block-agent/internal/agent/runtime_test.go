package agent

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"block.local/block-agent/internal/plcworker"
	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
	"golang.org/x/net/websocket"
)

func TestWebSocketRequiresRuntimeConfigureFirst(t *testing.T) {
	runtime, address, cancel, done := startRuntime(t)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()

	send(t, connection, map[string]any{"type": "point.command", "pointId": "x", "action": "pulse"})
	message := receive(t, connection)
	if message["type"] != "error" || errorCode(t, message) != "INVALID_REQUEST" {
		t.Fatalf("first-message response = %#v", message)
	}
	if runtime.Active() || runtime.Store().Configured() {
		t.Fatal("non-configure first message started a session")
	}
}

func TestConfigureSendsConfiguredThenSnapshot(t *testing.T) {
	runtime, address, cancel, done := startRuntime(t)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()

	configure(t, connection)
	configured := receive(t, connection)
	if configured["type"] != "runtime.configured" || configured["pointCount"] != float64(2) {
		t.Fatalf("configured event = %#v", configured)
	}
	snapshot := receive(t, connection)
	if snapshot["type"] != "points.snapshot" {
		t.Fatalf("snapshot event = %#v", snapshot)
	}
	if values, ok := snapshot["values"].(map[string]any); !ok || len(values) != 0 {
		t.Fatalf("initial snapshot values = %#v", snapshot["values"])
	}
	if !runtime.Active() {
		t.Fatal("configured connection did not own the session")
	}
	send(t, connection, map[string]any{
		"type": "runtime.configure", "scanIntervalMs": 50, "points": []any{},
	})
	duplicate := receive(t, connection)
	if duplicate["type"] != "error" || errorCode(t, duplicate) != "INVALID_REQUEST" {
		t.Fatalf("duplicate configure response = %#v", duplicate)
	}
	send(t, connection, map[string]any{"type": "future.message", "newField": true})
	unknown := receive(t, connection)
	if unknown["type"] != "error" || errorCode(t, unknown) != "UNKNOWN_MESSAGE" {
		t.Fatalf("unknown message response = %#v", unknown)
	}
}

func TestChangedEventContainsAbsoluteValue(t *testing.T) {
	runtime, address, cancel, done := startRuntime(t)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()
	configure(t, connection)
	_ = receive(t, connection)
	_ = receive(t, connection)

	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	if err := runtime.UpdateConfirmed(map[string]pointstore.PointValue{
		"machine.startFeedback": {Value: true, Quality: "good", UpdatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	changed := receive(t, connection)
	if changed["type"] != "points.changed" {
		t.Fatalf("changed event = %#v", changed)
	}
	values := changed["values"].(map[string]any)
	value := values["machine.startFeedback"].(map[string]any)
	if value["value"] != true || value["quality"] != "good" {
		t.Fatalf("changed value = %#v", value)
	}
}

func TestPointCommandIsValidatedButDoesNotWriteWithoutPLC(t *testing.T) {
	_, address, cancel, done := startRuntime(t)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()
	configure(t, connection)
	_ = receive(t, connection)
	_ = receive(t, connection)

	send(t, connection, map[string]any{
		"type": "point.command", "requestId": "one", "pointId": "machine.startCommand", "action": "pulse",
	})
	result := receive(t, connection)
	if result["type"] != "point.result" || result["success"] != false || errorCode(t, result) != "PLC_NOT_CONNECTED" {
		t.Fatalf("point result = %#v", result)
	}
}

func TestPointCommandUsesWorkerReadbackAndFC22Sequence(t *testing.T) {
	adapter := &runtimeFakeAdapter{registers: map[uint16]uint16{504: 0}}
	factory := plcworker.Factory(func(config runtimeconfig.Config, publish func(map[string]pointstore.PointValue) error) (*plcworker.Worker, error) {
		return plcworker.New(config, adapter, publish, time.Now)
	})
	_, address, cancel, done := startRuntimeWithFactory(t, factory)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()

	configure(t, connection)
	if configured := receive(t, connection); configured["type"] != "runtime.configured" {
		t.Fatalf("configured event = %#v", configured)
	}
	if snapshot := receive(t, connection); snapshot["type"] != "points.snapshot" {
		t.Fatalf("snapshot event = %#v", snapshot)
	}

	send(t, connection, map[string]any{
		"type": "point.command", "requestId": "pulse-one", "pointId": "machine.startCommand", "action": "pulse",
	})
	result := receiveType(t, connection, "point.result")
	if result["success"] != true || result["pointId"] != "machine.startCommand" || result["actualValue"] != false {
		t.Fatalf("worker point result = %#v", result)
	}
	writes := adapter.writeCalls()
	if len(writes) != 2 || !writes[0].value || writes[1].value || writes[0].address != 504 || writes[0].bit != 1 {
		t.Fatalf("worker did not use FC22 bit sequence: %#v", writes)
	}
}

func TestDisconnectStopsAndClearsSession(t *testing.T) {
	runtime, address, cancel, done := startRuntime(t)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	configure(t, connection)
	_ = receive(t, connection)
	_ = receive(t, connection)
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return !runtime.Active() && !runtime.Store().Configured() })
}

func TestRuntimeDoesNotCreateSQLitePointFiles(t *testing.T) {
	directory := t.TempDir()
	if _, err := NewLocalRuntime("127.0.0.1:0", time.Now); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".db" {
			t.Fatalf("unexpected SQLite file %q", entry.Name())
		}
	}
}

func TestOnlyLoopbackBindingIsAccepted(t *testing.T) {
	if _, err := NewLocalRuntime("0.0.0.0:8080", time.Now); err == nil {
		t.Fatal("wildcard local HTTP address was accepted")
	}
	if _, err := NewLocalRuntime("127.0.0.1:8080", time.Now); err != nil {
		t.Fatalf("loopback address rejected: %v", err)
	}
}

func startRuntime(t *testing.T) (*Runtime, string, context.CancelFunc, <-chan error) {
	t.Helper()
	return startRuntimeWithFactory(t, nil)
}

func startRuntimeWithFactory(t *testing.T, factory plcworker.Factory) (*Runtime, string, context.CancelFunc, <-chan error) {
	t.Helper()
	runtime, err := NewLocalRuntimeWithWorkerFactory("127.0.0.1:0", time.Now, factory)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	context, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.ServeListener(context, listener) }()
	address := listener.Addr().String()
	waitFor(t, time.Second, func() bool {
		response, err := http.Get("http://" + address + "/healthz")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
	return runtime, address, cancel, done
}

func stopRuntime(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")
	}
}

func dial(t *testing.T, address string) *websocket.Conn {
	t.Helper()
	connection, err := websocket.Dial("ws://"+address+"/ws", "", "http://"+address)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func configure(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	send(t, connection, map[string]any{
		"protocolVersion": "1.0",
		"type":            "runtime.configure",
		"scanIntervalMs":  50,
		"points": []any{
			map[string]any{
				"pointId": "machine.startCommand", "address": "D504.1", "type": "bool", "access": "read_write",
				"readPoint": "machine.startFeedback", "writePoint": "machine.startCommand", "writeMethod": "maskWrite",
				"write": map[string]any{"mode": "pulse", "activeValue": true, "defaultValue": false, "pulseMs": 100},
			},
			map[string]any{
				"pointId": "machine.startFeedback", "address": "D504.2", "type": "bool", "access": "read", "readPoint": "machine.startFeedback",
			},
		},
	})
}

func send(t *testing.T, connection *websocket.Conn, value any) {
	t.Helper()
	if err := websocket.JSON.Send(connection, value); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, connection *websocket.Conn) map[string]any {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := websocket.JSON.Receive(connection, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func receiveType(t *testing.T, connection *websocket.Conn, messageType string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		message := receive(t, connection)
		if message["type"] == messageType {
			return message
		}
	}
	t.Fatalf("did not receive %q", messageType)
	return nil
}

func errorCode(t *testing.T, message map[string]any) string {
	t.Helper()
	errorValue, ok := message["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error envelope: %#v", message)
	}
	code, _ := errorValue["code"].(string)
	return code
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

type runtimeWriteCall struct {
	address uint16
	bit     uint8
	value   bool
}

type runtimeFakeAdapter struct {
	mu        sync.Mutex
	registers map[uint16]uint16
	writes    []runtimeWriteCall
}

func (f *runtimeFakeAdapter) ReadHoldingRegisters(_ context.Context, address, quantity uint16) ([]uint16, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]uint16, quantity)
	for index := range values {
		values[index] = f.registers[address+uint16(index)]
	}
	return values, nil
}

func (f *runtimeFakeAdapter) MaskWriteBit(_ context.Context, address uint16, bit uint8, value bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, runtimeWriteCall{address: address, bit: bit, value: value})
	mask := uint16(1) << bit
	if value {
		f.registers[address] |= mask
	} else {
		f.registers[address] &^= mask
	}
	return nil
}

func (f *runtimeFakeAdapter) Close() {}

func (f *runtimeFakeAdapter) writeCalls() []runtimeWriteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtimeWriteCall(nil), f.writes...)
}
