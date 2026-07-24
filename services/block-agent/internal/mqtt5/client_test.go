package mqtt5

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

func TestClientReconnectReusesQoS1PacketIDWithDUP(t *testing.T) {
	clientConnections := make(chan net.Conn, 2)
	firstClient, firstBroker := net.Pipe()
	secondClient, secondBroker := net.Pipe()
	clientConnections <- firstClient
	clientConnections <- secondClient

	principal := "blk-0123456789abcdef0123456789abcdef"
	client, err := New(testConfig(principal, func(ctx context.Context) (net.Conn, error) {
		select {
		case connection := <-clientConnections:
			return connection, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	firstPublish := make(chan uint16, 1)
	secondPublish := make(chan uint16, 1)
	brokerErrors := make(chan error, 2)
	go func() {
		if err := brokerHandshake(firstBroker, principal); err != nil {
			brokerErrors <- err
			return
		}
		value, err := readPacket(firstBroker, DefaultMaximumPacketSize)
		if err != nil {
			brokerErrors <- err
			return
		}
		_, packetID, err := parsePublish(value)
		if err != nil {
			brokerErrors <- err
			return
		}
		if value.header&0x08 != 0 {
			brokerErrors <- errors.New("first QoS1 publish incorrectly set DUP")
			return
		}
		firstPublish <- packetID
		_ = firstBroker.Close() // lose transport acknowledgement
	}()
	go func() {
		if err := brokerHandshakeWithSessionPresent(secondBroker, principal, true); err != nil {
			brokerErrors <- err
			return
		}
		value, err := readPacket(secondBroker, DefaultMaximumPacketSize)
		if err != nil {
			brokerErrors <- err
			return
		}
		_, packetID, err := parsePublish(value)
		if err != nil {
			brokerErrors <- err
			return
		}
		if value.header&0x08 == 0 {
			brokerErrors <- errors.New("reconnected QoS1 publish did not set DUP")
			return
		}
		secondPublish <- packetID
		properties := []byte{0x1f, 0x00, 0x02, 'o', 'k'}
		body := binary.BigEndian.AppendUint16(nil, packetID)
		body = append(body, 0x00, byte(len(properties)))
		body = append(body, properties...)
		if err := writePacket(secondBroker, 0x40, body, DefaultMaximumPacketSize); err != nil {
			brokerErrors <- err
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()
	select {
	case <-client.Connected():
	case <-time.After(2 * time.Second):
		t.Fatal("client did not establish first MQTT session")
	}
	publishDone := make(chan error, 1)
	go func() {
		publishDone <- client.Publish(ctx, Message{
			Topic:   "bdm/v1/sites/site-lab/blocks/block-001/up/snapshot",
			Payload: []byte(`{"messageId":"one"}`), QoS: 1,
		})
	}()
	var firstID, secondID uint16
	select {
	case firstID = <-firstPublish:
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("broker did not receive first publish")
	}
	select {
	case secondID = <-secondPublish:
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("broker did not receive reconnected publish")
	}
	if firstID == 0 || secondID != firstID {
		t.Fatalf("packet id changed across reconnect: %d -> %d", firstID, secondID)
	}
	select {
	case err := <-publishDone:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("disconnected publish waiter error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("QoS1 publish waiter did not detach on disconnect")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		client.mu.Lock()
		_, pending := client.pending[firstID]
		client.mu.Unlock()
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resumed QoS1 publish did not clear after PUBACK")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	_ = secondBroker.Close()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client Run did not stop")
	}
}

func TestDeferredPendingResumesOnlyAfterGateWhenBrokerSessionPresent(t *testing.T) {
	firstClient, firstBroker := net.Pipe()
	secondClient, secondBroker := net.Pipe()
	connections := make(chan net.Conn, 2)
	connections <- firstClient
	connections <- secondClient
	principal := "blk-0123456789abcdef0123456789abcdef"
	config := testConfig(principal, func(ctx context.Context) (net.Conn, error) {
		select {
		case connection := <-connections:
			return connection, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	config.DeferPendingUntilReady = true
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	firstID := make(chan uint16, 1)
	brokerErrors := make(chan error, 2)
	go func() {
		if err := brokerHandshake(firstBroker, principal); err != nil {
			brokerErrors <- err
			return
		}
		value, err := readPacket(firstBroker, DefaultMaximumPacketSize)
		if err != nil {
			brokerErrors <- err
			return
		}
		_, packetID, err := parsePublish(value)
		if err != nil {
			brokerErrors <- err
			return
		}
		firstID <- packetID
		_ = firstBroker.Close()
	}()
	secondID := make(chan uint16, 1)
	allowResume := make(chan struct{})
	go func() {
		if err := brokerHandshakeWithSessionPresent(secondBroker, principal, true); err != nil {
			brokerErrors <- err
			return
		}
		_ = secondBroker.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		if _, err := readPacket(secondBroker, DefaultMaximumPacketSize); err == nil {
			brokerErrors <- errors.New("pending publish crossed the application connection gate")
			return
		} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
			brokerErrors <- err
			return
		}
		_ = secondBroker.SetReadDeadline(time.Time{})
		close(allowResume)
		value, err := readPacket(secondBroker, DefaultMaximumPacketSize)
		if err != nil {
			brokerErrors <- err
			return
		}
		_, packetID, err := parsePublish(value)
		if err != nil {
			brokerErrors <- err
			return
		}
		if value.header&0x08 == 0 {
			brokerErrors <- errors.New("resumed pending publish did not set DUP")
			return
		}
		secondID <- packetID
		if err := writePacket(
			secondBroker, 0x40, binary.BigEndian.AppendUint16(nil, packetID),
			DefaultMaximumPacketSize,
		); err != nil {
			brokerErrors <- err
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()
	<-client.Connected()
	publishDone := make(chan error, 1)
	go func() {
		publishDone <- client.Publish(context.Background(), Message{
			Topic:   "bdm/v1/sites/site-lab/blocks/block-001/up/snapshot",
			Payload: []byte(`{"messageId":"deferred"}`), QoS: 1,
			ApplicationKey: "outbox:deferred",
		})
	}()
	var originalID uint16
	select {
	case originalID = <-firstID:
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("first broker did not receive QoS1 publish")
	}
	select {
	case err := <-publishDone:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("publish waiter error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publish waiter did not detach after disconnect")
	}
	select {
	case <-client.Connected():
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("second MQTT session was not established")
	}
	sessionID := client.SessionID()
	if !client.SessionPresent(sessionID) {
		t.Fatal("client did not expose resumed broker session")
	}
	select {
	case <-allowResume:
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("broker could not verify deferred gate")
	}
	if err := client.Publish(context.Background(), Message{
		Topic: "bdm/v1/sites/site-lab/blocks/block-001/up/heartbeat",
		QoS:   0, Payload: []byte(`{"type":"block.heartbeat"}`),
	}); !errors.Is(err, ErrPendingResumeRequired) {
		t.Fatalf("new publish before pending resume error = %v", err)
	}
	if err := client.ResumePending(sessionID); err != nil {
		t.Fatal(err)
	}
	var resumedID uint16
	select {
	case resumedID = <-secondID:
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("pending publish was not resumed")
	}
	if resumedID != originalID {
		t.Fatalf("resumed packet id changed: %d -> %d", originalID, resumedID)
	}
	deadline := time.Now().Add(2 * time.Second)
	for client.HasPendingApplicationKey("outbox:deferred") {
		if time.Now().After(deadline) {
			t.Fatal("resumed pending publish did not clear after PUBACK")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	_ = secondBroker.Close()
	<-runDone
}

func TestBrokerSessionLossDiscardsOldPendingAndFreshPublishHasNoDUP(t *testing.T) {
	firstClient, firstBroker := net.Pipe()
	secondClient, secondBroker := net.Pipe()
	connections := make(chan net.Conn, 2)
	connections <- firstClient
	connections <- secondClient
	principal := "blk-0123456789abcdef0123456789abcdef"
	config := testConfig(principal, func(ctx context.Context) (net.Conn, error) {
		select {
		case connection := <-connections:
			return connection, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	config.DeferPendingUntilReady = true
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	oldSeen := make(chan struct{})
	brokerErrors := make(chan error, 2)
	go func() {
		if err := brokerHandshake(firstBroker, principal); err != nil {
			brokerErrors <- err
			return
		}
		if _, err := readPacket(firstBroker, DefaultMaximumPacketSize); err != nil {
			brokerErrors <- err
			return
		}
		close(oldSeen)
		_ = firstBroker.Close()
	}()
	freshSeen := make(chan struct{})
	go func() {
		if err := brokerHandshakeWithSessionPresent(secondBroker, principal, false); err != nil {
			brokerErrors <- err
			return
		}
		_ = secondBroker.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		if _, err := readPacket(secondBroker, DefaultMaximumPacketSize); err == nil {
			brokerErrors <- errors.New("lost-session pending publish was retransmitted")
			return
		} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
			brokerErrors <- err
			return
		}
		_ = secondBroker.SetReadDeadline(time.Time{})
		close(freshSeen)
		value, err := readPacket(secondBroker, DefaultMaximumPacketSize)
		if err != nil {
			brokerErrors <- err
			return
		}
		_, packetID, err := parsePublish(value)
		if err != nil {
			brokerErrors <- err
			return
		}
		if value.header&0x08 != 0 {
			brokerErrors <- errors.New("fresh publish after session loss set DUP")
			return
		}
		brokerErrors <- writePacket(
			secondBroker, 0x40, binary.BigEndian.AppendUint16(nil, packetID),
			DefaultMaximumPacketSize,
		)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()
	<-client.Connected()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.Publish(context.Background(), Message{
			Topic:   "bdm/v1/sites/site-lab/blocks/block-001/up/snapshot",
			Payload: []byte(`{"messageId":"lost"}`), QoS: 1,
			ApplicationKey: "outbox:lost",
		})
	}()
	select {
	case <-oldSeen:
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("first broker did not receive pending publish")
	}
	if err := <-firstDone; !errors.Is(err, ErrDisconnected) {
		t.Fatalf("lost transport waiter error = %v", err)
	}
	select {
	case <-client.Connected():
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("new broker session was not established")
	}
	sessionID := client.SessionID()
	if client.SessionPresent(sessionID) {
		t.Fatal("broker session unexpectedly reported present")
	}
	if client.HasPendingApplicationKey("outbox:lost") {
		t.Fatal("old MQTT pending packet survived broker session loss")
	}
	if err := client.ResumePending(sessionID); !errors.Is(err, ErrSessionLost) {
		t.Fatalf("resume on lost broker session error = %v", err)
	}
	select {
	case <-freshSeen:
	case err := <-brokerErrors:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("broker could not verify old pending was discarded")
	}
	if err := client.Publish(context.Background(), Message{
		Topic:   "bdm/v1/sites/site-lab/blocks/block-001/up/snapshot",
		Payload: []byte(`{"messageId":"fresh"}`), QoS: 1,
		ApplicationKey: "outbox:fresh",
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-brokerErrors; err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = secondBroker.Close()
	<-runDone
}

func TestControlQoS1ResumesWithOriginalPacketIDAndDUP(t *testing.T) {
	clientConnections := make(chan net.Conn, 2)
	firstClient, firstBroker := net.Pipe()
	secondClient, secondBroker := net.Pipe()
	clientConnections <- firstClient
	clientConnections <- secondClient
	principal := "blk-0123456789abcdef0123456789abcdef"
	config := testConfig(principal, func(ctx context.Context) (net.Conn, error) {
		select {
		case connection := <-clientConnections:
			return connection, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	config.DeferPendingUntilReady = true
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	firstSeen := make(chan uint16, 1)
	secondResult := make(chan error, 1)
	go func() {
		if err := brokerHandshake(firstBroker, principal); err != nil {
			secondResult <- err
			return
		}
		value, err := readPacket(firstBroker, DefaultMaximumPacketSize)
		if err != nil {
			secondResult <- err
			return
		}
		_, packetID, err := parsePublish(value)
		if err != nil {
			secondResult <- err
			return
		}
		firstSeen <- packetID
		_ = firstBroker.Close()
	}()
	go func() {
		if err := brokerHandshakeWithSessionPresent(secondBroker, principal, true); err != nil {
			secondResult <- err
			return
		}
		value, err := readPacket(secondBroker, DefaultMaximumPacketSize)
		if err != nil {
			secondResult <- err
			return
		}
		_, packetID, err := parsePublish(value)
		if err != nil {
			secondResult <- err
			return
		}
		if value.header&0x08 == 0 {
			secondResult <- errors.New("resumed control QoS1 did not set DUP")
			return
		}
		originalID := <-firstSeen
		if packetID != originalID {
			secondResult <- fmt.Errorf("control packet id changed: %d -> %d", originalID, packetID)
			return
		}
		secondResult <- writePacket(
			secondBroker, 0x40, binary.BigEndian.AppendUint16(nil, packetID),
			DefaultMaximumPacketSize,
		)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()
	<-client.Connected()
	publishDone := make(chan error, 1)
	go func() {
		publishDone <- client.Publish(context.Background(), Message{
			Topic:   "bdm/v1/sites/site-lab/blocks/block-001/up/hello",
			Payload: []byte(`{"type":"block.hello"}`), QoS: 1,
		})
	}()
	var originalID uint16
	select {
	case originalID = <-firstSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("control publish was not sent")
	}
	firstSeen <- originalID
	select {
	case err := <-publishDone:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("lost control publish waiter error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lost control publish waiter did not detach")
	}
	select {
	case <-client.Connected():
	case <-time.After(2 * time.Second):
		t.Fatal("client did not establish resumed session")
	}
	if err := client.ResumePending(client.SessionID()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-secondResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("control publish was not resumed")
	}
	cancel()
	_ = secondBroker.Close()
	<-runDone
}

func TestClientAcceptsOnlyExactDownSyncAndPUBACKsInboundQoS1(t *testing.T) {
	clientSide, brokerSide := net.Pipe()
	principal := "blk-0123456789abcdef0123456789abcdef"
	messages := make(chan Message, 1)
	client, err := New(func() Config {
		config := testConfig(principal, func(context.Context) (net.Conn, error) {
			return clientSide, nil
		})
		config.OnMessage = func(message Message) error {
			messages <- message
			return nil
		}
		return config
	}())
	if err != nil {
		t.Fatal(err)
	}
	brokerDone := make(chan error, 1)
	go func() {
		if err := brokerHandshake(brokerSide, principal); err != nil {
			brokerDone <- err
			return
		}
		body, _ := appendUTF(nil, "bdm/v1/sites/site-lab/blocks/block-001/down/sync")
		body = binary.BigEndian.AppendUint16(body, 44)
		body = append(body, 0x00)
		body = append(body, []byte(`{"action":"ACK"}`)...)
		// Fragment every byte to prove the live read loop is stream-safe.
		var wire bytes.Buffer
		if err := writePacket(&wire, 0x32, body, DefaultMaximumPacketSize); err != nil {
			brokerDone <- err
			return
		}
		for _, octet := range wire.Bytes() {
			if _, err := brokerSide.Write([]byte{octet}); err != nil {
				brokerDone <- err
				return
			}
		}
		ack, err := readPacket(brokerSide, DefaultMaximumPacketSize)
		if err != nil {
			brokerDone <- err
			return
		}
		if ack.header != 0x40 || len(ack.body) != 2 || binary.BigEndian.Uint16(ack.body) != 44 {
			brokerDone <- errors.New("client sent malformed inbound PUBACK")
			return
		}
		brokerDone <- nil
	}()
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()
	select {
	case message := <-messages:
		if string(message.Payload) != `{"action":"ACK"}` || message.QoS != 1 {
			t.Fatalf("inbound message = %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("down/sync message not delivered")
	}
	if err := <-brokerDone; err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = brokerSide.Close()
	<-runDone
}

func TestKeepAliveRequiresPINGRESP(t *testing.T) {
	clientSide, brokerSide := net.Pipe()
	principal := "blk-0123456789abcdef0123456789abcdef"
	config := testConfig(principal, func(context.Context) (net.Conn, error) { return clientSide, nil })
	config.KeepAlive = time.Second
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	pingSeen := make(chan error, 1)
	go func() {
		if err := brokerHandshake(brokerSide, principal); err != nil {
			pingSeen <- err
			return
		}
		value, err := readPacket(brokerSide, DefaultMaximumPacketSize)
		if err != nil {
			pingSeen <- err
			return
		}
		if value.header != 0xc0 || len(value.body) != 0 {
			pingSeen <- errors.New("expected PINGREQ")
			return
		}
		pingSeen <- writePacket(brokerSide, 0xd0, nil, DefaultMaximumPacketSize)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case err := <-pingSeen:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PINGREQ was not emitted")
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	_ = brokerSide.Close()
	<-done
}

func TestConnectIsMQTT5PersistentSessionWithExpectedIdentity(t *testing.T) {
	principal := "blk-0123456789abcdef0123456789abcdef"
	client, err := New(testConfig(principal, func(context.Context) (net.Conn, error) {
		return nil, errors.New("unused")
	}))
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.connectBody()
	if err != nil {
		t.Fatal(err)
	}
	protocol, rest, err := consumeUTF(body)
	if err != nil || protocol != "MQTT" || len(rest) < 4 {
		t.Fatalf("CONNECT protocol = %q, err=%v", protocol, err)
	}
	if rest[0] != 5 {
		t.Fatalf("protocol level = %d", rest[0])
	}
	flags := rest[1]
	if flags&0x02 != 0 {
		t.Fatal("Clean Start must be false")
	}
	if flags&0x80 == 0 || flags&0x04 == 0 || flags&0x18 != 0x08 || flags&0x20 == 0 {
		t.Fatalf("CONNECT flags = 0x%02x", flags)
	}
	if binary.BigEndian.Uint16(rest[2:4]) != 30 {
		t.Fatalf("KeepAlive = %d", binary.BigEndian.Uint16(rest[2:4]))
	}
}

func TestDurableQoS1StateSurvivesClientRestart(t *testing.T) {
	for _, sessionPresent := range []bool{true, false} {
		name := "broker-session-lost"
		if sessionPresent {
			name = "broker-session-present"
		}
		t.Run(name, func(t *testing.T) {
			principal := "blk-0123456789abcdef0123456789abcdef"
			topic := "bdm/v1/sites/site-lab/blocks/block-001/up/hello"
			originalPayload := []byte{0x00, 0xff, '{', '"', 'p', '"', ':', '1', '}'}
			store := &memorySessionStore{}

			firstClientConnection, firstBroker := net.Pipe()
			firstConnections := make(chan net.Conn, 1)
			firstConnections <- firstClientConnection
			firstConfig := testConfig(principal, func(ctx context.Context) (net.Conn, error) {
				select {
				case connection := <-firstConnections:
					return connection, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			firstConfig.DeferPendingUntilReady = true
			firstConfig.SessionStore = store
			processA, err := New(firstConfig)
			if err != nil {
				t.Fatal(err)
			}
			firstWire := make(chan observedPublish, 1)
			firstSubscriptionID := make(chan uint16, 1)
			brokerErrors := make(chan error, 2)
			go func() {
				subscriptionID, err := brokerHandshakeWithSessionPresentAndSubscriptionID(
					firstBroker, principal, false,
				)
				if err != nil {
					brokerErrors <- err
					return
				}
				firstSubscriptionID <- subscriptionID
				value, err := readPacket(firstBroker, DefaultMaximumPacketSize)
				if err != nil {
					brokerErrors <- err
					return
				}
				message, packetID, err := parsePublish(value)
				if err != nil {
					brokerErrors <- err
					return
				}
				firstWire <- observedPublish{
					message: message, packetID: packetID, duplicate: value.header&0x08 != 0,
				}
				_ = firstBroker.Close()
			}()

			runContextA, cancelRunA := context.WithCancel(context.Background())
			runDoneA := make(chan error, 1)
			go func() { runDoneA <- processA.Run(runContextA) }()
			waitConnected(t, processA)
			publishDoneA := make(chan error, 1)
			go func() {
				publishDoneA <- processA.Publish(context.Background(), Message{
					Topic: topic, Payload: originalPayload, QoS: 1,
					ApplicationKey: "control:process-a",
				})
			}()
			var original observedPublish
			select {
			case original = <-firstWire:
			case err := <-brokerErrors:
				t.Fatal(err)
			case <-time.After(2 * time.Second):
				t.Fatal("process A publish was not observed")
			}
			firstSubID := <-firstSubscriptionID
			if firstSubID == original.packetID {
				t.Fatalf("process A SUBSCRIBE reused PUBLISH packet id %d", original.packetID)
			}
			if original.duplicate || !bytes.Equal(original.message.Payload, originalPayload) {
				t.Fatalf("process A publish = %+v", original)
			}
			select {
			case err := <-publishDoneA:
				if !errors.Is(err, ErrDisconnected) {
					t.Fatalf("process A publish waiter error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("process A publish waiter did not detach after crash")
			}
			cancelRunA()
			select {
			case err := <-runDoneA:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("process A did not stop")
			}
			stored := store.snapshot(t)
			if len(stored) != 1 || stored[0].PacketID != original.packetID ||
				!bytes.Equal(stored[0].Payload, originalPayload) || !stored[0].EverSent {
				t.Fatalf("process A durable state = %+v", stored)
			}

			secondClientConnection, secondBroker := net.Pipe()
			secondConnections := make(chan net.Conn, 1)
			secondConnections <- secondClientConnection
			secondConfig := testConfig(principal, func(ctx context.Context) (net.Conn, error) {
				select {
				case connection := <-secondConnections:
					return connection, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			secondConfig.DeferPendingUntilReady = true
			secondConfig.SessionStore = store
			processB, err := New(secondConfig)
			if err != nil {
				t.Fatal(err)
			}
			secondWire := make(chan observedPublish, 1)
			secondSubscriptionID := make(chan uint16, 1)
			gateChecked := make(chan struct{})
			go func() {
				subscriptionID, err := brokerHandshakeWithSessionPresentAndSubscriptionID(
					secondBroker, principal, sessionPresent,
				)
				if err != nil {
					brokerErrors <- err
					return
				}
				secondSubscriptionID <- subscriptionID
				_ = secondBroker.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
				if _, err := readPacket(secondBroker, DefaultMaximumPacketSize); err == nil {
					brokerErrors <- errors.New("publish crossed the explicit restart recovery gate")
					return
				} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
					brokerErrors <- err
					return
				}
				_ = secondBroker.SetReadDeadline(time.Time{})
				close(gateChecked)
				value, err := readPacket(secondBroker, DefaultMaximumPacketSize)
				if err != nil {
					brokerErrors <- err
					return
				}
				message, packetID, err := parsePublish(value)
				if err != nil {
					brokerErrors <- err
					return
				}
				secondWire <- observedPublish{
					message: message, packetID: packetID, duplicate: value.header&0x08 != 0,
				}
				if err := writePacket(
					secondBroker, 0x40, binary.BigEndian.AppendUint16(nil, packetID),
					DefaultMaximumPacketSize,
				); err != nil {
					brokerErrors <- err
				}
			}()

			runContextB, cancelRunB := context.WithCancel(context.Background())
			runDoneB := make(chan error, 1)
			go func() { runDoneB <- processB.Run(runContextB) }()
			waitConnected(t, processB)
			select {
			case <-gateChecked:
			case err := <-brokerErrors:
				t.Fatal(err)
			case <-time.After(2 * time.Second):
				t.Fatal("restart recovery gate was not checked")
			}
			secondSubID := <-secondSubscriptionID
			if secondSubID == original.packetID {
				t.Fatalf("process B SUBSCRIBE reused stored PUBLISH packet id %d", original.packetID)
			}
			var (
				freshPayload = []byte(`{"process":"B","fresh":true}`)
				publishDoneB chan error
			)
			if sessionPresent {
				if err := processB.ResumePending(processB.SessionID()); err != nil {
					t.Fatal(err)
				}
			} else {
				if persisted := store.snapshot(t); len(persisted) != 0 {
					t.Fatalf("broker session loss did not clear durable state: %+v", persisted)
				}
				publishDoneB = make(chan error, 1)
				go func() {
					publishDoneB <- processB.Publish(context.Background(), Message{
						Topic: topic, Payload: freshPayload, QoS: 1,
						ApplicationKey: "control:process-b",
					})
				}()
			}
			var recovered observedPublish
			select {
			case recovered = <-secondWire:
			case err := <-brokerErrors:
				t.Fatal(err)
			case <-time.After(2 * time.Second):
				t.Fatal("process B publish was not observed")
			}
			if sessionPresent {
				if recovered.packetID != original.packetID || !recovered.duplicate ||
					!bytes.Equal(recovered.message.Payload, originalPayload) {
					t.Fatalf("resumed process B publish = %+v, original=%+v", recovered, original)
				}
			} else if recovered.packetID == original.packetID || recovered.duplicate ||
				!bytes.Equal(recovered.message.Payload, freshPayload) {
				t.Fatalf("fresh process B publish = %+v, original=%+v", recovered, original)
			}
			if publishDoneB != nil {
				select {
				case err := <-publishDoneB:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("fresh process B publish did not receive PUBACK")
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for len(store.snapshot(t)) != 0 {
				if time.Now().After(deadline) {
					t.Fatalf("PUBACK did not delete durable state: %+v", store.snapshot(t))
				}
				time.Sleep(time.Millisecond)
			}
			cancelRunB()
			_ = secondBroker.Close()
			select {
			case err := <-runDoneB:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("process B did not stop")
			}
		})
	}
}

func TestSessionStoreSaveCancellationDoesNotWriteSocket(t *testing.T) {
	clientSide, brokerSide := net.Pipe()
	principal := "blk-0123456789abcdef0123456789abcdef"
	store := &memorySessionStore{saveErr: context.Canceled}
	config := testConfig(principal, func(context.Context) (net.Conn, error) {
		return clientSide, nil
	})
	config.SessionStore = store
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	noPublish := make(chan error, 1)
	go func() {
		if err := brokerHandshake(brokerSide, principal); err != nil {
			noPublish <- err
			return
		}
		_ = brokerSide.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := readPacket(brokerSide, DefaultMaximumPacketSize); err == nil {
			noPublish <- errors.New("PUBLISH reached socket after durable save failed")
		} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
			noPublish <- err
		} else {
			noPublish <- nil
		}
	}()
	runContext, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(runContext) }()
	waitConnected(t, client)
	err = client.Publish(context.Background(), Message{
		Topic:   "bdm/v1/sites/site-lab/blocks/block-001/up/hello",
		Payload: []byte(`{"not":"written"}`), QoS: 1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("durable save error = %v", err)
	}
	if err := <-noPublish; err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	pending := len(client.pending)
	client.mu.Unlock()
	if pending != 0 || len(store.snapshot(t)) != 0 {
		t.Fatalf("failed durable save left pending=%d stored=%+v", pending, store.snapshot(t))
	}
	cancelRun()
	_ = brokerSide.Close()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not stop")
	}
}

type observedPublish struct {
	message   Message
	packetID  uint16
	duplicate bool
}

type memorySessionStore struct {
	mu      sync.Mutex
	records map[uint16]StoredPublish
	saveErr error
}

func (s *memorySessionStore) LoadMQTTInflight(ctx context.Context) ([]StoredPublish, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]StoredPublish, 0, len(s.records))
	for _, record := range s.records {
		record.Payload = append([]byte{}, record.Payload...)
		records = append(records, record)
	}
	return records, nil
}

func (s *memorySessionStore) SaveMQTTInflight(ctx context.Context, record StoredPublish) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	if s.records == nil {
		s.records = make(map[uint16]StoredPublish)
	}
	if _, exists := s.records[record.PacketID]; exists {
		return errors.New("duplicate durable MQTT packet id")
	}
	record.Payload = append([]byte{}, record.Payload...)
	s.records[record.PacketID] = record
	return nil
}

func (s *memorySessionStore) DeleteMQTTInflight(ctx context.Context, packetID uint16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[packetID]; !exists {
		return errors.New("durable MQTT packet id is missing")
	}
	delete(s.records, packetID)
	return nil
}

func (s *memorySessionStore) ClearMQTTInflight(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.records = make(map[uint16]StoredPublish)
	s.mu.Unlock()
	return nil
}

func (s *memorySessionStore) snapshot(t *testing.T) []StoredPublish {
	t.Helper()
	records, err := s.LoadMQTTInflight(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func waitConnected(t *testing.T, client *Client) {
	t.Helper()
	select {
	case <-client.Connected():
	case <-time.After(2 * time.Second):
		t.Fatal("MQTT client did not connect")
	}
}

func testConfig(principal string, dial DialContextFunc) Config {
	return Config{
		ClientID: principal, Username: principal, KeepAlive: 30 * time.Second,
		SessionExpiry: 7 * 24 * 60 * 60, MaximumPacketSize: DefaultMaximumPacketSize,
		ReceiveMaximum: 20,
		SubscribeTopic: "bdm/v1/sites/site-lab/blocks/block-001/down/sync",
		Will: Will{
			Topic:   "bdm/v1/sites/site-lab/blocks/block-001/up/presence",
			Payload: []byte(`{"status":"OFFLINE","reason":"LWT","bootId":"20000000-0000-4000-8000-000000000001"}`),
			QoS:     1, Retain: true,
		},
		DialContext:    dial,
		ReconnectDelay: func(int) time.Duration { return time.Millisecond },
	}
}

func brokerHandshake(connection net.Conn, principal string) error {
	return brokerHandshakeWithSessionPresent(connection, principal, false)
}

func brokerHandshakeWithSessionPresent(connection net.Conn, principal string, sessionPresent bool) error {
	_, err := brokerHandshakeWithSessionPresentAndSubscriptionID(
		connection, principal, sessionPresent,
	)
	return err
}

func brokerHandshakeWithSessionPresentAndSubscriptionID(
	connection net.Conn,
	principal string,
	sessionPresent bool,
) (uint16, error) {
	connect, err := readPacket(connection, DefaultMaximumPacketSize)
	if err != nil {
		return 0, err
	}
	if connect.header != 0x10 {
		return 0, errors.New("broker expected CONNECT")
	}
	acknowledgeFlags := byte(0)
	if sessionPresent {
		acknowledgeFlags = 1
	}
	if err := writePacket(connection, 0x20, []byte{acknowledgeFlags, 0x00, 0x03, 0x13, 0x00, 0x1e}, DefaultMaximumPacketSize); err != nil {
		return 0, err
	}
	subscribe, err := readPacket(connection, DefaultMaximumPacketSize)
	if err != nil {
		return 0, err
	}
	if subscribe.header != 0x82 || len(subscribe.body) < 2 {
		return 0, errors.New("broker expected SUBSCRIBE")
	}
	packetID := binary.BigEndian.Uint16(subscribe.body[:2])
	properties := []byte{0x1f, 0x00, 0x02, 'o', 'k'}
	body := binary.BigEndian.AppendUint16(nil, packetID)
	body = append(body, byte(len(properties)))
	body = append(body, properties...)
	body = append(body, 0x01)
	_ = principal
	return packetID, writePacket(connection, 0x90, body, DefaultMaximumPacketSize)
}

func TestNewRejectsIdentityMismatchAndPlainMissingWill(t *testing.T) {
	config := testConfig("blk-0123456789abcdef0123456789abcdef", func(context.Context) (net.Conn, error) {
		return nil, errors.New("unused")
	})
	config.Username = "different"
	if _, err := New(config); err == nil {
		t.Fatal("different ClientID/username unexpectedly passed")
	}
	config = testConfig("blk-0123456789abcdef0123456789abcdef", func(context.Context) (net.Conn, error) {
		return nil, errors.New("unused")
	})
	config.Will.Retain = false
	if _, err := New(config); err == nil {
		t.Fatal("non-retained LWT unexpectedly passed")
	}
}

func TestDisconnectedQoS1PublishDoesNotEnterPendingQueue(t *testing.T) {
	principal := "blk-0123456789abcdef0123456789abcdef"
	client, err := New(testConfig(principal, func(context.Context) (net.Conn, error) {
		return nil, errors.New("unused")
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = client.Publish(context.Background(), Message{
		Topic:   "bdm/v1/sites/site-lab/blocks/block-001/up/snapshot",
		Payload: []byte(`{"messageId":"not-sent"}`), QoS: 1,
	})
	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("disconnected publish error = %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.pending) != 0 {
		t.Fatalf("disconnected publish queued %d packet(s)", len(client.pending))
	}
}

func TestCanceledQoS1PublishRemainsInflightAndRetransmitsUntilPUBACK(t *testing.T) {
	clientConnections := make(chan net.Conn, 2)
	firstClient, firstBroker := net.Pipe()
	secondClient, secondBroker := net.Pipe()
	clientConnections <- firstClient
	clientConnections <- secondClient
	principal := "blk-0123456789abcdef0123456789abcdef"
	client, err := New(testConfig(principal, func(ctx context.Context) (net.Conn, error) {
		select {
		case connection := <-clientConnections:
			return connection, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	firstSeen := make(chan uint16, 1)
	closeFirst := make(chan struct{})
	secondResult := make(chan error, 1)
	go func() {
		if err := brokerHandshake(firstBroker, principal); err != nil {
			secondResult <- err
			return
		}
		value, err := readPacket(firstBroker, DefaultMaximumPacketSize)
		if err != nil {
			secondResult <- err
			return
		}
		_, packetID, err := parsePublish(value)
		if err != nil {
			secondResult <- err
			return
		}
		if value.header&0x08 != 0 {
			secondResult <- errors.New("first canceled QoS1 publish incorrectly set DUP")
			return
		}
		firstSeen <- packetID
		<-closeFirst
		_ = firstBroker.Close()
	}()
	go func() {
		if err := brokerHandshakeWithSessionPresent(secondBroker, principal, true); err != nil {
			secondResult <- err
			return
		}
		value, err := readPacket(secondBroker, DefaultMaximumPacketSize)
		if err != nil {
			secondResult <- err
			return
		}
		_, packetID, err := parsePublish(value)
		if err != nil {
			secondResult <- err
			return
		}
		if value.header&0x08 == 0 {
			secondResult <- errors.New("canceled in-flight QoS1 publish was not retransmitted with DUP")
			return
		}
		body := binary.BigEndian.AppendUint16(nil, packetID)
		if err := writePacket(secondBroker, 0x40, body, DefaultMaximumPacketSize); err != nil {
			secondResult <- err
			return
		}
		secondResult <- nil
	}()
	runContext, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(runContext) }()
	<-client.Connected()
	publishContext, cancelPublish := context.WithCancel(context.Background())
	publishDone := make(chan error, 1)
	go func() {
		publishDone <- client.Publish(publishContext, Message{
			Topic:   "bdm/v1/sites/site-lab/blocks/block-001/up/hello",
			Payload: []byte(`{"type":"block.hello"}`), QoS: 1,
		})
	}()
	var firstID uint16
	select {
	case firstID = <-firstSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("first publish not observed")
	}
	cancelPublish()
	if err := <-publishDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publish error = %v", err)
	}
	client.mu.Lock()
	canceledPending := client.pending[firstID]
	client.mu.Unlock()
	if canceledPending == nil {
		t.Fatal("canceled in-flight publish was removed before PUBACK")
	}
	close(closeFirst)
	select {
	case err := <-secondResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not retransmit canceled in-flight publish")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		client.mu.Lock()
		_, stillPending := client.pending[firstID]
		client.mu.Unlock()
		if !stillPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PUBACK did not release canceled in-flight publish")
		}
		time.Sleep(time.Millisecond)
	}
	cancelRun()
	_ = secondBroker.Close()
	<-runDone
}

func TestOpenSessionAcceptsLegalPacketsInterleavedBeforeSUBACK(t *testing.T) {
	clientSide, brokerSide := net.Pipe()
	principal := "blk-0123456789abcdef0123456789abcdef"
	inbound := make(chan Message, 1)
	config := testConfig(principal, func(context.Context) (net.Conn, error) {
		return clientSide, nil
	})
	config.OnMessage = func(message Message) error {
		inbound <- message
		return nil
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	pending := &publishPending{
		packetID: 77,
		topic:    "bdm/v1/sites/site-lab/blocks/block-001/up/hello",
		payload:  []byte(`{"type":"block.hello"}`),
		everSent: true,
		done:     make(chan error, 1),
	}
	client.pending[pending.packetID] = pending

	brokerDone := make(chan error, 1)
	go func() {
		connect, err := readPacket(brokerSide, DefaultMaximumPacketSize)
		if err != nil {
			brokerDone <- err
			return
		}
		if connect.header != 0x10 {
			brokerDone <- errors.New("broker expected CONNECT")
			return
		}
		if err := writePacket(brokerSide, 0x20, []byte{0x01, 0x00, 0x00}, DefaultMaximumPacketSize); err != nil {
			brokerDone <- err
			return
		}
		subscribe, err := readPacket(brokerSide, DefaultMaximumPacketSize)
		if err != nil {
			brokerDone <- err
			return
		}
		if subscribe.header != 0x82 || len(subscribe.body) < 2 {
			brokerDone <- errors.New("broker expected SUBSCRIBE")
			return
		}
		subscriptionID := binary.BigEndian.Uint16(subscribe.body[:2])
		if err := writePacket(brokerSide, 0x40, binary.BigEndian.AppendUint16(nil, pending.packetID), DefaultMaximumPacketSize); err != nil {
			brokerDone <- err
			return
		}
		if err := writePacket(brokerSide, 0xd0, nil, DefaultMaximumPacketSize); err != nil {
			brokerDone <- err
			return
		}
		body, _ := appendUTF(nil, config.SubscribeTopic)
		body = binary.BigEndian.AppendUint16(body, 91)
		body = append(body, 0x00)
		body = append(body, []byte(`{"action":"ACK"}`)...)
		if err := writePacket(brokerSide, 0x32, body, DefaultMaximumPacketSize); err != nil {
			brokerDone <- err
			return
		}
		ack, err := readPacket(brokerSide, DefaultMaximumPacketSize)
		if err != nil {
			brokerDone <- err
			return
		}
		if ack.header != 0x40 || len(ack.body) != 2 || binary.BigEndian.Uint16(ack.body) != 91 {
			brokerDone <- errors.New("client sent malformed interleaved PUBLISH acknowledgement")
			return
		}
		subackBody := binary.BigEndian.AppendUint16(nil, subscriptionID)
		subackBody = append(subackBody, 0x00, 0x01)
		brokerDone <- writePacket(brokerSide, 0x90, subackBody, DefaultMaximumPacketSize)
	}()

	sessionDone := make(chan error, 1)
	go func() {
		current, err := client.openSession(context.Background(), clientSide)
		if err == nil {
			_ = current.connection.Close()
		}
		sessionDone <- err
	}()
	select {
	case err := <-pending.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interleaved PUBACK was not processed")
	}
	select {
	case message := <-inbound:
		if string(message.Payload) != `{"action":"ACK"}` {
			t.Fatalf("interleaved PUBLISH payload = %s", message.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interleaved PUBLISH was not processed")
	}
	if err := <-brokerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-sessionDone; err != nil {
		t.Fatal(err)
	}
}

func TestDefaultReconnectScheduleHasBoundedJitter(t *testing.T) {
	bases := []time.Duration{
		time.Second, 2 * time.Second, 5 * time.Second,
		10 * time.Second, 30 * time.Second, 60 * time.Second,
	}
	for index, base := range bases {
		value := defaultReconnectDelay(index + 1)
		if value < base*8/10 || value > base*12/10 {
			t.Fatalf("attempt %d delay %s outside jittered base %s", index+1, value, base)
		}
	}
	value := defaultReconnectDelay(100)
	if value < 48*time.Second || value > 72*time.Second {
		t.Fatalf("capped reconnect delay = %s", value)
	}
}
