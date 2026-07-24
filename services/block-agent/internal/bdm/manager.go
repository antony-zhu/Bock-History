package bdm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"block.local/block-agent/internal/config"
	"block.local/block-agent/internal/mqtt5"
	"block.local/block-agent/internal/storage"
	"block.local/block-agent/internal/uplink"
)

const (
	heartbeatPeriod   = 5 * time.Second
	outboxPollPeriod  = 250 * time.Millisecond
	directRetryPeriod = 30 * time.Second
	checkpointPeriod  = time.Hour
)

type Manager struct {
	config    config.BDM
	source    uplink.Source
	bootID    string
	topics    uplink.Topics
	store     *storage.Store
	now       func() time.Time
	startedAt time.Time

	syncMu       sync.Mutex
	syncPending  *queuedSync
	syncConflict [2]*queuedSync
	syncReady    chan struct{}
	errorMu      sync.RWMutex
	lastError    error
}

type queuedSync struct {
	body     []byte
	revision uint64
	digest   string
}

type permanentSyncReject struct {
	err error
}

func (e *permanentSyncReject) Error() string {
	return e.err.Error()
}

func rejectSyncPermanently(message string, err error) error {
	if err != nil {
		return &permanentSyncReject{err: fmt.Errorf("%s: %w", message, err)}
	}
	return &permanentSyncReject{err: errors.New(message)}
}

func New(
	settings config.BDM,
	source uplink.Source,
	bootID string,
	store *storage.Store,
	now func() time.Time,
) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{
		config: settings, source: source, bootID: bootID,
		topics: uplink.NewTopics(source.SiteID, source.BlockID),
		store:  store, now: now, startedAt: now().UTC(),
		syncReady: make(chan struct{}, 1),
	}
}

func (m *Manager) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		tlsConfig, address, err := mqtt5.TLSConfig(
			m.config.Endpoint, m.config.CAFile, m.config.ClientCertFile,
			m.config.ClientKeyFile, m.config.Principal, m.now().UTC(),
		)
		if err != nil {
			m.setError(err)
			if !wait(ctx, 5*time.Second) {
				return nil
			}
			continue
		}
		lwt, err := uplink.PresenceJSON("OFFLINE", "LWT", m.bootID)
		if err != nil {
			m.setError(err)
			return nil
		}
		client, err := mqtt5.New(mqtt5.Config{
			ClientID: m.config.Principal, Username: m.config.Principal,
			KeepAlive: 30 * time.Second, SessionExpiry: 7 * 24 * 60 * 60,
			MaximumPacketSize: mqtt5.DefaultMaximumPacketSize, ReceiveMaximum: 20,
			SubscribeTopic: m.topics.DownSync,
			Will: mqtt5.Will{
				Topic: m.topics.Presence, Payload: lwt, QoS: 1, Retain: true,
			},
			DialContext:            mqtt5.TLSDialer(address, tlsConfig, 10*time.Second),
			OnMessage:              m.acceptSyncMessage,
			OnError:                m.setError,
			SessionStore:           m.store,
			DeferPendingUntilReady: true,
		})
		if err != nil {
			m.setError(err)
			if !wait(ctx, 5*time.Second) {
				return nil
			}
			continue
		}
		return m.runClient(ctx, client)
	}
}

func (m *Manager) acceptSyncMessage(message mqtt5.Message) error {
	if message.Topic != m.topics.DownSync || message.QoS != 1 || message.Retain {
		return errors.New("only exact non-retained QoS 1 /down/sync messages are accepted")
	}
	item, err := m.validateQueuedSync(message.Payload)
	if err != nil {
		var permanent *permanentSyncReject
		if errors.As(err, &permanent) {
			m.setError(permanent)
			// A permanently invalid application message must still receive its
			// transport PUBACK. Otherwise one poisoned persistent broker entry
			// would prevent every later valid Sync from reaching the Block.
			return nil
		}
		return err
	}
	m.syncMu.Lock()
	switch {
	case m.syncPending == nil:
		m.syncPending = item
	case item.revision > m.syncPending.revision:
		m.syncPending = item
		// The mailbox represents only the highest observed revision. Any
		// conflict pair retained for the superseded revision is obsolete too.
		m.syncConflict = [2]*queuedSync{}
	case item.revision < m.syncPending.revision:
		// A lower revision is obsolete by contract. It is acknowledged only
		// after this application-owned mailbox has compared it with the
		// authoritative highest revision.
	case item.digest == m.syncPending.digest:
		// Byte-different but canonically identical retransmissions are idempotent.
	case m.syncConflict[0] == nil:
		m.syncConflict[0] = m.syncPending
		m.syncConflict[1] = item
	case item.digest == m.syncConflict[0].digest || item.digest == m.syncConflict[1].digest:
		// Retransmission of either already retained conflicting body.
	default:
		m.syncMu.Unlock()
		m.setError(rejectSyncPermanently(
			"more than two conflicting MQTT sync bodies share one revision", nil,
		))
		return nil
	}
	m.syncMu.Unlock()
	select {
	case m.syncReady <- struct{}{}:
	default:
	}
	return nil
}

func (m *Manager) validateQueuedSync(contents []byte) (*queuedSync, error) {
	canonical, err := uplink.CanonicalizeJSON(contents)
	if err != nil {
		return nil, rejectSyncPermanently("invalid MQTT sync JSON", err)
	}
	var message uplink.Sync
	if err := json.Unmarshal(contents, &message); err != nil {
		return nil, rejectSyncPermanently("invalid MQTT sync envelope", err)
	}
	revision, err := uplink.ParseSequence("syncRevision", message.SyncRevision, true)
	if err != nil {
		return nil, rejectSyncPermanently("invalid MQTT sync revision", err)
	}
	if message.SchemaVersion != uplink.SchemaVersion ||
		message.Target.SiteID != m.source.SiteID ||
		message.Target.BlockID != m.source.BlockID ||
		message.Target.DeviceID != m.source.DeviceID {
		return nil, rejectSyncPermanently("MQTT sync envelope does not target this Block", nil)
	}
	if m.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		state, stateErr := m.store.UplinkState(ctx)
		cancel()
		if stateErr != nil {
			return nil, fmt.Errorf("load stream identity before MQTT sync handoff: %w", stateErr)
		}
		if message.StreamEpoch != state.StreamEpoch {
			return nil, rejectSyncPermanently("MQTT sync envelope has the wrong stream epoch", nil)
		}
	}
	return &queuedSync{
		body: append([]byte{}, contents...), revision: revision, digest: string(canonical),
	}, nil
}

func (m *Manager) takeQueuedSync() [][]byte {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	var messages [][]byte
	if m.syncConflict[0] != nil {
		messages = append(messages,
			append([]byte{}, m.syncConflict[0].body...),
			append([]byte{}, m.syncConflict[1].body...),
		)
	}
	if m.syncPending != nil &&
		(m.syncConflict[0] == nil ||
			m.syncPending.revision != m.syncConflict[0].revision ||
			m.syncPending.digest != m.syncConflict[0].digest) {
		messages = append(messages, append([]byte{}, m.syncPending.body...))
	}
	m.syncPending = nil
	m.syncConflict = [2]*queuedSync{}
	return messages
}

func (m *Manager) runClient(ctx context.Context, client *mqtt5.Client) error {
	clientContext, cancelClient := context.WithCancel(context.Background())
	clientDone := make(chan error, 1)
	clientExited := false
	go func() { clientDone <- client.Run(clientContext) }()
	defer func() {
		cancelClient()
		if !clientExited {
			<-clientDone
		}
	}()

	heartbeatTicker := time.NewTicker(heartbeatPeriod)
	defer heartbeatTicker.Stop()
	outboxTicker := time.NewTicker(outboxPollPeriod)
	defer outboxTicker.Stop()
	checkpointTicker := time.NewTicker(checkpointPeriod)
	defer checkpointTicker.Stop()
	var (
		readySession uint64
	)

	for {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = m.publishPresence(shutdownContext, client, "OFFLINE", "graceful_shutdown")
			cancel()
			return nil
		case err := <-clientDone:
			clientExited = true
			if ctx.Err() != nil {
				return nil
			}
			m.setError(err)
			return err
		case <-client.Connected():
			readySession = 0
			sessionID := client.SessionID()
			if sessionID == 0 {
				continue
			}
			sessionPresent := client.SessionPresent(sessionID)
			if !sessionPresent {
				if err := m.store.ResetOutboxPublishSchedule(ctx); err != nil {
					m.setError(err)
					client.AbortSession(sessionID)
					continue
				}
			}
			currentSnapshot, err := m.store.CurrentSnapshotOutbox(ctx, m.now().UTC())
			if err != nil {
				m.setError(err)
				client.AbortSession(sessionID)
				continue
			}
			currentWasPending := currentSnapshot != nil &&
				client.HasPendingApplicationKey(outboxApplicationKey(currentSnapshot.MessageID))
			if sessionPresent {
				// MQTT 5 requires every previously transmitted QoS 1 packet,
				// including connection-control messages, to resume with its
				// original packet id and DUP before any new publish.
				if err := client.ResumePending(sessionID); err != nil {
					m.setError(err)
					client.AbortSession(sessionID)
					continue
				}
			}
			if err := m.publishPresence(ctx, client, "ONLINE", "startup"); err != nil {
				m.setError(err)
				client.AbortSession(sessionID)
				continue
			}
			if err := m.publishHello(ctx, client); err != nil {
				m.setError(err)
				client.AbortSession(sessionID)
				continue
			}
			if err := m.publishSyncStatus(ctx, client); err != nil {
				m.setError(err)
				client.AbortSession(sessionID)
				continue
			}
			if currentSnapshot != nil && !currentWasPending {
				err = m.publishOutboxRecord(ctx, client, *currentSnapshot)
				if err != nil {
					m.setError(err)
					client.AbortSession(sessionID)
					continue
				}
			}
			if client.SessionID() != sessionID {
				continue
			}
			readySession = sessionID
			for _, body := range m.takeQueuedSync() {
				if err := m.processSync(ctx, client, body); err != nil {
					m.setError(err)
				}
			}
		case <-m.syncReady:
			if readySession == 0 || client.SessionID() != readySession {
				continue
			}
			for _, body := range m.takeQueuedSync() {
				if err := m.processSync(ctx, client, body); err != nil {
					m.setError(err)
				}
			}
		case <-outboxTicker.C:
			if readySession == 0 || client.SessionID() != readySession {
				continue
			}
			if err := m.publishNextOutbox(ctx, client); err != nil &&
				!errors.Is(err, mqtt5.ErrDisconnected) {
				m.setError(err)
			}
		case <-heartbeatTicker.C:
			if readySession == 0 || client.SessionID() != readySession {
				continue
			}
			if err := m.publishHeartbeat(client); err != nil &&
				!errors.Is(err, mqtt5.ErrDisconnected) {
				m.setError(err)
			}
		case <-checkpointTicker.C:
			if err := m.store.CheckpointPassive(ctx); err != nil {
				m.setError(err)
			}
		}
	}
}

func (m *Manager) publishPresence(
	ctx context.Context,
	client *mqtt5.Client,
	status, reason string,
) error {
	body, err := uplink.PresenceJSON(status, reason, m.bootID)
	if err != nil {
		return err
	}
	return client.Publish(ctx, mqtt5.Message{
		Topic: m.topics.Presence, Payload: body, QoS: 1, Retain: true,
	})
}

func (m *Manager) publishHello(ctx context.Context, client *mqtt5.Client) error {
	stream, err := m.store.UplinkState(ctx)
	if err != nil {
		return err
	}
	message, err := uplink.NewControl(
		"block.hello", m.source, m.bootID, m.currentQuality(ctx),
		uplink.HelloPayload{
			SoftwareVersion: m.config.SoftwareVersion, OSVersion: m.config.OSVersion,
			Architecture: m.config.Architecture, HardwareModel: m.config.HardwareModel,
			Capabilities:              []string{"snapshot", "event", "alarm", "replay"},
			SupportedProtocolVersions: []string{"v1"},
			DesiredConfigRevision:     "0", ReportedConfigRevision: "0",
			StreamEpoch: stream.StreamEpoch, StreamGeneration: stream.StreamGeneration,
			StreamEpochStartedAt:   uplink.FormatTime(stream.EpochStartedAt),
			FirstAvailableSequence: uplink.FormatSequence(stream.FirstAvailableSequence),
			LastProducedSequence:   uplink.FormatSequence(stream.LastProducedSequence),
			LastAckedSequence:      uplink.FormatSequence(stream.LastAckedSequence),
		},
		m.now().UTC(),
	)
	if err != nil {
		return err
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return client.Publish(ctx, mqtt5.Message{
		Topic: m.topics.Hello, Payload: body, QoS: 1,
	})
}

func (m *Manager) publishHeartbeat(client *mqtt5.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := m.store.UplinkState(ctx)
	if err != nil {
		return err
	}
	connected := false
	var lastSample *string
	if record, err := m.store.LoadSnapshot(ctx); err == nil {
		connected = record.Meta.PLCConnected
		value := uplink.FormatTime(record.Meta.ReceivedAt)
		lastSample = &value
	}
	uptime := m.now().UTC().Sub(m.startedAt)
	if uptime < 0 {
		uptime = 0
	}
	message, err := uplink.NewControl(
		"block.heartbeat", m.source, m.bootID, m.currentQuality(ctx),
		uplink.HeartbeatPayload{
			UptimeSec: uint64(uptime / time.Second), CPUPercent: 0, MemoryPercent: 0,
			DiskFreeBytes: 0, ClockOffsetMs: 0,
			DeviceConnections: []uplink.DeviceConnection{{
				DeviceID: m.source.DeviceID, Connected: connected, LastSampleAt: lastSample,
			}},
			OutboxPending:        stream.OutboxPending,
			LastProducedSequence: uplink.FormatSequence(stream.LastProducedSequence),
			LastAckedSequence:    uplink.FormatSequence(stream.LastAckedSequence),
		},
		m.now().UTC(),
	)
	if err != nil {
		return err
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return client.PublishQoS0(mqtt5.Message{
		Topic: m.topics.Heartbeat, Payload: body, QoS: 0,
	})
}

func (m *Manager) publishSyncStatus(ctx context.Context, client *mqtt5.Client) error {
	stream, err := m.store.UplinkState(ctx)
	if err != nil {
		return err
	}
	message, err := uplink.NewControl(
		"sync.status", m.source, m.bootID, m.currentQuality(ctx),
		uplink.SyncStatusPayload{
			StreamEpoch:            stream.StreamEpoch,
			FirstAvailableSequence: uplink.FormatSequence(stream.FirstAvailableSequence),
			LastProducedSequence:   uplink.FormatSequence(stream.LastProducedSequence),
			LastAckedSequence:      uplink.FormatSequence(stream.LastAckedSequence),
		},
		m.now().UTC(),
	)
	if err != nil {
		return err
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return client.Publish(ctx, mqtt5.Message{
		Topic: m.topics.SyncStatus, Payload: body, QoS: 1,
	})
}

func (m *Manager) publishNextOutbox(ctx context.Context, client *mqtt5.Client) error {
	records, err := m.store.NextOutboxDue(ctx, m.now().UTC(), directRetryPeriod, 1)
	if err != nil || len(records) == 0 {
		return err
	}
	if client.HasPendingApplicationKey(outboxApplicationKey(records[0].MessageID)) {
		return nil
	}
	return m.publishOutboxRecord(ctx, client, records[0])
}

func (m *Manager) publishOutboxRecord(
	ctx context.Context,
	client *mqtt5.Client,
	record storage.OutboxRecord,
) error {
	body, err := m.store.PrepareDirectAttempt(ctx, record, m.now().UTC())
	if err != nil {
		return err
	}
	topic, err := m.topics.Upstream(record.Channel)
	if err != nil {
		return err
	}
	// Transport PUBACK completes this call but deliberately does not remove the
	// row. Only an application-level /down/sync ACK can delete it.
	return client.Publish(ctx, mqtt5.Message{
		Topic: topic, Payload: body, QoS: 1,
		ApplicationKey: outboxApplicationKey(record.MessageID),
	})
}

func (m *Manager) processSync(ctx context.Context, client *mqtt5.Client, body []byte) error {
	result, err := m.store.ApplySyncJSON(ctx, body)
	if err != nil {
		return err
	}
	if !result.Ignored && !result.Duplicate && len(result.RequestRanges) > 0 {
		replays, err := m.store.BuildReplayBatches(ctx, result.RequestRanges, m.now().UTC())
		if err != nil {
			return err
		}
		for _, replay := range replays {
			if err := client.Publish(ctx, mqtt5.Message{
				Topic: m.topics.Replay, Payload: replay, QoS: 1,
			}); err != nil {
				return err
			}
		}
	}
	if shouldPublishSyncStatus(result) {
		return m.publishSyncStatus(ctx, client)
	}
	return nil
}

func shouldPublishSyncStatus(result storage.SyncResult) bool {
	return !result.Ignored && !result.Duplicate &&
		(result.StateChanged || len(result.RequestRanges) > 0)
}

func outboxApplicationKey(messageID string) string {
	return "outbox:" + messageID
}

func (m *Manager) currentQuality(ctx context.Context) string {
	record, err := m.store.LoadSnapshot(ctx)
	if err != nil {
		return "BAD"
	}
	// The snapshot builder uses the same quality/connectivity source as the
	// reliable stream. Connectivity is sufficient to distinguish STALE here.
	if record.Stale {
		return "STALE"
	}
	switch string(record.Meta.Quality) {
	case "GOOD":
		return "GOOD"
	case "UNCERTAIN":
		return "UNCERTAIN"
	default:
		return "BAD"
	}
}

func (m *Manager) LastError() error {
	m.errorMu.RLock()
	defer m.errorMu.RUnlock()
	return m.lastError
}

func (m *Manager) setError(err error) {
	if err == nil {
		return
	}
	m.errorMu.Lock()
	m.lastError = err
	m.errorMu.Unlock()
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
