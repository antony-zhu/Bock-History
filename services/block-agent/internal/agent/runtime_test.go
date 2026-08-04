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

	"block.local/block-agent/internal/alarmhistory"
	"block.local/block-agent/internal/plcworker"
	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
	"block.local/block-agent/internal/storage"
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

func TestConfigureThenSnapshotGet(t *testing.T) {
	runtime, address, cancel, done := startRuntime(t)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()

	configure(t, connection)
	configured := receive(t, connection)
	if configured["type"] != "runtime.configured" || configured["scanIntervalMs"] != float64(50) {
		t.Fatalf("configured event = %#v", configured)
	}
	send(t, connection, map[string]any{"type": "points.snapshot.get"})
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
	send(t, connection, map[string]any{"type": "plc.connect", "requestId": "connect-one", "deviceId": "easy521://127.0.0.1:1502?unitId=1"})
	if event := receiveType(t, connection, "plc.connection.changed"); event["state"] != "connecting" {
		t.Fatalf("connecting event = %#v", event)
	}
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != true {
		t.Fatalf("connect result = %#v", result)
	}
	if snapshot := receiveType(t, connection, "points.snapshot"); snapshot["type"] != "points.snapshot" {
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

func TestPLCDisconnectStopsWorkerAndClearsValues(t *testing.T) {
	adapter := &runtimeFakeAdapter{registers: map[uint16]uint16{504: 0}}
	factory := plcworker.Factory(func(config runtimeconfig.Config, publish func(map[string]pointstore.PointValue) error) (*plcworker.Worker, error) {
		return plcworker.New(config, adapter, publish, time.Now)
	})
	runtime, address, cancel, done := startRuntimeWithFactory(t, factory)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()
	configure(t, connection)
	_ = receive(t, connection)
	send(t, connection, map[string]any{"type": "plc.connect", "requestId": "connect", "deviceId": "easy521://127.0.0.1:1502?unitId=1"})
	_ = receiveType(t, connection, "plc.connection.changed")
	_ = receiveType(t, connection, "plc.connect.result")
	_ = receiveType(t, connection, "points.snapshot")
	if len(runtime.Store().Snapshot()) == 0 {
		t.Fatal("PLC initial read did not populate the point store")
	}
	send(t, connection, map[string]any{"type": "plc.disconnect", "requestId": "disconnect"})
	if event := receiveType(t, connection, "plc.connection.changed"); event["state"] != "disconnected" {
		t.Fatalf("disconnect event = %#v", event)
	}
	if result := receiveType(t, connection, "plc.disconnect.result"); result["success"] != true || result["state"] != "disconnected" {
		t.Fatalf("disconnect result = %#v", result)
	}
	if !runtime.Store().Configured() || len(runtime.Store().Snapshot()) != 0 {
		t.Fatalf("disconnect did not preserve config and clear values: configured=%t values=%#v", runtime.Store().Configured(), runtime.Store().Snapshot())
	}
}

func TestPLCScanReturnsOneCompleteResult(t *testing.T) {
	_, address, cancel, done := startRuntime(t)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()
	configure(t, connection)
	_ = receive(t, connection)
	send(t, connection, map[string]any{"type": "plc.scan", "requestId": "scan", "addressRange": "127.0.0.1/32"})
	result := receiveType(t, connection, "plc.scan.result")
	if result["success"] != true {
		t.Fatalf("scan result = %#v", result)
	}
	if _, ok := result["devices"].([]any); !ok {
		t.Fatalf("scan devices = %#v", result["devices"])
	}
}

func TestDisconnectStopsAndClearsSession(t *testing.T) {
	runtime, address, cancel, done := startRuntime(t)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	configure(t, connection)
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

func TestAlarmHistoryPersistsLocallyWithoutMQTTS(t *testing.T) {
	directory := t.TempDir()
	store, err := storage.Open(filepath.Join(directory, "block.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime, address, cancel, done := startRuntimeWithOptions(t, RuntimeOptions{AlarmStore: store})
	defer stopRuntime(t, cancel, done)
	if runtime.mqtt.Enabled {
		t.Fatal("MQTTS v2 must default to disabled")
	}
	connection := dial(t, address)
	defer connection.Close()
	send(t, connection, map[string]any{
		"protocolVersion": "1.0", "type": "runtime.configure", "scanIntervalMs": 50,
		"points": []any{map[string]any{
			"pointId": "machine.fault", "address": "D600.0", "type": "bool", "access": "read", "readPoint": "machine.fault",
			"alarm": map[string]any{"normalValue": false, "alarmValue": true, "message": "Machine fault"},
		}},
	})
	if configured := receive(t, connection); configured["type"] != "runtime.configured" {
		t.Fatalf("configured event = %#v", configured)
	}
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	active := true
	if err := runtime.UpdateConfirmed(map[string]pointstore.PointValue{
		"machine.fault": {Value: true, Quality: "good", UpdatedAt: now, AlarmActive: &active},
	}); err != nil {
		t.Fatal(err)
	}
	_ = receive(t, connection)
	inactive := false
	if err := runtime.UpdateConfirmed(map[string]pointstore.PointValue{
		"machine.fault": {Value: false, Quality: "good", UpdatedAt: now.Add(time.Second), AlarmActive: &inactive},
	}); err != nil {
		t.Fatal(err)
	}
	_ = receive(t, connection)
	records, hasMore, err := store.List(context.Background(), alarmhistory.Query{
		FromOccurredAt: now.Add(-time.Second), ToOccurredAt: now.Add(time.Minute), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(records) != 2 || records[0].EventKind != "RAISED" || records[1].EventKind != "CLEARED" {
		t.Fatalf("local alarm history = %#v, hasMore=%t", records, hasMore)
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
	return startRuntimeWithOptionsAndFactory(t, factory, RuntimeOptions{})
}

func startRuntimeWithOptions(t *testing.T, options RuntimeOptions) (*Runtime, string, context.CancelFunc, <-chan error) {
	t.Helper()
	return startRuntimeWithOptionsAndFactory(t, nil, options)
}

func startRuntimeWithOptionsAndFactory(t *testing.T, factory plcworker.Factory, options RuntimeOptions) (*Runtime, string, context.CancelFunc, <-chan error) {
	t.Helper()
	runtime, err := NewLocalRuntimeWithOptions("127.0.0.1:0", time.Now, factory, nil, nil, options)
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
	if message, ok := value.(map[string]any); ok {
		if _, exists := message["protocolVersion"]; !exists {
			message["protocolVersion"] = protocolVersion
		}
		if _, exists := message["timestamp"]; !exists {
			message["timestamp"] = time.Now().UTC().Format(time.RFC3339)
		}
	}
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
