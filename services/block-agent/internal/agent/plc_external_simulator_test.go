package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"block.local/block-agent/internal/easy521"
)

func TestExternalEasy521SimulatorPLCAddressLifecycle(t *testing.T) {
	if os.Getenv("BLOCK_EASY521_SIMULATOR_E2E") != "1" {
		t.Skip("set BLOCK_EASY521_SIMULATOR_E2E=1 to verify the local Easy521 simulator")
	}
	simulatorEndpoint := os.Getenv("BLOCK_EASY521_SIMULATOR_ENDPOINT")
	if simulatorEndpoint == "" {
		t.Skip("set BLOCK_EASY521_SIMULATOR_ENDPOINT to the PC simulator IPv4 host:port")
	}
	endpoint, err := parsePLCDeviceID("easy521://" + simulatorEndpoint + "?unitId=1")
	if err != nil {
		t.Fatal("BLOCK_EASY521_SIMULATOR_ENDPOINT must be an IPv4 host:port for the PC simulator")
	}
	deviceID := endpoint.DeviceID()
	addressRange := endpoint.host + "/32"
	probeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	devices, err := scanRange(probeContext, addressRange, endpoint.port, endpoint.unitID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].DeviceID != deviceID {
		t.Fatalf("external simulator scan = %#v", devices)
	}

	store := openPLCEndpointStore(t)
	options := RuntimeOptions{PLCEndpointStore: store}
	_, address, stop, done := startRuntimeWithOptions(t, options)
	connection := dial(t, address)
	configure(t, connection)
	if configured := receive(t, connection); configured["type"] != "runtime.configured" {
		t.Fatalf("runtime configured event = %#v", configured)
	}
	send(t, connection, map[string]any{"type": "plc.connect", "requestId": "simulator-connect", "deviceId": deviceID})
	if event := receiveType(t, connection, "plc.connection.changed"); event["state"] != "connecting" {
		t.Fatalf("simulator connecting event = %#v", event)
	}
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != true {
		t.Fatalf("simulator connect result = %#v", result)
	}
	stored, found, err := loadPLCEndpoint(store)
	if err != nil || !found || stored.DeviceID() != deviceID {
		t.Fatalf("saved simulator endpoint = %#v, found=%t, error=%v", stored, found, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	stopRuntime(t, stop, done)

	_, address, stop, done = startRuntimeWithOptions(t, options)
	defer stopRuntime(t, stop, done)
	connection = dial(t, address)
	defer connection.Close()
	configure(t, connection)
	if configured := receive(t, connection); configured["type"] != "runtime.configured" {
		t.Fatalf("restart configured event = %#v", configured)
	}
	if event := receiveType(t, connection, "plc.connection.changed"); event["state"] != "connecting" || event["deviceId"] != deviceID {
		t.Fatalf("saved simulator endpoint did not auto-connect = %#v", event)
	}
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != true {
		t.Fatalf("saved simulator reconnect result = %#v", result)
	}
	send(t, connection, map[string]any{"type": "points.snapshot.get", "requestId": "simulator-refresh"})
	if snapshot := receiveType(t, connection, "points.snapshot"); snapshot["values"] == nil {
		t.Fatalf("simulator snapshot = %#v", snapshot)
	}
	send(t, connection, map[string]any{"type": "plc.disconnect", "requestId": "simulator-disconnect"})
	if result := receiveType(t, connection, "plc.disconnect.result"); result["success"] != true {
		t.Fatalf("simulator disconnect result = %#v", result)
	}
	if stored, found, err := loadPLCEndpoint(store); err != nil || !found || stored.DeviceID() != deviceID {
		t.Fatalf("simulator disconnect did not retain endpoint: stored=%#v found=%t error=%v", stored, found, err)
	}
}

func TestExternalEasy521SimulatorAlarmWordTransitions(t *testing.T) {
	if os.Getenv("BLOCK_EASY521_SIMULATOR_E2E") != "1" {
		t.Skip("set BLOCK_EASY521_SIMULATOR_E2E=1 to verify the local Easy521 simulator")
	}
	simulatorEndpoint := os.Getenv("BLOCK_EASY521_SIMULATOR_ENDPOINT")
	if simulatorEndpoint == "" {
		t.Skip("set BLOCK_EASY521_SIMULATOR_ENDPOINT to the PC simulator IPv4 host:port")
	}
	endpoint, err := parsePLCDeviceID("easy521://" + simulatorEndpoint + "?unitId=1")
	if err != nil {
		t.Fatal("BLOCK_EASY521_SIMULATOR_ENDPOINT must be an IPv4 host:port for the PC simulator")
	}
	writer, err := easy521.New(easy521.Config{
		Endpoint:       endpoint.String(),
		UnitID:         endpoint.unitID,
		ConnectTimeout: 2 * time.Second,
		RequestTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writeD500 := func(value uint16) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := writer.WriteSingleRegister(ctx, 500, value); err != nil {
			t.Fatal(err)
		}
	}
	writeD500(0)

	_, address, stop, done := startRuntime(t)
	defer stopRuntime(t, stop, done)
	connection := dial(t, address)
	defer connection.Close()
	send(t, connection, map[string]any{
		"type": "runtime.configure", "scanIntervalMs": 500,
		"points": []any{
			alarmPoint("alarm.light.curtain.triggered", "D500.2", "光幕触发"),
			alarmPoint("alarm.material.conflict", "D500.9", "有料冲突"),
			alarmPoint("alarm.self.check.failed", "D500.10", "自检未通过"),
		},
	})
	if configured := receive(t, connection); configured["type"] != "runtime.configured" || configured["scanIntervalMs"] != float64(500) {
		t.Fatalf("runtime configured event = %#v", configured)
	}
	send(t, connection, map[string]any{"type": "plc.connect", "requestId": "alarm-word-connect", "deviceId": endpoint.DeviceID()})
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != true {
		t.Fatalf("connect result = %#v", result)
	}
	initial := receiveType(t, connection, "points.snapshot")
	visible := alarmValues(t, initial)
	assertActiveAlarmCount(t, visible, 0)

	assertTransition := func(word uint16, expectedChanged []string, expectedVisible int) {
		t.Helper()
		started := time.Now()
		writeD500(word)
		changed := receiveType(t, connection, "points.changed")
		elapsed := time.Since(started)
		if elapsed > time.Second {
			t.Fatalf("D500=0x%04X was not published within one complete 500 ms poll: %s", word, elapsed)
		}
		values := alarmValues(t, changed)
		if len(values) != len(expectedChanged) {
			t.Fatalf("D500=0x%04X changed values = %#v, want %d", word, values, len(expectedChanged))
		}
		for _, pointID := range expectedChanged {
			if _, exists := values[pointID]; !exists {
				t.Fatalf("D500=0x%04X did not publish %s: %#v", word, pointID, values)
			}
		}
		for pointID, value := range values {
			visible[pointID] = value
		}
		assertActiveAlarmCount(t, visible, expectedVisible)
		t.Logf("D500=0x%04X published in %s; visible alarms=%d", word, elapsed, expectedVisible)
	}

	assertTransition(0x0004, []string{"alarm.light.curtain.triggered"}, 1)
	assertTransition(0x0604, []string{"alarm.material.conflict", "alarm.self.check.failed"}, 3)
	assertTransition(0, []string{"alarm.light.curtain.triggered", "alarm.material.conflict", "alarm.self.check.failed"}, 0)
}

func alarmPoint(pointID, address, message string) map[string]any {
	return map[string]any{
		"pointId": pointID, "address": address, "type": "bool", "access": "read",
		"readPoint": pointID, "writePoint": nil, "writeMethod": nil,
		"alarm": map[string]any{"normalValue": false, "alarmValue": true, "message": message, "level": "danger"},
	}
}

func alarmValues(t *testing.T, message map[string]any) map[string]bool {
	t.Helper()
	rawValues, ok := message["values"].(map[string]any)
	if !ok {
		t.Fatalf("alarm message has no values: %#v", message)
	}
	values := make(map[string]bool, len(rawValues))
	for pointID, rawValue := range rawValues {
		value, ok := rawValue.(map[string]any)
		if !ok || value["quality"] != "good" {
			t.Fatalf("alarm value %s = %#v", pointID, rawValue)
		}
		active, ok := value["value"].(bool)
		if !ok {
			t.Fatalf("alarm value %s is not a bool: %#v", pointID, rawValue)
		}
		values[pointID] = active
	}
	return values
}

func assertActiveAlarmCount(t *testing.T, values map[string]bool, want int) {
	t.Helper()
	active := 0
	for _, value := range values {
		if value {
			active++
		}
	}
	if active != want {
		t.Fatalf("active alarms = %d, want %d; values=%#v", active, want, values)
	}
}
