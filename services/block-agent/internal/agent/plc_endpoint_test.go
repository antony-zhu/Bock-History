package agent

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"block.local/block-agent/internal/plcworker"
	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
)

func TestPLCEndpointStorageRoundTripAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "plc-endpoint.json")
	endpoint, err := parsePLCDeviceID("easy521://127.0.0.1:1502?unitId=1")
	if err != nil {
		t.Fatal(err)
	}
	if err := savePLCEndpoint(path, endpoint); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := loadPLCEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found || loaded.DeviceID() != endpoint.DeviceID() {
		t.Fatalf("loaded endpoint = %#v, found=%t", loaded, found)
	}
	if err := clearPLCEndpoint(path); err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadPLCEndpoint(path); err != nil || found {
		t.Fatalf("cleared endpoint found=%t error=%v", found, err)
	}
}

func TestRuntimePLCEndpointLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plc-endpoint.json")
	endpoint := "easy521://127.0.0.1:1502?unitId=1"
	var factoryCalls int
	var factoryMu sync.Mutex
	factory := plcworker.Factory(func(config runtimeconfig.Config, publish func(map[string]pointstore.PointValue) error) (*plcworker.Worker, error) {
		factoryMu.Lock()
		factoryCalls++
		factoryMu.Unlock()
		adapter := &runtimeFakeAdapter{registers: map[uint16]uint16{504: 0}}
		return plcworker.New(config, adapter, publish, time.Now)
	})

	_, address, cancel, done := startRuntimeWithOptionsAndFactory(t, factory, RuntimeOptions{PLCEndpointPath: path})
	connection := dial(t, address)
	configure(t, connection)
	if configured := receive(t, connection); configured["type"] != "runtime.configured" {
		t.Fatalf("configured event = %#v", configured)
	}
	factoryMu.Lock()
	initialFactoryCalls := factoryCalls
	factoryMu.Unlock()
	if initialFactoryCalls != 0 {
		t.Fatalf("runtime without saved endpoint started %d PLC workers", initialFactoryCalls)
	}

	send(t, connection, map[string]any{"type": "plc.connect", "requestId": "manual-connect", "deviceId": endpoint})
	if event := receiveType(t, connection, "plc.connection.changed"); event["state"] != "connecting" {
		t.Fatalf("manual connecting event = %#v", event)
	}
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != true {
		t.Fatalf("manual connect result = %#v", result)
	}
	if _, found, err := loadPLCEndpoint(path); err != nil || !found {
		t.Fatalf("manual connection was not persisted: found=%t error=%v", found, err)
	}

	send(t, connection, map[string]any{"type": "plc.disconnect", "requestId": "manual-disconnect"})
	if result := receiveType(t, connection, "plc.disconnect.result"); result["success"] != true {
		t.Fatalf("manual disconnect result = %#v", result)
	}
	if _, found, err := loadPLCEndpoint(path); err != nil || found {
		t.Fatalf("manual disconnect did not clear endpoint: found=%t error=%v", found, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	stopRuntime(t, cancel, done)

	parsed, err := parsePLCDeviceID(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePLCEndpoint(path, parsed); err != nil {
		t.Fatal(err)
	}
	_, address, cancel, done = startRuntimeWithOptionsAndFactory(t, factory, RuntimeOptions{PLCEndpointPath: path})
	defer stopRuntime(t, cancel, done)
	connection = dial(t, address)
	defer connection.Close()
	configure(t, connection)
	if configured := receive(t, connection); configured["type"] != "runtime.configured" {
		t.Fatalf("restart configured event = %#v", configured)
	}
	if event := receiveType(t, connection, "plc.connection.changed"); event["deviceId"] != endpoint || event["state"] != "connecting" {
		t.Fatalf("restart did not auto-connect saved endpoint: %#v", event)
	}
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != true {
		t.Fatalf("restart connect result = %#v", result)
	}
	factoryMu.Lock()
	finalFactoryCalls := factoryCalls
	factoryMu.Unlock()
	if finalFactoryCalls != 2 {
		t.Fatalf("PLC worker calls=%d, want 2", finalFactoryCalls)
	}
}

func TestFailedPLCConnectionDoesNotPersistEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plc-endpoint.json")
	factory := plcworker.Factory(func(runtimeconfig.Config, func(map[string]pointstore.PointValue) error) (*plcworker.Worker, error) {
		return nil, errors.New("PLC unavailable")
	})
	_, address, cancel, done := startRuntimeWithOptionsAndFactory(t, factory, RuntimeOptions{PLCEndpointPath: path})
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()
	configure(t, connection)
	_ = receive(t, connection)
	send(t, connection, map[string]any{
		"type": "plc.connect", "requestId": "failed-connect", "deviceId": "easy521://127.0.0.1:1502?unitId=1",
	})
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != false {
		t.Fatalf("failed connect result = %#v", result)
	}
	if _, found, err := loadPLCEndpoint(path); err != nil || found {
		t.Fatalf("failed connection persisted endpoint: found=%t error=%v", found, err)
	}
}
