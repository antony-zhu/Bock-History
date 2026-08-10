package agent

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"block.local/block-agent/internal/plcworker"
	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
	"block.local/block-agent/internal/storage"
)

func TestPLCEndpointStorageRoundTrip(t *testing.T) {
	store := openPLCEndpointStore(t)
	endpoint, err := parsePLCDeviceID("easy521://127.0.0.1:1502?unitId=1")
	if err != nil {
		t.Fatal(err)
	}
	if err := savePLCEndpoint(store, endpoint); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := loadPLCEndpoint(store)
	if err != nil {
		t.Fatal(err)
	}
	if !found || loaded.DeviceID() != endpoint.DeviceID() {
		t.Fatalf("loaded endpoint = %#v, found=%t", loaded, found)
	}
}

func TestRuntimePLCEndpointLifecycle(t *testing.T) {
	store := openPLCEndpointStore(t)
	firstEndpoint := "easy521://127.0.0.1:1502?unitId=1"
	secondEndpoint := "easy521://127.0.0.2:1503?unitId=2"
	var factoryCalls int
	var factoryMu sync.Mutex
	factory := plcworker.Factory(func(config runtimeconfig.Config, publish func(map[string]pointstore.PointValue) error) (*plcworker.Worker, error) {
		factoryMu.Lock()
		factoryCalls++
		factoryMu.Unlock()
		adapter := &runtimeFakeAdapter{registers: map[uint16]uint16{504: 0}}
		return plcworker.New(config, adapter, publish, time.Now)
	})
	options := RuntimeOptions{PLCEndpointStore: store}

	_, address, cancel, done := startRuntimeWithOptionsAndFactory(t, factory, options)
	connection := dial(t, address)
	configure(t, connection)
	if configured := receive(t, connection); configured["type"] != "runtime.configured" {
		t.Fatalf("configured event = %#v", configured)
	}
	factoryMu.Lock()
	initialFactoryCalls := factoryCalls
	factoryMu.Unlock()
	if initialFactoryCalls != 0 {
		t.Fatalf("runtime without a saved endpoint started %d PLC workers", initialFactoryCalls)
	}

	send(t, connection, map[string]any{"type": "plc.connect", "requestId": "first-connect", "deviceId": firstEndpoint})
	if event := receiveType(t, connection, "plc.connection.changed"); event["state"] != "connecting" {
		t.Fatalf("first connecting event = %#v", event)
	}
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != true {
		t.Fatalf("first connect result = %#v", result)
	}
	if saved, found, err := loadPLCEndpoint(store); err != nil || !found || saved.DeviceID() != firstEndpoint {
		t.Fatalf("first manual connection was not persisted: saved=%#v found=%t error=%v", saved, found, err)
	}

	send(t, connection, map[string]any{"type": "plc.connect", "requestId": "second-connect", "deviceId": secondEndpoint})
	if event := receiveType(t, connection, "plc.connection.changed"); event["state"] != "connecting" || event["deviceId"] != secondEndpoint {
		t.Fatalf("second connecting event = %#v", event)
	}
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != true {
		t.Fatalf("second connect result = %#v", result)
	}
	if saved, found, err := loadPLCEndpoint(store); err != nil || !found || saved.DeviceID() != secondEndpoint {
		t.Fatalf("second manual connection did not replace the sole endpoint: saved=%#v found=%t error=%v", saved, found, err)
	}

	send(t, connection, map[string]any{"type": "plc.disconnect", "requestId": "manual-disconnect"})
	if event := receiveType(t, connection, "plc.connection.changed"); event["state"] != "disconnected" {
		t.Fatalf("manual disconnect event = %#v", event)
	}
	if result := receiveType(t, connection, "plc.disconnect.result"); result["success"] != true {
		t.Fatalf("manual disconnect result = %#v", result)
	}
	if saved, found, err := loadPLCEndpoint(store); err != nil || !found || saved.DeviceID() != secondEndpoint {
		t.Fatalf("manual disconnect removed the saved endpoint: saved=%#v found=%t error=%v", saved, found, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	stopRuntime(t, cancel, done)

	_, address, cancel, done = startRuntimeWithOptionsAndFactory(t, factory, options)
	defer stopRuntime(t, cancel, done)
	connection = dial(t, address)
	defer connection.Close()
	configure(t, connection)
	if configured := receive(t, connection); configured["type"] != "runtime.configured" {
		t.Fatalf("restart configured event = %#v", configured)
	}
	if event := receiveType(t, connection, "plc.connection.changed"); event["deviceId"] != secondEndpoint || event["state"] != "connecting" {
		t.Fatalf("restart did not auto-connect the saved endpoint: %#v", event)
	}
	if result := receiveType(t, connection, "plc.connect.result"); result["success"] != true {
		t.Fatalf("restart connect result = %#v", result)
	}
	factoryMu.Lock()
	finalFactoryCalls := factoryCalls
	factoryMu.Unlock()
	if finalFactoryCalls != 3 {
		t.Fatalf("PLC worker calls=%d, want 3", finalFactoryCalls)
	}
}

func TestRuntimeIgnoresLegacyEmptyPLCEndpointJSON(t *testing.T) {
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "plc-endpoint.json")
	if err := os.WriteFile(legacyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(directory, "block.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	var factoryCalls int
	factory := plcworker.Factory(func(config runtimeconfig.Config, publish func(map[string]pointstore.PointValue) error) (*plcworker.Worker, error) {
		factoryCalls++
		return nil, errors.New("unexpected automatic connection")
	})
	_, address, cancel, done := startRuntimeWithOptionsAndFactory(t, factory, RuntimeOptions{PLCEndpointStore: store})
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()
	configure(t, connection)
	if configured := receive(t, connection); configured["type"] != "runtime.configured" {
		t.Fatalf("empty legacy JSON blocked runtime configuration: %#v", configured)
	}
	if factoryCalls != 0 {
		t.Fatalf("legacy JSON started %d PLC workers", factoryCalls)
	}
}

func TestFailedPLCConnectionDoesNotPersistEndpointOrClaimConnected(t *testing.T) {
	store := openPLCEndpointStore(t)
	factory := plcworker.Factory(func(runtimeconfig.Config, func(map[string]pointstore.PointValue) error) (*plcworker.Worker, error) {
		return nil, errors.New("PLC unavailable")
	})
	runtime, address, cancel, done := startRuntimeWithOptionsAndFactory(t, factory, RuntimeOptions{PLCEndpointStore: store})
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()
	configure(t, connection)
	_ = receive(t, connection)
	send(t, connection, map[string]any{
		"type": "plc.connect", "requestId": "failed-connect", "deviceId": "easy521://127.0.0.1:1502?unitId=1",
	})
	result := receiveType(t, connection, "plc.connect.result")
	if result["success"] != false || result["state"] == "connected" {
		t.Fatalf("failed connect result claims a connection: %#v", result)
	}
	if _, found, err := loadPLCEndpoint(store); err != nil || found {
		t.Fatalf("failed connection persisted endpoint: found=%t error=%v", found, err)
	}
	runtime.mu.Lock()
	worker := runtime.session.worker
	runtime.mu.Unlock()
	if worker != nil {
		t.Fatal("failed connection left a PLC worker running")
	}
}

func openPLCEndpointStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "block.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}
