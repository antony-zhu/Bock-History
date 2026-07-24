package bdm

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"block.local/block-agent/internal/config"
	"block.local/block-agent/internal/mqtt5"
	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/state"
	"block.local/block-agent/internal/storage"
	"block.local/block-agent/internal/uplink"
)

func TestAcceptSyncMessageRequiresExactNonRetainedQoS1Channel(t *testing.T) {
	manager := New(
		config.BDM{},
		uplink.Source{SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001"},
		"20000000-0000-4000-8000-000000000001",
		nil,
		nil,
	)
	body, err := json.Marshal(uplink.Sync{
		SchemaVersion: uplink.SchemaVersion,
		SyncID:        "40000000-0000-4000-8000-000000000001",
		SyncRevision:  "1",
		Target: uplink.SyncTarget{
			SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
		},
		IssuedAt:                  "2026-07-24T02:00:00.000Z",
		Action:                    "ACK",
		StreamEpoch:               "30000000-0000-4000-8000-000000000001",
		HighestContiguousSequence: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := mqtt5.Message{
		Topic: manager.topics.DownSync, Payload: body, QoS: 1,
	}
	if err := manager.acceptSyncMessage(valid); err != nil {
		t.Fatal(err)
	}
	receivedMessages := manager.takeQueuedSync()
	if len(receivedMessages) != 1 {
		t.Fatalf("queued sync count = %d", len(receivedMessages))
	}
	received := receivedMessages[0]
	valid.Payload[0] = '!'
	if len(received) == 0 || received[0] != '{' {
		t.Fatalf("queued payload was not copied: %s", received)
	}
	for name, message := range map[string]mqtt5.Message{
		"wrong topic": {
			Topic: manager.topics.DownSync + "/extra", Payload: []byte(`{}`), QoS: 1,
		},
		"wrong qos": {
			Topic: manager.topics.DownSync, Payload: []byte(`{}`), QoS: 0,
		},
		"retained": {
			Topic: manager.topics.DownSync, Payload: []byte(`{}`), QoS: 1, Retain: true,
		},
	} {
		if err := manager.acceptSyncMessage(message); err == nil {
			t.Fatalf("%s message unexpectedly accepted", name)
		}
	}
}

func TestSyncMailboxKeepsHighestRevisionAndRetainsSameRevisionConflict(t *testing.T) {
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	source := uplink.Source{
		SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
	}
	store, err := storage.OpenWithOptions(
		filepath.Join(t.TempDir(), "block.db"),
		func() time.Time { return now },
		storage.UplinkOptions{
			Enabled: true, Source: source,
			BootID:           "20000000-0000-4000-8000-000000000001",
			StreamGeneration: "1", StaleAfter: 5 * time.Second,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stream, err := store.UplinkState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager := New(config.BDM{}, source, "20000000-0000-4000-8000-000000000001", store, nil)

	for _, revision := range []uint64{1, 64, 32} {
		if err := manager.acceptSyncMessage(mqtt5.Message{
			Topic:   manager.topics.DownSync,
			Payload: managerSyncJSON(t, source, stream.StreamEpoch, revision, 0, now),
			QoS:     1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	wrongTarget := source
	wrongTarget.BlockID = "block-other"
	if err := manager.acceptSyncMessage(mqtt5.Message{
		Topic:   manager.topics.DownSync,
		Payload: managerSyncJSON(t, wrongTarget, stream.StreamEpoch, 999, 0, now),
		QoS:     1,
	}); err != nil {
		t.Fatalf("permanent wrong-target reject must be transport-acked: %v", err)
	}
	queued := manager.takeQueuedSync()
	if len(queued) != 1 {
		t.Fatalf("coalesced mailbox count = %d", len(queued))
	}
	var highest uplink.Sync
	if err := json.Unmarshal(queued[0], &highest); err != nil {
		t.Fatal(err)
	}
	if highest.SyncRevision != "64" {
		t.Fatalf("mailbox retained revision %q, want 64", highest.SyncRevision)
	}
	if manager.LastError() == nil {
		t.Fatal("wrong-target sync was not recorded")
	}

	firstConflict := managerSyncJSON(t, source, stream.StreamEpoch, 65, 0, now)
	secondConflict := managerSyncJSON(t, source, stream.StreamEpoch, 65, 0, now.Add(time.Second))
	for _, body := range [][]byte{firstConflict, secondConflict} {
		if err := manager.acceptSyncMessage(mqtt5.Message{
			Topic: manager.topics.DownSync, Payload: body, QoS: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	conflicts := manager.takeQueuedSync()
	if len(conflicts) != 2 {
		t.Fatalf("same-revision conflict mailbox count = %d", len(conflicts))
	}
	if _, err := store.ApplySyncJSON(context.Background(), conflicts[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySyncJSON(context.Background(), conflicts[1]); !errors.Is(err, storage.ErrSyncConflict) {
		t.Fatalf("retained same-revision conflict error = %v", err)
	}

	for _, body := range [][]byte{
		managerSyncJSON(t, source, stream.StreamEpoch, 66, 0, now),
		managerSyncJSON(t, source, stream.StreamEpoch, 66, 0, now.Add(time.Second)),
		managerSyncJSON(t, source, stream.StreamEpoch, 67, 0, now.Add(2*time.Second)),
		managerSyncJSON(t, source, stream.StreamEpoch, 67, 0, now.Add(3*time.Second)),
		managerSyncJSON(t, source, stream.StreamEpoch, 66, 0, now.Add(4*time.Second)),
	} {
		if err := manager.acceptSyncMessage(mqtt5.Message{
			Topic: manager.topics.DownSync, Payload: body, QoS: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	higherConflicts := manager.takeQueuedSync()
	if len(higherConflicts) != 2 {
		t.Fatalf("superseding conflict mailbox count = %d", len(higherConflicts))
	}
	for index, body := range higherConflicts {
		var message uplink.Sync
		if err := json.Unmarshal(body, &message); err != nil {
			t.Fatal(err)
		}
		if message.SyncRevision != "67" {
			t.Fatalf("superseding conflict[%d] revision = %q, want 67", index, message.SyncRevision)
		}
	}
}

func TestPermanentOldEpochSyncIsDroppedWithoutBlockingLaterValidSync(t *testing.T) {
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	source := uplink.Source{
		SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
	}
	store, err := storage.OpenWithOptions(
		filepath.Join(t.TempDir(), "block.db"),
		func() time.Time { return now },
		storage.UplinkOptions{
			Enabled: true, Source: source,
			BootID:           "20000000-0000-4000-8000-000000000001",
			StreamGeneration: "1", StaleAfter: 5 * time.Second,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stream, err := store.UplinkState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager := New(config.BDM{}, source, "20000000-0000-4000-8000-000000000001", store, nil)
	oldEpoch := "30000000-0000-4000-8000-000000000099"
	if oldEpoch == stream.StreamEpoch {
		t.Fatal("test old epoch unexpectedly equals current epoch")
	}
	if err := manager.acceptSyncMessage(mqtt5.Message{
		Topic:   manager.topics.DownSync,
		Payload: managerSyncJSON(t, source, oldEpoch, 1, 0, now),
		QoS:     1,
	}); err != nil {
		t.Fatalf("old epoch must be acknowledged and dropped: %v", err)
	}
	if messages := manager.takeQueuedSync(); len(messages) != 0 {
		t.Fatalf("old epoch entered application mailbox: %d", len(messages))
	}
	valid := managerSyncJSON(t, source, stream.StreamEpoch, 2, 0, now.Add(time.Second))
	if err := manager.acceptSyncMessage(mqtt5.Message{
		Topic: manager.topics.DownSync, Payload: valid, QoS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	messages := manager.takeQueuedSync()
	if len(messages) != 1 || string(messages[0]) != string(valid) {
		t.Fatalf("later valid sync was not retained: %d", len(messages))
	}
}

func TestSyncStatusFeedbackConvergesAfterFinalACK(t *testing.T) {
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	source := uplink.Source{
		SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
	}
	store, err := storage.OpenWithOptions(
		filepath.Join(t.TempDir(), "block.db"),
		func() time.Time { return now },
		storage.UplinkOptions{
			Enabled: true, Source: source,
			BootID:           "20000000-0000-4000-8000-000000000001",
			StreamGeneration: "1", StaleAfter: 5 * time.Second,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveSnapshot(context.Background(), managerSnapshot(now, 1)); err != nil {
		t.Fatal(err)
	}
	stream, err := store.UplinkState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := managerSyncJSON(
		t, source, stream.StreamEpoch, 1, stream.LastProducedSequence, now.Add(time.Second),
	)
	firstResult, err := store.ApplySyncJSON(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if !firstResult.StateChanged || !shouldPublishSyncStatus(firstResult) {
		t.Fatalf("advancing final ACK must produce one status: %+v", firstResult)
	}
	second := managerSyncJSON(
		t, source, stream.StreamEpoch, 2, stream.LastProducedSequence, now.Add(2*time.Second),
	)
	secondResult, err := store.ApplySyncJSON(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.StateChanged || shouldPublishSyncStatus(secondResult) {
		t.Fatalf("same-waterline higher revision must converge silently: %+v", secondResult)
	}
	duplicate, err := store.ApplySyncJSON(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || shouldPublishSyncStatus(duplicate) {
		t.Fatalf("duplicate Sync must not emit status: %+v", duplicate)
	}
}

func TestConnectionGatePublishesPreambleAndCurrentSnapshotBeforeBacklog(t *testing.T) {
	initialNow := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(initialNow.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	source := uplink.Source{
		SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
	}
	store, err := storage.OpenWithOptions(
		filepath.Join(t.TempDir(), "block.db"),
		now,
		storage.UplinkOptions{
			Enabled: true, Source: source,
			BootID:           "20000000-0000-4000-8000-000000000001",
			StreamGeneration: "1", StaleAfter: 5 * time.Second,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveSnapshot(context.Background(), managerSnapshot(initialNow, 1)); err != nil {
		t.Fatal(err)
	}

	manager := New(
		config.BDM{
			SoftwareVersion: "test", OSVersion: "Ubuntu", Architecture: "arm64",
			HardwareModel: "test",
		},
		source, "20000000-0000-4000-8000-000000000001", store,
		now,
	)
	firstClient, firstBroker := net.Pipe()
	secondClient, secondBroker := net.Pipe()
	allowSecond := make(chan struct{})
	var dialAttempt int
	client, err := mqtt5.New(mqtt5.Config{
		ClientID:  "blk-0123456789abcdef0123456789abcdef",
		Username:  "blk-0123456789abcdef0123456789abcdef",
		KeepAlive: 30 * time.Second, SessionExpiry: 7 * 24 * 60 * 60,
		MaximumPacketSize: mqtt5.DefaultMaximumPacketSize, ReceiveMaximum: 20,
		SubscribeTopic: manager.topics.DownSync,
		Will: mqtt5.Will{
			Topic: manager.topics.Presence, Payload: []byte(`{"status":"OFFLINE"}`),
			QoS: 1, Retain: true,
		},
		DialContext: func(ctx context.Context) (net.Conn, error) {
			dialAttempt++
			switch dialAttempt {
			case 1:
				timer := time.NewTimer(350 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
					return firstClient, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			case 2:
				select {
				case <-allowSecond:
					return secondClient, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			default:
				<-ctx.Done()
				return nil, ctx.Err()
			}
		},
		ReconnectDelay:         func(int) time.Duration { return time.Millisecond },
		OnMessage:              manager.acceptSyncMessage,
		SessionStore:           store,
		DeferPendingUntilReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	firstPreamble := make(chan []capturedPublish, 1)
	secondPreamble := make(chan []capturedPublish, 1)
	brokerErrors := make(chan error, 2)
	go serveTestBroker(firstBroker, true, firstPreamble, brokerErrors)
	go serveTestBroker(secondBroker, false, secondPreamble, brokerErrors)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- manager.runClient(ctx, client) }()
	first := waitPreamble(t, firstPreamble, brokerErrors)
	assertPreamble(t, manager.topics, first, "1")

	secondNow := initialNow.Add(time.Second)
	clock.Store(secondNow.UnixNano())
	if err := store.SaveSnapshot(context.Background(), managerSnapshot(secondNow, 2)); err != nil {
		t.Fatalf("local state did not persist while MQTT was reconnecting: %v", err)
	}
	close(allowSecond)
	second := waitPreamble(t, secondPreamble, brokerErrors)
	assertPreamble(t, manager.topics, second, "2")

	cancel()
	_ = secondBroker.Close()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not stop")
	}
}

func TestMoreThan32QueuedSyncBeforeSUBACKDoesNotLivelockConnection(t *testing.T) {
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	source := uplink.Source{
		SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
	}
	epoch := "30000000-0000-4000-8000-000000000001"
	manager := New(
		config.BDM{}, source, "20000000-0000-4000-8000-000000000001", nil,
		func() time.Time { return now },
	)
	clientSide, brokerSide := net.Pipe()
	client, err := mqtt5.New(mqtt5.Config{
		ClientID:  "blk-0123456789abcdef0123456789abcdef",
		Username:  "blk-0123456789abcdef0123456789abcdef",
		KeepAlive: 30 * time.Second, SessionExpiry: 7 * 24 * 60 * 60,
		MaximumPacketSize: mqtt5.DefaultMaximumPacketSize, ReceiveMaximum: 20,
		SubscribeTopic: manager.topics.DownSync,
		Will: mqtt5.Will{
			Topic: manager.topics.Presence, Payload: []byte(`{"status":"OFFLINE"}`),
			QoS: 1, Retain: true,
		},
		DialContext:            func(context.Context) (net.Conn, error) { return clientSide, nil },
		ReconnectDelay:         func(int) time.Duration { return time.Millisecond },
		OnMessage:              manager.acceptSyncMessage,
		DeferPendingUntilReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var backlog [][]byte
	for revision := uint64(1); revision <= 64; revision++ {
		backlog = append(backlog, managerSyncJSON(
			t, source, epoch, revision, 0, now.Add(time.Duration(revision)*time.Millisecond),
		))
	}
	preamble := make(chan []capturedPublish, 1)
	brokerErrors := make(chan error, 1)
	go func() {
		header, _, err := readTestMQTTPacket(brokerSide)
		if err != nil || header != 0x10 {
			brokerErrors <- fmt.Errorf("expected CONNECT: header=%x err=%v", header, err)
			return
		}
		if err := writeTestMQTTPacket(brokerSide, 0x20, []byte{0x00, 0x00, 0x00}); err != nil {
			brokerErrors <- err
			return
		}
		header, subscribe, err := readTestMQTTPacket(brokerSide)
		if err != nil || header != 0x82 || len(subscribe) < 2 {
			brokerErrors <- fmt.Errorf("expected SUBSCRIBE: header=%x err=%v", header, err)
			return
		}
		subscriptionID := binary.BigEndian.Uint16(subscribe[:2])
		for index, body := range backlog {
			packetID := uint16(1000 + index)
			if err := writeTestInboundPublish(
				brokerSide, manager.topics.DownSync, packetID, body,
			); err != nil {
				brokerErrors <- err
				return
			}
			ackHeader, ackBody, err := readTestMQTTPacket(brokerSide)
			if err != nil || ackHeader != 0x40 || len(ackBody) < 2 ||
				binary.BigEndian.Uint16(ackBody[:2]) != packetID {
				brokerErrors <- fmt.Errorf(
					"queued Sync %d PUBACK header=%x body=%x err=%v",
					index, ackHeader, ackBody, err,
				)
				return
			}
		}
		suback := binary.BigEndian.AppendUint16(nil, subscriptionID)
		suback = append(suback, 0x00, 0x01)
		if err := writeTestMQTTPacket(brokerSide, 0x90, suback); err != nil {
			brokerErrors <- err
			return
		}
		var captured []capturedPublish
		for len(captured) < 1 {
			header, body, err := readTestMQTTPacket(brokerSide)
			if err != nil {
				brokerErrors <- err
				return
			}
			message, packetID, err := parseTestPublish(header, body)
			if err != nil {
				brokerErrors <- err
				return
			}
			captured = append(captured, message)
			if err := writeTestMQTTPacket(
				brokerSide, 0x40, binary.BigEndian.AppendUint16(nil, packetID),
			); err != nil {
				brokerErrors <- err
				return
			}
		}
		preamble <- captured
	}()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()
	select {
	case <-client.Connected():
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("client livelocked before Connected after queued Sync backlog")
	}
	if err := client.Publish(context.Background(), mqtt5.Message{
		Topic: manager.topics.Hello, Payload: []byte(`{"type":"block.hello"}`), QoS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	captured := waitPreamble(t, preamble, brokerErrors)
	if len(captured) != 1 || captured[0].topic != manager.topics.Hello {
		t.Fatalf("post-backlog publish = %+v", captured)
	}
	queued := manager.takeQueuedSync()
	if len(queued) != 1 {
		t.Fatalf("bounded latest-Sync mailbox count = %d", len(queued))
	}
	var latest uplink.Sync
	if err := json.Unmarshal(queued[0], &latest); err != nil {
		t.Fatal(err)
	}
	if latest.SyncRevision != "64" {
		t.Fatalf("queued Sync revision = %q, want 64", latest.SyncRevision)
	}
	_ = brokerSide.Close()
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop after queued-Sync test")
	}
}

func TestManagerHandlesLostSnapshotForPresentAndLostBrokerSessions(t *testing.T) {
	for _, sessionPresent := range []bool{true, false} {
		name := "session-lost"
		if sessionPresent {
			name = "session-present"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
			source := uplink.Source{
				SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001",
			}
			store, err := storage.OpenWithOptions(
				filepath.Join(t.TempDir(), "block.db"),
				func() time.Time { return now },
				storage.UplinkOptions{
					Enabled: true, Source: source,
					BootID:           "20000000-0000-4000-8000-000000000001",
					StreamGeneration: "1", StaleAfter: 5 * time.Second,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.SaveSnapshot(context.Background(), managerSnapshot(now, 1)); err != nil {
				t.Fatal(err)
			}
			manager := New(
				config.BDM{
					SoftwareVersion: "test", OSVersion: "Ubuntu", Architecture: "arm64",
					HardwareModel: "test",
				},
				source, "20000000-0000-4000-8000-000000000001", store,
				func() time.Time { return now },
			)
			firstClient, firstBroker := net.Pipe()
			secondClient, secondBroker := net.Pipe()
			connections := make(chan net.Conn, 2)
			connections <- firstClient
			connections <- secondClient
			client, err := mqtt5.New(mqtt5.Config{
				ClientID:  "blk-0123456789abcdef0123456789abcdef",
				Username:  "blk-0123456789abcdef0123456789abcdef",
				KeepAlive: 30 * time.Second, SessionExpiry: 7 * 24 * 60 * 60,
				MaximumPacketSize: mqtt5.DefaultMaximumPacketSize, ReceiveMaximum: 20,
				SubscribeTopic: manager.topics.DownSync,
				Will: mqtt5.Will{
					Topic: manager.topics.Presence, Payload: []byte(`{"status":"OFFLINE"}`),
					QoS: 1, Retain: true,
				},
				DialContext: func(ctx context.Context) (net.Conn, error) {
					select {
					case connection := <-connections:
						return connection, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
				ReconnectDelay:         func(int) time.Duration { return time.Millisecond },
				OnMessage:              manager.acceptSyncMessage,
				SessionStore:           store,
				DeferPendingUntilReady: true,
			})
			if err != nil {
				t.Fatal(err)
			}

			lostSnapshot := make(chan capturedPublish, 1)
			verified := make(chan struct{})
			brokerErrors := make(chan error, 2)
			go func() {
				if err := testBrokerHandshake(firstBroker); err != nil {
					brokerErrors <- err
					return
				}
				for index := 0; index < 4; index++ {
					header, body, err := readTestMQTTPacket(firstBroker)
					if err != nil {
						brokerErrors <- err
						return
					}
					message, packetID, err := parseTestPublish(header, body)
					if err != nil {
						brokerErrors <- err
						return
					}
					if index < 3 {
						if err := writeTestMQTTPacket(
							firstBroker, 0x40, binary.BigEndian.AppendUint16(nil, packetID),
						); err != nil {
							brokerErrors <- err
							return
						}
						continue
					}
					if message.topic != manager.topics.Snapshot || message.duplicate {
						brokerErrors <- fmt.Errorf("first snapshot transport = %+v", message)
						return
					}
					lostSnapshot <- message
					_ = firstBroker.Close()
				}
			}()
			go func() {
				if err := testBrokerHandshakeWithSessionPresent(secondBroker, sessionPresent); err != nil {
					brokerErrors <- err
					return
				}
				first := <-lostSnapshot
				var topics []string
				expected := 4
				if sessionPresent {
					expected = 4 // resumed snapshot, then ONLINE/Hello/SyncStatus
				}
				for index := 0; index < expected; index++ {
					header, body, err := readTestMQTTPacket(secondBroker)
					if err != nil {
						brokerErrors <- err
						return
					}
					message, packetID, err := parseTestPublish(header, body)
					if err != nil {
						brokerErrors <- err
						return
					}
					topics = append(topics, message.topic)
					if sessionPresent && index == 0 {
						if message.topic != manager.topics.Snapshot ||
							message.packetID != first.packetID || !message.duplicate {
							brokerErrors <- fmt.Errorf(
								"resumed snapshot = %+v, original = %+v", message, first,
							)
							return
						}
					} else if message.duplicate {
						brokerErrors <- fmt.Errorf("fresh connection preamble set DUP: %+v", message)
						return
					}
					if err := writeTestMQTTPacket(
						secondBroker, 0x40, binary.BigEndian.AppendUint16(nil, packetID),
					); err != nil {
						brokerErrors <- err
						return
					}
				}
				want := []string{
					manager.topics.Presence, manager.topics.Hello,
					manager.topics.SyncStatus, manager.topics.Snapshot,
				}
				if sessionPresent {
					want = []string{
						manager.topics.Snapshot, manager.topics.Presence,
						manager.topics.Hello, manager.topics.SyncStatus,
					}
				}
				if fmt.Sprint(topics) != fmt.Sprint(want) {
					brokerErrors <- fmt.Errorf("second-session topics = %v, want %v", topics, want)
					return
				}
				close(verified)
			}()

			ctx, cancel := context.WithCancel(context.Background())
			runDone := make(chan error, 1)
			go func() { runDone <- manager.runClient(ctx, client) }()
			select {
			case <-verified:
			case err := <-brokerErrors:
				t.Fatal(err)
			case <-time.After(4 * time.Second):
				t.Fatal("manager did not complete broker-session recovery")
			}
			_ = secondBroker.Close()
			cancel()
			select {
			case err := <-runDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("manager did not stop after recovery test")
			}
		})
	}
}

func TestMQTTInflightSurvivesProcessRestartForPresentAndLostSessions(t *testing.T) {
	for _, sessionPresent := range []bool{true, false} {
		name := "session-lost"
		if sessionPresent {
			name = "session-present"
		}
		t.Run(name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "block.db")
			store, err := storage.Open(databasePath, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if store != nil {
					_ = store.Close()
				}
			})
			principal := "blk-0123456789abcdef0123456789abcdef"
			downSync := "bdm/v1/sites/site-lab/blocks/block-001/down/sync"
			presence := "bdm/v1/sites/site-lab/blocks/block-001/up/presence"
			topic := "bdm/v1/sites/site-lab/blocks/block-001/up/hello"
			newClient := func(dial mqtt5.DialContextFunc) *mqtt5.Client {
				t.Helper()
				client, err := mqtt5.New(mqtt5.Config{
					ClientID: principal, Username: principal,
					KeepAlive: 30 * time.Second, SessionExpiry: 7 * 24 * 60 * 60,
					MaximumPacketSize: mqtt5.DefaultMaximumPacketSize, ReceiveMaximum: 20,
					SubscribeTopic: downSync,
					Will: mqtt5.Will{
						Topic: presence, Payload: []byte(`{"status":"OFFLINE"}`),
						QoS: 1, Retain: true,
					},
					DialContext: dial, ReconnectDelay: func(int) time.Duration { return time.Millisecond },
					SessionStore: store, DeferPendingUntilReady: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				return client
			}

			firstClientConn, firstBroker := net.Pipe()
			firstConnections := make(chan net.Conn, 1)
			firstConnections <- firstClientConn
			processA := newClient(func(ctx context.Context) (net.Conn, error) {
				select {
				case connection := <-firstConnections:
					return connection, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			firstWire := make(chan capturedPublish, 1)
			brokerErrors := make(chan error, 2)
			go func() {
				if err := testBrokerHandshake(firstBroker); err != nil {
					brokerErrors <- err
					return
				}
				header, body, err := readTestMQTTPacket(firstBroker)
				if err != nil {
					brokerErrors <- err
					return
				}
				message, _, err := parseTestPublish(header, body)
				if err != nil {
					brokerErrors <- err
					return
				}
				firstWire <- message
				_ = firstBroker.Close()
			}()
			ctxA, cancelA := context.WithCancel(context.Background())
			runA := make(chan error, 1)
			go func() { runA <- processA.Run(ctxA) }()
			<-processA.Connected()
			originalPayload := []byte(`{"type":"block.hello","process":"A"}`)
			publishA := make(chan error, 1)
			go func() {
				publishA <- processA.Publish(context.Background(), mqtt5.Message{
					Topic: topic, Payload: originalPayload, QoS: 1,
				})
			}()
			var original capturedPublish
			select {
			case original = <-firstWire:
			case err := <-brokerErrors:
				t.Fatal(err)
			case <-time.After(3 * time.Second):
				t.Fatal("process A did not write QoS1 packet")
			}
			if original.duplicate || string(original.payload) != string(originalPayload) {
				t.Fatalf("process A wire publish = %+v", original)
			}
			if err := <-publishA; !errors.Is(err, mqtt5.ErrDisconnected) {
				t.Fatalf("process A publish waiter error = %v", err)
			}
			cancelA()
			<-runA
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = nil
			store, err = storage.Open(databasePath, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := store.LoadMQTTInflight(context.Background())
			if err != nil || len(stored) != 1 ||
				stored[0].PacketID != original.packetID ||
				string(stored[0].Payload) != string(originalPayload) ||
				!stored[0].EverSent {
				t.Fatalf("durable process A in-flight = %+v, err=%v", stored, err)
			}

			secondClientConn, secondBroker := net.Pipe()
			processB := newClient(func(context.Context) (net.Conn, error) {
				return secondClientConn, nil
			})
			secondWire := make(chan capturedPublish, 1)
			gateChecked := make(chan struct{})
			go func() {
				if err := testBrokerHandshakeWithSessionPresent(secondBroker, sessionPresent); err != nil {
					brokerErrors <- err
					return
				}
				if !sessionPresent {
					_ = secondBroker.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
					if _, _, err := readTestMQTTPacket(secondBroker); err == nil {
						brokerErrors <- errors.New("process B replayed old packet after broker session loss")
						return
					} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
						brokerErrors <- err
						return
					}
					_ = secondBroker.SetReadDeadline(time.Time{})
				}
				close(gateChecked)
				header, body, err := readTestMQTTPacket(secondBroker)
				if err != nil {
					brokerErrors <- err
					return
				}
				message, packetID, err := parseTestPublish(header, body)
				if err != nil {
					brokerErrors <- err
					return
				}
				secondWire <- message
				if err := writeTestMQTTPacket(
					secondBroker, 0x40, binary.BigEndian.AppendUint16(nil, packetID),
				); err != nil {
					brokerErrors <- err
				}
			}()
			ctxB, cancelB := context.WithCancel(context.Background())
			runB := make(chan error, 1)
			go func() { runB <- processB.Run(ctxB) }()
			select {
			case <-processB.Connected():
			case err := <-brokerErrors:
				t.Fatal(err)
			case <-time.After(3 * time.Second):
				t.Fatal("process B did not connect")
			}
			select {
			case <-gateChecked:
			case err := <-brokerErrors:
				t.Fatal(err)
			case <-time.After(3 * time.Second):
				t.Fatal("process B broker gate was not checked")
			}
			var (
				freshPayload []byte
				publishDoneB chan error
			)
			if sessionPresent {
				if err := processB.ResumePending(processB.SessionID()); err != nil {
					t.Fatal(err)
				}
			} else {
				stored, err = store.LoadMQTTInflight(context.Background())
				if err != nil || len(stored) != 0 {
					t.Fatalf("lost broker session did not clear transport state: %+v, err=%v", stored, err)
				}
				freshPayload = []byte(`{"type":"block.hello","process":"B"}`)
				publishDoneB = make(chan error, 1)
				go func() {
					publishDoneB <- processB.Publish(context.Background(), mqtt5.Message{
						Topic: topic, Payload: freshPayload, QoS: 1,
					})
				}()
			}
			var recovered capturedPublish
			select {
			case recovered = <-secondWire:
			case err := <-brokerErrors:
				t.Fatal(err)
			case <-time.After(3 * time.Second):
				t.Fatal("process B did not publish after restart")
			}
			if sessionPresent {
				if recovered.packetID != original.packetID || !recovered.duplicate ||
					string(recovered.payload) != string(originalPayload) {
					t.Fatalf("process B resumed publish = %+v, original=%+v", recovered, original)
				}
			} else if recovered.packetID == original.packetID || recovered.duplicate ||
				string(recovered.payload) != string(freshPayload) {
				t.Fatalf("process B fresh publish = %+v, original=%+v", recovered, original)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				stored, err = store.LoadMQTTInflight(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if len(stored) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("PUBACK did not clear durable in-flight state: %+v", stored)
				}
				time.Sleep(time.Millisecond)
			}
			if publishDoneB != nil {
				select {
				case err := <-publishDoneB:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("fresh process B publish did not finish")
				}
			}
			cancelB()
			_ = secondBroker.Close()
			<-runB
		})
	}
}

type capturedPublish struct {
	topic     string
	payload   []byte
	retain    bool
	packetID  uint16
	duplicate bool
}

func serveTestBroker(
	connection net.Conn,
	closeAfterPreamble bool,
	preamble chan<- []capturedPublish,
	errs chan<- error,
) {
	if err := testBrokerHandshake(connection); err != nil {
		errs <- err
		return
	}
	var captured []capturedPublish
	for {
		header, body, err := readTestMQTTPacket(connection)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				errs <- err
			}
			return
		}
		switch header >> 4 {
		case 3:
			message, packetID, err := parseTestPublish(header, body)
			if err != nil {
				errs <- err
				return
			}
			captured = append(captured, message)
			if packetID != 0 {
				if err := writeTestMQTTPacket(
					connection, 0x40, binary.BigEndian.AppendUint16(nil, packetID),
				); err != nil {
					errs <- err
					return
				}
			}
			if len(captured) == 4 {
				preamble <- append([]capturedPublish{}, captured...)
				if closeAfterPreamble {
					_ = connection.Close()
					return
				}
			}
		case 14:
			return
		}
	}
}

func testBrokerHandshake(connection net.Conn) error {
	return testBrokerHandshakeWithSessionPresent(connection, false)
}

func testBrokerHandshakeWithSessionPresent(connection net.Conn, sessionPresent bool) error {
	header, _, err := readTestMQTTPacket(connection)
	if err != nil {
		return err
	}
	if header != 0x10 {
		return errors.New("expected CONNECT")
	}
	flags := byte(0)
	if sessionPresent {
		flags = 0x01
	}
	if err := writeTestMQTTPacket(connection, 0x20, []byte{flags, 0x00, 0x00}); err != nil {
		return err
	}
	header, body, err := readTestMQTTPacket(connection)
	if err != nil {
		return err
	}
	if header != 0x82 || len(body) < 2 {
		return errors.New("expected SUBSCRIBE")
	}
	subscriptionID := binary.BigEndian.Uint16(body[:2])
	suback := binary.BigEndian.AppendUint16(nil, subscriptionID)
	suback = append(suback, 0x00, 0x01)
	return writeTestMQTTPacket(connection, 0x90, suback)
}

func waitPreamble(
	t *testing.T,
	preamble <-chan []capturedPublish,
	errs <-chan error,
) []capturedPublish {
	t.Helper()
	select {
	case messages := <-preamble:
		return messages
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for MQTT preamble")
	}
	return nil
}

func assertPreamble(
	t *testing.T,
	topics uplink.Topics,
	messages []capturedPublish,
	wantRevision string,
) {
	t.Helper()
	wantTopics := []string{topics.Presence, topics.Hello, topics.SyncStatus, topics.Snapshot}
	if len(messages) != len(wantTopics) {
		t.Fatalf("preamble item count = %d", len(messages))
	}
	for index, want := range wantTopics {
		if messages[index].topic != want {
			t.Fatalf("preamble topic %d = %q, want %q", index, messages[index].topic, want)
		}
	}
	if !messages[0].retain {
		t.Fatal("ONLINE presence is not retained")
	}
	var hello struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(messages[1].payload, &hello); err != nil || hello.Type != "block.hello" {
		t.Fatalf("hello payload = %s, err=%v", messages[1].payload, err)
	}
	var snapshot struct {
		Type    string `json:"type"`
		Payload struct {
			Revision string `json:"revision"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(messages[3].payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Type != "device.snapshot" || snapshot.Payload.Revision != wantRevision {
		t.Fatalf("current snapshot = %+v", snapshot)
	}
}

func managerSnapshot(at time.Time, revision uint64) storage.SnapshotRecord {
	return storage.SnapshotRecord{
		State: state.Model{
			Revision: revision, UpdatedAt: at, Mode: "auto", Target: 100, Cycle: 10,
		},
		Meta: state.SourceMeta{
			SimulatorSessionID: "session-1", SampleSequence: revision,
			Quality: plccontract.QualityGood, PLCConnected: true, ReceivedAt: at,
		},
	}
}

func managerSyncJSON(
	t *testing.T,
	source uplink.Source,
	epoch string,
	revision uint64,
	highest uint64,
	issuedAt time.Time,
) []byte {
	t.Helper()
	message := uplink.Sync{
		SchemaVersion: uplink.SchemaVersion,
		SyncID:        fmt.Sprintf("40000000-0000-4000-8000-%012d", revision),
		SyncRevision:  uplink.FormatSequence(revision),
		Target: uplink.SyncTarget{
			SiteID: source.SiteID, BlockID: source.BlockID, DeviceID: source.DeviceID,
		},
		IssuedAt:                  uplink.FormatTime(issuedAt),
		Action:                    "ACK",
		StreamEpoch:               epoch,
		HighestContiguousSequence: uplink.FormatSequence(highest),
	}
	contents, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func readTestMQTTPacket(connection net.Conn) (byte, []byte, error) {
	var header [1]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		return 0, nil, err
	}
	remaining, err := readTestVarInt(connection)
	if err != nil {
		return 0, nil, err
	}
	body := make([]byte, remaining)
	if _, err := io.ReadFull(connection, body); err != nil {
		return 0, nil, err
	}
	return header[0], body, nil
}

func writeTestMQTTPacket(connection net.Conn, header byte, body []byte) error {
	frame := []byte{header}
	remaining := len(body)
	for {
		digit := byte(remaining % 128)
		remaining /= 128
		if remaining > 0 {
			digit |= 0x80
		}
		frame = append(frame, digit)
		if remaining == 0 {
			break
		}
	}
	frame = append(frame, body...)
	for len(frame) > 0 {
		written, err := connection.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[written:]
	}
	return nil
}

func writeTestInboundPublish(
	connection net.Conn,
	topic string,
	packetID uint16,
	payload []byte,
) error {
	if len(topic) > 65535 {
		return errors.New("test MQTT topic is too long")
	}
	body := binary.BigEndian.AppendUint16(nil, uint16(len(topic)))
	body = append(body, topic...)
	body = binary.BigEndian.AppendUint16(body, packetID)
	body = append(body, 0x00)
	body = append(body, payload...)
	return writeTestMQTTPacket(connection, 0x32, body)
}

func readTestVarInt(reader io.Reader) (int, error) {
	value, multiplier := 0, 1
	for count := 0; count < 4; count++ {
		var digit [1]byte
		if _, err := io.ReadFull(reader, digit[:]); err != nil {
			return 0, err
		}
		value += int(digit[0]&0x7f) * multiplier
		if digit[0]&0x80 == 0 {
			return value, nil
		}
		multiplier *= 128
	}
	return 0, errors.New("invalid MQTT variable integer")
}

func parseTestPublish(header byte, body []byte) (capturedPublish, uint16, error) {
	if len(body) < 2 {
		return capturedPublish{}, 0, io.ErrUnexpectedEOF
	}
	topicLength := int(binary.BigEndian.Uint16(body[:2]))
	if len(body) < 2+topicLength {
		return capturedPublish{}, 0, io.ErrUnexpectedEOF
	}
	message := capturedPublish{
		topic: string(body[2 : 2+topicLength]), retain: header&0x01 != 0,
		duplicate: header&0x08 != 0,
	}
	rest := body[2+topicLength:]
	var packetID uint16
	if (header>>1)&0x03 == 1 {
		if len(rest) < 2 {
			return capturedPublish{}, 0, io.ErrUnexpectedEOF
		}
		packetID = binary.BigEndian.Uint16(rest[:2])
		message.packetID = packetID
		rest = rest[2:]
	}
	propertyLength, consumed, err := readTestVarIntBytes(rest)
	if err != nil || len(rest) < consumed+propertyLength {
		return capturedPublish{}, 0, io.ErrUnexpectedEOF
	}
	message.payload = append([]byte{}, rest[consumed+propertyLength:]...)
	return message, packetID, nil
}

func readTestVarIntBytes(contents []byte) (int, int, error) {
	value, multiplier := 0, 1
	for index := 0; index < len(contents) && index < 4; index++ {
		value += int(contents[index]&0x7f) * multiplier
		if contents[index]&0x80 == 0 {
			return value, index + 1, nil
		}
		multiplier *= 128
	}
	return 0, 0, errors.New("invalid MQTT variable integer")
}
