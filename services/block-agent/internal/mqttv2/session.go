package mqttv2

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync"
	"time"

	"block.local/block-agent/internal/alarmhistory"
)

// ConnectionConfig is the small, local configuration required for one
// MQTTS-v2 session. It deliberately carries no reliable-delivery state.
type ConnectionConfig struct {
	Endpoint       string
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string
	Principal      string
	Source         Source
}

// Session owns one best-effort MQTTS connection for an active HMI runtime
// session. On a network loss it reconnects simply and the Manager republishes
// only the latest current state.
type Session struct {
	manager *Manager
	client  *client
}

func NewSession(config ConnectionConfig, history HistoryReader, options Options) (*Session, error) {
	if !validSource(config.Source) {
		return nil, errors.New("MQTT v2 source is required")
	}
	tlsConfig, address, err := newTLSConfig(config)
	if err != nil {
		return nil, err
	}
	var manager *Manager
	client := newClient(clientConfig{
		Address: address, TLSConfig: tlsConfig, ClientID: config.Principal,
		Subscriptions: []string{NewTopics(config.Source.SiteID, config.Source.BlockID).StateGet, NewTopics(config.Source.SiteID, config.Source.BlockID).AlarmHistoryQuery},
		OnMessage: func(message Inbound) error {
			return manager.HandleInbound(context.Background(), message)
		},
	})
	manager = NewManager(config.Source, client, history, options)
	return &Session{manager: manager, client: client}, nil
}

func (s *Session) Run(ctx context.Context) {
	if s == nil {
		return
	}
	go s.client.Run(ctx)
	ticker := time.NewTicker(DefaultPublishInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.manager.Disconnected()
			return
		case <-s.client.Connected():
			_ = s.manager.Connected(ctx)
		case <-s.client.Disconnected():
			s.manager.Disconnected()
		case <-ticker.C:
			_ = s.manager.Tick(ctx)
		}
	}
}

// ObserveSnapshot keeps only the latest state; disconnected sends are not
// retained for later delivery.
func (s *Session) ObserveSnapshot(snapshot Snapshot) {
	if s != nil {
		_ = s.manager.ObserveSnapshot(context.Background(), snapshot)
	}
}

func (s *Session) Notify(ctx context.Context, record alarmhistory.Record) error {
	if s == nil {
		return ErrDisconnected
	}
	return s.manager.Notify(ctx, record)
}

func newTLSConfig(config ConnectionConfig) (*tls.Config, string, error) {
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Scheme != "mqtts" || parsed.Hostname() == "" || parsed.Port() != "8883" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, "", errors.New("MQTT endpoint must be mqtts://HOST:8883")
	}
	if config.Principal == "" {
		return nil, "", errors.New("MQTT principal is required")
	}
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, "", fmt.Errorf("read MQTTS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, "", errors.New("MQTTS CA file contains no certificates")
	}
	certificate, err := tls.LoadX509KeyPair(config.ClientCertFile, config.ClientKeyFile)
	if err != nil {
		return nil, "", fmt.Errorf("load MQTTS client certificate: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return nil, "", errors.New("MQTTS client certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, "", fmt.Errorf("parse MQTTS client certificate: %w", err)
	}
	if leaf.Subject.CommonName != config.Principal {
		return nil, "", errors.New("MQTTS client certificate CN does not equal principal")
	}
	if now := time.Now(); now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, "", errors.New("MQTTS client certificate is not currently valid")
	}
	clientAuth := false
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny {
			clientAuth = true
			break
		}
	}
	if !clientAuth {
		return nil, "", errors.New("MQTTS client certificate lacks clientAuth extended key usage")
	}
	certificate.Leaf = leaf
	return &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: parsed.Hostname()}, parsed.Host, nil
}

type clientConfig struct {
	Address       string
	TLSConfig     *tls.Config
	ClientID      string
	Subscriptions []string
	OnMessage     func(Inbound) error
}

// client is intentionally limited to the MQTT 5.0 features used by v2:
// mTLS, QoS 0 publication, two QoS 0 subscriptions and a simple reconnect.
type client struct {
	config clientConfig

	mu           sync.Mutex
	connection   net.Conn
	writeMu      sync.Mutex
	connected    chan struct{}
	disconnected chan struct{}
}

func newClient(config clientConfig) *client {
	return &client{config: config, connected: make(chan struct{}, 1), disconnected: make(chan struct{}, 1)}
}

func (c *client) Connected() <-chan struct{}    { return c.connected }
func (c *client) Disconnected() <-chan struct{} { return c.disconnected }

func (c *client) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			c.close()
			return
		}
		connection, err := c.connect(ctx)
		if err == nil {
			c.setConnection(connection)
			select {
			case c.connected <- struct{}{}:
			default:
			}
			err = c.serve(ctx, connection)
			c.clearConnection(connection)
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *client) Publish(_ context.Context, publication Publication) error {
	if publication.QoS != 0 || publication.Retain {
		return ErrInvalidRequest
	}
	c.mu.Lock()
	connection := c.connection
	c.mu.Unlock()
	if connection == nil {
		return ErrDisconnected
	}
	return c.writePacket(connection, 0x30, publishPayload(publication))
}

func (c *client) connect(ctx context.Context) (net.Conn, error) {
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}, Config: c.config.TLSConfig}
	connection, err := dialer.DialContext(ctx, "tcp", c.config.Address)
	if err != nil {
		return nil, err
	}
	if err := c.writePacket(connection, 0x10, connectPayload(c.config.ClientID)); err != nil {
		_ = connection.Close()
		return nil, err
	}
	header, payload, err := readPacket(connection)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if header != 0x20 {
		_ = connection.Close()
		return nil, errors.New("MQTT CONNACK is invalid")
	}
	if err := validateConnack(payload); err != nil {
		_ = connection.Close()
		return nil, err
	}
	if len(c.config.Subscriptions) > 0 {
		if err := c.writePacket(connection, 0x82, subscribePayload(c.config.Subscriptions)); err != nil {
			_ = connection.Close()
			return nil, err
		}
		header, payload, err = readPacket(connection)
		if err != nil {
			_ = connection.Close()
			return nil, err
		}
		if header != 0x90 {
			_ = connection.Close()
			return nil, errors.New("MQTT SUBACK is invalid")
		}
		if err := validateSuback(payload, len(c.config.Subscriptions)); err != nil {
			_ = connection.Close()
			return nil, err
		}
	}
	return connection, nil
}

func (c *client) serve(ctx context.Context, connection net.Conn) error {
	errorsChannel := make(chan error, 1)
	go func() {
		for {
			header, payload, err := readPacket(connection)
			if err != nil {
				errorsChannel <- err
				return
			}
			if header>>4 == 3 {
				if err := c.handlePublish(header, payload); err != nil {
					errorsChannel <- err
					return
				}
			}
		}
	}()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = connection.Close()
			return nil
		case err := <-errorsChannel:
			return err
		case <-ticker.C:
			if err := c.writePacket(connection, 0xc0, nil); err != nil {
				return err
			}
		}
	}
}

func (c *client) handlePublish(header byte, payload []byte) error {
	qos := (header >> 1) & 0x03
	if qos != 0 || header&0x01 != 0 || len(payload) < 2 {
		return ErrInboundPolicy
	}
	length := int(payload[0])<<8 | int(payload[1])
	if length == 0 || len(payload) < 2+length {
		return ErrInvalidRequest
	}
	propertiesLength, propertiesStart, err := decodeVariableByteInteger(payload[2+length:])
	if err != nil || propertiesLength > len(payload)-(2+length+propertiesStart) {
		return ErrInvalidRequest
	}
	payloadStart := 2 + length + propertiesStart + propertiesLength
	if c.config.OnMessage == nil {
		return nil
	}
	return c.config.OnMessage(Inbound{Topic: string(payload[2 : 2+length]), Payload: append([]byte(nil), payload[payloadStart:]...), QoS: qos, Retain: header&0x01 != 0})
}

func (c *client) setConnection(connection net.Conn) {
	c.mu.Lock()
	c.connection = connection
	c.mu.Unlock()
}
func (c *client) clearConnection(connection net.Conn) {
	c.mu.Lock()
	wasCurrent := c.connection == connection
	if wasCurrent {
		c.connection = nil
	}
	c.mu.Unlock()
	_ = connection.Close()
	if wasCurrent {
		select {
		case c.disconnected <- struct{}{}:
		default:
		}
	}
}
func (c *client) close() {
	c.mu.Lock()
	connection := c.connection
	c.connection = nil
	c.mu.Unlock()
	if connection != nil {
		_ = connection.Close()
		select {
		case c.disconnected <- struct{}{}:
		default:
		}
	}
}
func (c *client) writePacket(connection net.Conn, header byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	packet := append([]byte{header}, encodeRemainingLength(len(payload))...)
	packet = append(packet, payload...)
	_, err := connection.Write(packet)
	return err
}

func connectPayload(clientID string) []byte {
	// Clean Start plus the default session-expiry interval of zero keeps this
	// connection stateless. The zero byte is the MQTT 5 CONNECT property length.
	payload := append(mqttString("MQTT"), 5, 0x02, 0, 30, 0)
	return append(payload, mqttString(clientID)...)
}
func publishPayload(publication Publication) []byte {
	payload := append(mqttString(publication.Topic), 0) // MQTT 5 PUBLISH properties length.
	return append(payload, publication.Payload...)
}
func subscribePayload(subscriptions []string) []byte {
	payload := []byte{0, 1, 0} // packet identifier 1 and MQTT 5 properties length.
	for _, topic := range subscriptions {
		payload = append(payload, mqttString(topic)...)
		payload = append(payload, 0)
	}
	return payload
}
func mqttString(value string) []byte {
	return append([]byte{byte(len(value) >> 8), byte(len(value))}, value...)
}
func encodeRemainingLength(length int) []byte {
	var encoded []byte
	for {
		digit := byte(length % 128)
		length /= 128
		if length > 0 {
			digit |= 0x80
		}
		encoded = append(encoded, digit)
		if length == 0 {
			return encoded
		}
	}
}

func validateConnack(payload []byte) error {
	if len(payload) < 3 {
		return errors.New("MQTT CONNACK is invalid")
	}
	ackFlags, reasonCode := payload[0], payload[1]
	if ackFlags&^byte(0x01) != 0 || ackFlags&0x01 != 0 {
		return errors.New("MQTT CONNACK session flags are invalid")
	}
	propertiesLength, propertiesStart, err := decodeVariableByteInteger(payload[2:])
	if err != nil || propertiesLength != len(payload)-(2+propertiesStart) {
		return errors.New("MQTT CONNACK properties are invalid")
	}
	if reasonCode != 0 {
		return fmt.Errorf("MQTT CONNACK rejected: reason code 0x%02x", reasonCode)
	}
	return nil
}

func validateSuback(payload []byte, subscriptions int) error {
	if len(payload) < 3 || payload[0] != 0 || payload[1] != 1 {
		return errors.New("MQTT SUBACK is invalid")
	}
	propertiesLength, propertiesStart, err := decodeVariableByteInteger(payload[2:])
	if err != nil {
		return errors.New("MQTT SUBACK properties are invalid")
	}
	reasonsStart := 2 + propertiesStart + propertiesLength
	if reasonsStart > len(payload) || len(payload)-reasonsStart != subscriptions {
		return errors.New("MQTT SUBACK is invalid")
	}
	for _, reasonCode := range payload[reasonsStart:] {
		if reasonCode != 0 {
			return fmt.Errorf("MQTT SUBACK rejected: reason code 0x%02x", reasonCode)
		}
	}
	return nil
}

func decodeVariableByteInteger(payload []byte) (int, int, error) {
	value, multiplier := 0, 1
	for offset := 0; offset < 4; offset++ {
		if offset >= len(payload) {
			return 0, 0, errors.New("MQTT variable byte integer is truncated")
		}
		digit := payload[offset]
		value += int(digit&127) * multiplier
		if digit&128 == 0 {
			return value, offset + 1, nil
		}
		multiplier *= 128
	}
	return 0, 0, errors.New("MQTT variable byte integer is invalid")
}
func readPacket(connection net.Conn) (byte, []byte, error) {
	var first [1]byte
	if _, err := connection.Read(first[:]); err != nil {
		return 0, nil, err
	}
	length, multiplier := 0, 1
	for count := 0; count < 4; count++ {
		var digit [1]byte
		if _, err := connection.Read(digit[:]); err != nil {
			return 0, nil, err
		}
		length += int(digit[0]&127) * multiplier
		if digit[0]&128 == 0 {
			payload := make([]byte, length)
			for offset := 0; offset < length; {
				n, err := connection.Read(payload[offset:])
				if err != nil {
					return 0, nil, err
				}
				offset += n
			}
			return first[0], payload, nil
		}
		multiplier *= 128
	}
	return 0, nil, errors.New("MQTT remaining length is invalid")
}

var _ Sender = (*client)(nil)
