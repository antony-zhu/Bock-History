package mqttv2

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"block.local/block-agent/internal/alarmhistory"
)

func TestMQTT5ConnectPayload(t *testing.T) {
	got := connectPayload("block-001")
	want := []byte{
		0, 4, 'M', 'Q', 'T', 'T',
		5,    // MQTT 5.0 protocol level
		0x02, // Clean Start
		0, 30,
		0, // CONNECT properties length
		0, 9, 'b', 'l', 'o', 'c', 'k', '-', '0', '0', '1',
	}
	if string(got) != string(want) {
		t.Fatalf("CONNECT payload = %x, want %x", got, want)
	}
}

func TestMQTT5ConnackSuccessAndFailure(t *testing.T) {
	// Property 0x21 (Receive Maximum) has two bytes. The client does not need
	// its value, but must consume the MQTT 5 properties section correctly.
	if err := validateConnack([]byte{0, 0, 3, 0x21, 0, 10}); err != nil {
		t.Fatalf("success CONNACK rejected: %v", err)
	}
	if err := validateConnack([]byte{0, 0x87, 0}); err == nil {
		t.Fatal("failure CONNACK accepted")
	}
}

func TestMQTT5StateAndHistoryPublications(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	source := Source{SiteID: "site-lab", BlockID: "block-001", DeviceID: "device-001"}
	connection := &mqtt5WireConnection{}
	sender := newClient(clientConfig{})
	sender.setConnection(connection)
	history := &mqtt5History{}
	manager := NewManager(source, sender, history, Options{Now: func() time.Time { return now }})

	if err := manager.Connected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ObserveSnapshot(context.Background(), Snapshot{Values: map[string]PointValue{
		"machine.running": {Value: true, Quality: QualityGood, UpdatedAt: now},
	}}); err != nil {
		t.Fatal(err)
	}
	requestID := "315a1ea6-1cdc-47d9-96f9-b4f80ffbda7c"
	request, err := json.Marshal(AlarmHistoryQuery{
		Type: "alarm.history.query", SchemaVersion: SchemaVersion, RequestID: requestID, Target: source,
		FromOccurredAt: now.Add(-time.Minute).Format(time.RFC3339), ToOccurredAt: now.Add(time.Minute).Format(time.RFC3339), Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.HandleInbound(context.Background(), Inbound{
		Topic: NewTopics(source.SiteID, source.BlockID).AlarmHistoryQuery, Payload: request, QoS: 0,
	}); err != nil {
		t.Fatal(err)
	}
	packets := decodeMQTT5Packets(t, connection.wire)
	if len(packets) != 2 {
		t.Fatalf("PUBLISH packets = %d, want 2", len(packets))
	}
	for _, packet := range packets {
		if packet.header != 0x30 {
			t.Fatalf("PUBLISH header = 0x%02x, want 0x30", packet.header)
		}
	}

	topics := NewTopics(source.SiteID, source.BlockID)
	stateTopic, statePayload := decodeMQTT5Publish(t, packets[0].body)
	if stateTopic != topics.StateLatest {
		t.Fatalf("state topic = %q, want %q", stateTopic, topics.StateLatest)
	}
	var state StateCurrent
	if err := json.Unmarshal(statePayload, &state); err != nil || state.Type != "device.state.current" {
		t.Fatalf("state payload = %s, error = %v", statePayload, err)
	}

	historyTopic, historyPayload := decodeMQTT5Publish(t, packets[1].body)
	if historyTopic != topics.AlarmHistoryPage {
		t.Fatalf("history topic = %q, want %q", historyTopic, topics.AlarmHistoryPage)
	}
	var page AlarmHistoryPage
	if err := json.Unmarshal(historyPayload, &page); err != nil || page.Type != "alarm.history.page" || page.RequestID != requestID {
		t.Fatalf("history payload = %s, error = %v", historyPayload, err)
	}
}

func TestMQTT5SubscribePayload(t *testing.T) {
	got := subscribePayload([]string{"one", "two"})
	want := []byte{0, 1, 0, 0, 3, 'o', 'n', 'e', 0, 0, 3, 't', 'w', 'o', 0}
	if string(got) != string(want) {
		t.Fatalf("SUBSCRIBE payload = %x, want %x", got, want)
	}
	if err := validateSuback([]byte{0, 1, 0, 0, 0}, 2); err != nil {
		t.Fatalf("success SUBACK rejected: %v", err)
	}
	if err := validateSuback([]byte{0, 1, 0, 0x80, 0}, 2); err == nil {
		t.Fatal("failed SUBACK accepted")
	}
}

func TestMQTT5InboundPublishSkipsProperties(t *testing.T) {
	var got Inbound
	client := newClient(clientConfig{OnMessage: func(inbound Inbound) error {
		got = inbound
		return nil
	}})
	payload := append(mqttString("bdm/v2/test"), 2, 0x01, 0)
	payload = append(payload, []byte(`{"type":"device.state.get"}`)...)
	if err := client.handlePublish(0x30, payload); err != nil {
		t.Fatalf("handlePublish() error = %v", err)
	}
	if got.Topic != "bdm/v2/test" || string(got.Payload) != `{"type":"device.state.get"}` || got.QoS != 0 || got.Retain {
		t.Fatalf("inbound = %#v", got)
	}
}

type mqtt5History struct{}

func (mqtt5History) List(_ context.Context, _ alarmhistory.Query) (alarmhistory.Page, error) {
	return alarmhistory.Page{}, nil
}

func decodeMQTT5Publish(t *testing.T, body []byte) (string, []byte) {
	t.Helper()
	if len(body) < 3 {
		t.Fatalf("PUBLISH body is too short: %x", body)
	}
	topicLength := int(body[0])<<8 | int(body[1])
	if topicLength == 0 || len(body) < 2+topicLength+1 {
		t.Fatalf("PUBLISH topic is invalid: %x", body)
	}
	propertiesLength, propertiesStart, err := decodeVariableByteInteger(body[2+topicLength:])
	if err != nil || propertiesLength != 0 {
		t.Fatalf("PUBLISH properties = length %d, bytes %d, error %v", propertiesLength, propertiesStart, err)
	}
	payloadStart := 2 + topicLength + propertiesStart
	return string(body[2 : 2+topicLength]), body[payloadStart:]
}

type mqtt5Packet struct {
	header byte
	body   []byte
}

func decodeMQTT5Packets(t *testing.T, wire []byte) []mqtt5Packet {
	t.Helper()
	var packets []mqtt5Packet
	for len(wire) > 0 {
		if len(wire) < 2 {
			t.Fatalf("MQTT packet is truncated: %x", wire)
		}
		length, lengthBytes, err := decodeVariableByteInteger(wire[1:])
		if err != nil || len(wire) < 1+lengthBytes+length {
			t.Fatalf("MQTT packet length is invalid: %x, error %v", wire, err)
		}
		bodyStart := 1 + lengthBytes
		packets = append(packets, mqtt5Packet{header: wire[0], body: append([]byte(nil), wire[bodyStart:bodyStart+length]...)})
		wire = wire[bodyStart+length:]
	}
	return packets
}

type mqtt5WireConnection struct {
	wire []byte
}

func (c *mqtt5WireConnection) Read(_ []byte) (int, error) { return 0, io.EOF }
func (c *mqtt5WireConnection) Write(payload []byte) (int, error) {
	c.wire = append(c.wire, payload...)
	return len(payload), nil
}
func (c *mqtt5WireConnection) Close() error                     { return nil }
func (c *mqtt5WireConnection) LocalAddr() net.Addr              { return mqtt5Address("local") }
func (c *mqtt5WireConnection) RemoteAddr() net.Addr             { return mqtt5Address("remote") }
func (c *mqtt5WireConnection) SetDeadline(time.Time) error      { return nil }
func (c *mqtt5WireConnection) SetReadDeadline(time.Time) error  { return nil }
func (c *mqtt5WireConnection) SetWriteDeadline(time.Time) error { return nil }

type mqtt5Address string

func (a mqtt5Address) Network() string { return "tcp" }
func (a mqtt5Address) String() string  { return string(a) }
