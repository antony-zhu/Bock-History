package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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

	endpointPath := filepath.Join(t.TempDir(), "plc-endpoint.json")
	_, address, stop, done := startRuntimeWithOptions(t, RuntimeOptions{PLCEndpointPath: endpointPath})
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
	stored, found, err := loadPLCEndpoint(endpointPath)
	if err != nil || !found || stored.DeviceID() != deviceID {
		t.Fatalf("saved simulator endpoint = %#v, found=%t, error=%v", stored, found, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	stopRuntime(t, stop, done)

	_, address, stop, done = startRuntimeWithOptions(t, RuntimeOptions{PLCEndpointPath: endpointPath})
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
	if _, found, err := loadPLCEndpoint(endpointPath); err != nil || found {
		t.Fatalf("simulator disconnect did not clear endpoint: found=%t error=%v", found, err)
	}
}
