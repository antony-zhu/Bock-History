package uplink

import (
	"encoding/json"
	"testing"
	"time"

	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/state"
)

func TestTopicsMatchFrozenMQTTV1Contract(t *testing.T) {
	topics := NewTopics("site-lab", "block-001")
	want := map[string]string{
		"presence": topics.Presence, "hello": topics.Hello, "heartbeat": topics.Heartbeat,
		"snapshot": topics.Snapshot, "event": topics.Event, "alarm": topics.Alarm,
		"replay": topics.Replay, "sync-status": topics.SyncStatus, "down-sync": topics.DownSync,
	}
	for suffix, topic := range want {
		direction := "up"
		if suffix == "down-sync" {
			direction, suffix = "down", "sync"
		}
		expected := "bdm/v1/sites/site-lab/blocks/block-001/" + direction + "/" + suffix
		if topic != expected {
			t.Fatalf("%s topic = %q, want %q", suffix, topic, expected)
		}
	}
}

func TestSnapshotMappingUsesExistingPersistedState(t *testing.T) {
	now := time.Date(2026, 7, 24, 2, 0, 0, 50_000_000, time.UTC)
	view := SnapshotView{
		State: state.Model{
			Revision: 8, UpdatedAt: now.Add(-50 * time.Millisecond), Running: true, Mode: "auto",
			Target: 100, Output: 41, Passed: 40, NG: 1, Blank: 59, Finished: 41,
			Cycle: 10,
		},
		Meta: state.SourceMeta{Quality: plccontract.QualityGood, PLCConnected: true, ReceivedAt: now.Add(-25 * time.Millisecond)},
	}
	payload := BuildSnapshot(view, now)
	if payload.Connectivity != "ONLINE" || payload.OperatingState != "RUNNING" ||
		payload.OperatingMode != "AUTO" || payload.Production.Total != 41 ||
		payload.DataFreshness != 50 {
		encoded, _ := json.Marshal(payload)
		t.Fatalf("unexpected snapshot mapping: %s", encoded)
	}
}

func TestBuildDraftsProducesAlarmAndProductionEdges(t *testing.T) {
	at := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	previous := SnapshotView{
		State: state.Model{UpdatedAt: at, Output: 1, Passed: 1},
		Meta:  state.SourceMeta{Quality: plccontract.QualityGood, PLCConnected: true, ReceivedAt: at},
	}
	current := previous
	current.State.UpdatedAt = at.Add(time.Second)
	current.State.Running = true
	current.State.Output = 2
	current.State.Passed = 2
	current.State.Alarms = []state.Alarm{{ID: 7, Level: "critical", Code: "E7", Text: "fault", OccurredAt: current.State.UpdatedAt}}
	drafts := BuildDrafts(&previous, current, current.State.UpdatedAt, false)
	types := make(map[string]bool)
	for _, draft := range drafts {
		types[draft.Type] = true
	}
	for _, required := range []string{"machine.state.changed", "production.count.changed", "alarm.raised", "device.snapshot"} {
		if !types[required] {
			t.Fatalf("missing %s in %#v", required, types)
		}
	}
}

func TestInitialDraftsRaiseActiveAlarmsInStableOrderBeforeSnapshot(t *testing.T) {
	at := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	current := SnapshotView{
		State: state.Model{
			UpdatedAt: at,
			Alarms: []state.Alarm{
				{ID: 9, Level: "warning", Code: "W9", Text: "later", OccurredAt: at},
				{ID: 2, Level: "critical", Code: "E2", Text: "first", OccurredAt: at},
			},
		},
		Meta: state.SourceMeta{
			Quality: plccontract.QualityGood, PLCConnected: true, ReceivedAt: at,
		},
	}
	drafts := BuildDrafts(nil, current, at, false)
	if len(drafts) != 3 || drafts[0].Type != "alarm.raised" ||
		drafts[1].Type != "alarm.raised" || drafts[2].Type != "device.snapshot" {
		t.Fatalf("initial draft types/order = %#v", drafts)
	}
	first := drafts[0].Payload.(AlarmRaisedPayload)
	second := drafts[1].Payload.(AlarmRaisedPayload)
	if first.AlarmID != "2" || second.AlarmID != "9" {
		t.Fatalf("initial alarm order = %s, %s", first.AlarmID, second.AlarmID)
	}
}

func TestSnapshotQualityChangeProducesReliableSnapshot(t *testing.T) {
	at := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	previous := SnapshotView{
		State: state.Model{Revision: 1, UpdatedAt: at},
		Meta: state.SourceMeta{
			Quality: plccontract.QualityUncertain, PLCConnected: true, ReceivedAt: at,
		},
	}
	current := previous
	current.Meta.Quality = plccontract.QualityBad
	drafts := BuildDrafts(&previous, current, at, false)
	if len(drafts) != 1 || drafts[0].Type != "device.snapshot" || drafts[0].Quality != "BAD" {
		t.Fatalf("quality-only change drafts = %#v", drafts)
	}
}
