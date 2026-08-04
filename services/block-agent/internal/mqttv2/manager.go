package mqttv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"block.local/block-agent/internal/alarmhistory"
)

type Options struct {
	Now             func() time.Time
	PublishInterval time.Duration
}

type Manager struct {
	mu sync.Mutex

	source   Source
	topics   Topics
	sender   Sender
	history  HistoryReader
	now      func() time.Time
	interval time.Duration

	connected           bool
	hasLatest           bool
	latest              Snapshot
	lastAttemptedState  string
	lastPeriodicPublish time.Time
}

func NewManager(source Source, sender Sender, history HistoryReader, options Options) *Manager {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	interval := options.PublishInterval
	if interval <= 0 {
		interval = DefaultPublishInterval
	}
	return &Manager{
		source: source, topics: NewTopics(source.SiteID, source.BlockID),
		sender: sender, history: history, now: now, interval: interval,
	}
}

// Connected starts a connection window and publishes at most one latest state.
func (m *Manager) Connected(ctx context.Context) error {
	m.mu.Lock()
	if m.connected {
		m.mu.Unlock()
		return nil
	}
	m.connected = true
	publication, ok, err := m.currentPublicationLocked("", true)
	m.lastPeriodicPublish = m.now().UTC()
	m.mu.Unlock()
	if err != nil || !ok {
		return err
	}
	return m.publish(ctx, publication)
}

func (m *Manager) Disconnected() {
	m.mu.Lock()
	m.connected = false
	m.mu.Unlock()
}

// ObserveSnapshot stores only the latest state. A changed good state is sent
// once when connected; failed sends are not retried or queued.
func (m *Manager) ObserveSnapshot(ctx context.Context, snapshot Snapshot) error {
	m.mu.Lock()
	m.latest = cloneSnapshot(snapshot)
	m.hasLatest = true
	publication, ok, err := m.currentPublicationLocked("", false)
	m.mu.Unlock()
	if err != nil || !ok {
		return err
	}
	return m.publish(ctx, publication)
}

// Tick emits the current latest state at the configured interval.
func (m *Manager) Tick(ctx context.Context) error {
	m.mu.Lock()
	now := m.now().UTC()
	if !m.connected || (!m.lastPeriodicPublish.IsZero() && now.Sub(m.lastPeriodicPublish) < m.interval) {
		m.mu.Unlock()
		return nil
	}
	publication, ok, err := m.currentPublicationLocked("", true)
	m.lastPeriodicPublish = now
	m.mu.Unlock()
	if err != nil || !ok {
		return err
	}
	return m.publish(ctx, publication)
}

// Notify satisfies alarmhistory.Notifier. Store errors are handled by the
// alarm-history service before this best-effort call is attempted.
func (m *Manager) Notify(ctx context.Context, record alarmhistory.Record) error {
	m.mu.Lock()
	if !m.connected {
		m.mu.Unlock()
		return ErrDisconnected
	}
	value := AlarmNotification{
		Type: "alarm.history.notification", SchemaVersion: SchemaVersion,
		Source: m.source, SentAt: m.now().UTC(), Alarm: record,
	}
	topic := m.topics.AlarmNotify
	m.mu.Unlock()
	return m.publishValue(ctx, topic, value)
}

// HandleInbound accepts only the two read-only v2 requests.
func (m *Manager) HandleInbound(ctx context.Context, inbound Inbound) error {
	if inbound.QoS != 0 || inbound.Retain {
		return ErrInboundPolicy
	}
	switch inbound.Topic {
	case m.topics.StateGet:
		return m.handleStateGet(ctx, inbound.Payload)
	case m.topics.AlarmHistoryQuery:
		return m.handleAlarmHistoryQuery(ctx, inbound.Payload)
	default:
		return fmt.Errorf("%w: unexpected topic %q", ErrInvalidRequest, inbound.Topic)
	}
}

func (m *Manager) handleStateGet(ctx context.Context, payload []byte) error {
	var request StateGet
	if err := decodeStrict(payload, &request); err != nil {
		return err
	}
	if request.Type != "device.state.get" || request.SchemaVersion != SchemaVersion ||
		!validRequestID(request.RequestID) || !m.matchesTarget(request.Target) {
		return ErrInvalidRequest
	}
	m.mu.Lock()
	if !m.connected {
		m.mu.Unlock()
		return ErrDisconnected
	}
	publication, ok, err := m.currentPublicationLocked(request.RequestID, true)
	m.mu.Unlock()
	if err != nil || !ok {
		return err
	}
	return m.publish(ctx, publication)
}

func (m *Manager) handleAlarmHistoryQuery(ctx context.Context, payload []byte) error {
	var request AlarmHistoryQuery
	if err := decodeStrict(payload, &request); err != nil {
		return err
	}
	if request.Type != "alarm.history.query" || request.SchemaVersion != SchemaVersion ||
		!validRequestID(request.RequestID) || !m.matchesTarget(request.Target) ||
		request.Limit < 1 || request.Limit > alarmhistory.MaxPageSize {
		return ErrInvalidRequest
	}
	if !strings.HasSuffix(request.FromOccurredAt, "Z") || !strings.HasSuffix(request.ToOccurredAt, "Z") {
		return ErrInvalidRequest
	}
	from, err := time.Parse(time.RFC3339Nano, request.FromOccurredAt)
	if err != nil {
		return ErrInvalidRequest
	}
	to, err := time.Parse(time.RFC3339Nano, request.ToOccurredAt)
	if err != nil {
		return ErrInvalidRequest
	}
	query := alarmhistory.Query{
		FromOccurredAt: from, ToOccurredAt: to, Limit: request.Limit,
	}
	if request.AfterCursor != nil {
		query.AfterCursor = *request.AfterCursor
	}
	if err := query.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	connected := m.connected
	history := m.history
	source := m.source
	topic := m.topics.AlarmHistoryPage
	m.mu.Unlock()
	if !connected {
		return ErrDisconnected
	}
	if history == nil {
		return errors.New("alarm history reader is unavailable")
	}
	page, err := history.List(ctx, query)
	if err != nil {
		return err
	}
	if err := page.Validate(); err != nil {
		return err
	}
	value := AlarmHistoryPage{
		Type: "alarm.history.page", SchemaVersion: SchemaVersion,
		RequestID: request.RequestID, Source: source, Records: page.Records,
		NextCursor: page.NextCursor, HasMore: page.HasMore,
	}
	return m.publishValue(ctx, topic, value)
}

func (m *Manager) currentPublicationLocked(requestID string, force bool) (Publication, bool, error) {
	if !m.connected || !m.hasLatest {
		return Publication{}, false, nil
	}
	if !validSource(m.source) {
		return Publication{}, false, ErrInvalidRequest
	}
	points, stateAt := goodPoints(m.latest)
	if len(points) == 0 {
		return Publication{}, false, nil
	}
	fingerprint := stateFingerprint(points)
	if !force && fingerprint == m.lastAttemptedState {
		return Publication{}, false, nil
	}
	m.lastAttemptedState = fingerprint
	now := m.now().UTC()
	if stateAt.IsZero() {
		stateAt = now
	}
	value := StateCurrent{
		Type: "device.state.current", SchemaVersion: SchemaVersion,
		Source: m.source, RequestID: requestID, StateAt: stateAt.UTC(),
		SentAt: now, State: points,
	}
	publication, err := publicationFor(m.topics.StateLatest, value)
	if err != nil {
		return Publication{}, false, err
	}
	return publication, true, nil
}

func (m *Manager) matchesTarget(target Source) bool {
	return target.SiteID == m.source.SiteID &&
		target.BlockID == m.source.BlockID &&
		target.DeviceID == m.source.DeviceID
}

func (m *Manager) publish(ctx context.Context, publication Publication) error {
	if publication.QoS != 0 || publication.Retain {
		return ErrInvalidRequest
	}
	if m.sender == nil {
		return ErrSenderMissing
	}
	return m.sender.Publish(ctx, publication)
}

func (m *Manager) publishValue(ctx context.Context, topic string, value any) error {
	publication, err := publicationFor(topic, value)
	if err != nil {
		return err
	}
	return m.publish(ctx, publication)
}

func publicationFor(topic string, value any) (Publication, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return Publication{}, err
	}
	if len(payload) > maximumJSONPayload {
		return Publication{}, errors.New("mqtt v2 JSON payload is too large")
	}
	return Publication{Topic: topic, Payload: payload, QoS: 0, Retain: false}, nil
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	values := make(map[string]PointValue, len(snapshot.Values))
	for pointID, value := range snapshot.Values {
		values[pointID] = value
	}
	return Snapshot{Values: values, StateAt: snapshot.StateAt}
}
