package mqttv2

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"block.local/block-agent/internal/alarmhistory"
)

type recordedSender struct {
	messages []Publication
	err      error
}

func (s *recordedSender) Publish(_ context.Context, publication Publication) error {
	copyOfPayload := append([]byte(nil), publication.Payload...)
	s.messages = append(s.messages, Publication{
		Topic: publication.Topic, Payload: copyOfPayload, QoS: publication.QoS, Retain: publication.Retain,
	})
	return s.err
}

type fakeHistory struct {
	page  alarmhistory.Page
	query alarmhistory.Query
	err   error
}

func (h *fakeHistory) List(_ context.Context, query alarmhistory.Query) (alarmhistory.Page, error) {
	h.query = query
	return h.page, h.err
}

func source() Source {
	return Source{SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001"}
}

func snapshot(value bool, at time.Time) Snapshot {
	return Snapshot{Values: map[string]PointValue{
		"machine.startFeedback": {Value: value, Quality: QualityGood, UpdatedAt: at},
		"alarm.emergencyStop":   {Value: false, Quality: QualityStale, UpdatedAt: at},
	}}
}

func TestObserveChangeAndPeriodicPublishUseQoS0(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	sender := &recordedSender{}
	manager := NewManager(source(), sender, nil, Options{
		Now: func() time.Time { return now },
	})

	if err := manager.Connected(context.Background()); err != nil {
		t.Fatalf("Connected() error = %v", err)
	}
	if err := manager.ObserveSnapshot(context.Background(), snapshot(false, now)); err != nil {
		t.Fatalf("ObserveSnapshot() error = %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(sender.messages))
	}
	first := sender.messages[0]
	if first.Topic != NewTopics("site-lab", "block-001").StateLatest || first.QoS != 0 || first.Retain {
		t.Fatalf("publication = %#v", first)
	}
	var current StateCurrent
	if err := json.Unmarshal(first.Payload, &current); err != nil {
		t.Fatalf("unmarshal state = %v", err)
	}
	if current.Type != "device.state.current" || len(current.State) != 1 ||
		current.State["machine.startFeedback"].Value != false {
		t.Fatalf("state = %#v", current)
	}

	now = now.Add(time.Second)
	if err := manager.ObserveSnapshot(context.Background(), snapshot(false, now)); err != nil {
		t.Fatalf("same ObserveSnapshot() error = %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("same snapshot published again: %d", len(sender.messages))
	}
	now = now.Add(59 * time.Second)
	if err := manager.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	if len(sender.messages) != 2 {
		t.Fatalf("periodic messages = %d, want 2", len(sender.messages))
	}
}

func TestDisconnectedDropsIntermediateAndReconnectSendsOneLatest(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	sender := &recordedSender{}
	manager := NewManager(source(), sender, nil, Options{Now: func() time.Time { return now }})

	if err := manager.Connected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ObserveSnapshot(context.Background(), snapshot(false, now)); err != nil {
		t.Fatal(err)
	}
	manager.Disconnected()
	now = now.Add(time.Second)
	if err := manager.ObserveSnapshot(context.Background(), snapshot(true, now)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := manager.ObserveSnapshot(context.Background(), snapshot(false, now)); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("disconnected messages = %d, want 1", len(sender.messages))
	}
	if err := manager.Connected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Connected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 {
		t.Fatalf("reconnect messages = %d, want exactly 2", len(sender.messages))
	}
}

func TestStateGetPublishesCurrentWithoutReplay(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	sender := &recordedSender{}
	manager := NewManager(source(), sender, nil, Options{Now: func() time.Time { return now }})
	if err := manager.Connected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ObserveSnapshot(context.Background(), snapshot(true, now)); err != nil {
		t.Fatal(err)
	}
	requestID := "315a1ea6-1cdc-47d9-96f9-b4f80ffbda7c"
	payload, _ := json.Marshal(StateGet{
		Type: "device.state.get", SchemaVersion: SchemaVersion, RequestID: requestID, Target: source(),
	})
	if err := manager.HandleInbound(context.Background(), Inbound{
		Topic: NewTopics("site-lab", "block-001").StateGet, Payload: payload, QoS: 0,
	}); err != nil {
		t.Fatalf("HandleInbound(state/get) error = %v", err)
	}
	if len(sender.messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(sender.messages))
	}
	var current StateCurrent
	if err := json.Unmarshal(sender.messages[1].Payload, &current); err != nil {
		t.Fatal(err)
	}
	if current.RequestID != requestID {
		t.Fatalf("requestId = %q, want %q", current.RequestID, requestID)
	}
}

func TestAlarmNotificationAndReadOnlyHistoryPage(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	cursor := uint64(4)
	history := &fakeHistory{page: alarmhistory.Page{
		Records:    []alarmhistory.Record{{AlarmRecordID: "alarm-4", HistoryCursor: cursor}},
		NextCursor: &cursor, HasMore: true,
	}}
	sender := &recordedSender{}
	manager := NewManager(source(), sender, history, Options{Now: func() time.Time { return now }})
	if err := manager.Connected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Notify(context.Background(), alarmhistory.Record{
		AlarmRecordID: "alarm-3", HistoryCursor: 3, AlarmID: "alarm.emergencyStop",
	}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	requestID := "58fc3653-55d3-445b-a9f1-8202e08af72d"
	payload, _ := json.Marshal(AlarmHistoryQuery{
		Type: "alarm.history.query", SchemaVersion: SchemaVersion, RequestID: requestID, Target: source(),
		FromOccurredAt: now.Format(time.RFC3339), ToOccurredAt: now.Add(time.Hour).Format(time.RFC3339),
		Limit: 20,
	})
	if err := manager.HandleInbound(context.Background(), Inbound{
		Topic: NewTopics("site-lab", "block-001").AlarmHistoryQuery, Payload: payload, QoS: 0,
	}); err != nil {
		t.Fatalf("HandleInbound(history) error = %v", err)
	}
	if len(sender.messages) != 2 || history.query.Limit != 20 {
		t.Fatalf("messages = %d, query = %#v", len(sender.messages), history.query)
	}
	var page AlarmHistoryPage
	if err := json.Unmarshal(sender.messages[1].Payload, &page); err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextCursor == nil || *page.NextCursor != cursor {
		t.Fatalf("history page = %#v", page)
	}
}

func TestFailedSendIsNotAutomaticallyRetried(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	sender := &recordedSender{err: errors.New("network down")}
	manager := NewManager(source(), sender, nil, Options{Now: func() time.Time { return now }})
	if err := manager.Connected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ObserveSnapshot(context.Background(), snapshot(true, now)); err == nil {
		t.Fatal("ObserveSnapshot() error = nil, want sender error")
	}
	now = now.Add(time.Second)
	if err := manager.ObserveSnapshot(context.Background(), snapshot(true, now)); err != nil {
		t.Fatalf("same snapshot should not retry: %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("sender calls = %d, want 1", len(sender.messages))
	}
}
