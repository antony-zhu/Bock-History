package pointstore

import (
	"testing"
	"time"

	"block.local/block-agent/internal/runtimeconfig"
)

func TestStoreReplacesSessionAndReturnsAbsoluteChanges(t *testing.T) {
	store := New()
	config := runtimeconfig.Config{
		ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs,
		Points: []runtimeconfig.PointDefinition{{
			PointID: "alarm.stop", Address: "M0.1", Type: "bool", Access: "read", ReadPoint: "alarm.stop",
		}},
	}
	if err := store.Replace(config); err != nil {
		t.Fatal(err)
	}
	active := true
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	changed, err := store.Update(map[string]PointValue{
		"alarm.stop": {Value: true, Quality: "good", UpdatedAt: now, AlarmActive: &active},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := changed["alarm.stop"].Value; got != true {
		t.Fatalf("changed value = %#v, want true", got)
	}

	changed, err = store.Update(map[string]PointValue{
		"alarm.stop": {Value: true, Quality: "good", UpdatedAt: now.Add(time.Second), AlarmActive: &active},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("timestamp-only update emitted %#v", changed)
	}

	snapshot := store.Snapshot()
	snapshotValue := snapshot["alarm.stop"]
	*snapshotValue.AlarmActive = false
	storedValue := store.Snapshot()["alarm.stop"]
	if *storedValue.AlarmActive != true {
		t.Fatal("snapshot exposed mutable alarm state")
	}

	store.Clear()
	if store.Configured() || len(store.Snapshot()) != 0 {
		t.Fatal("Clear did not remove the in-memory session")
	}
}

func TestStoreRejectsUnknownPoints(t *testing.T) {
	store := New()
	if _, err := store.Update(map[string]PointValue{"unknown": {Value: true, Quality: "good", UpdatedAt: time.Now()}}); err == nil {
		t.Fatal("unknown point update unexpectedly succeeded")
	}
}

func TestStoreAllowsUnknownValueBeforeFirstSuccessfulPLCRead(t *testing.T) {
	store := New()
	config := runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{{
		PointID: "alarm.stop", Address: "M0.1", Type: "bool", Access: "read", ReadPoint: "alarm.stop",
	}}}
	if err := store.Replace(config); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(map[string]PointValue{
		"alarm.stop": {Quality: "stale", UpdatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("stale unknown value was rejected: %v", err)
	}
	if value := store.Snapshot()["alarm.stop"]; value.Value != nil || value.AlarmActive != nil {
		t.Fatalf("unread PLC point was fabricated: %#v", value)
	}
}
