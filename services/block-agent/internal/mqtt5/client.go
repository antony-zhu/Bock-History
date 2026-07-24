package mqtt5

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrDisconnected          = errors.New("MQTT client is disconnected")
	ErrSessionLost           = errors.New("MQTT broker did not resume the previous session")
	ErrPendingResumeRequired = errors.New("MQTT pending packets must resume before new publishes")
	ErrProtocol              = errors.New("MQTT protocol error")
)

const sessionStoreTimeout = 10 * time.Second

type Message struct {
	Topic          string
	Payload        []byte
	QoS            byte
	Retain         bool
	ApplicationKey string
}

type Will struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}

type DialContextFunc func(context.Context) (net.Conn, error)

type Config struct {
	ClientID          string
	Username          string
	KeepAlive         time.Duration
	SessionExpiry     uint32
	MaximumPacketSize int
	ReceiveMaximum    uint16
	SubscribeTopic    string
	Will              Will
	DialContext       DialContextFunc
	ReconnectDelay    func(attempt int) time.Duration
	OnMessage         func(Message) error
	OnError           func(error)
	SessionStore      SessionStore
	// DeferPendingUntilReady lets the application explicitly resume the
	// broker-session QoS 1 in-flight set before it emits any new publish.
	DeferPendingUntilReady bool
}

type publishPending struct {
	packetID       uint16
	topic          string
	payload        []byte
	retain         bool
	applicationKey string
	order          uint64
	everSent       bool
	session        uint64
	done           chan error
}

type session struct {
	id             uint64
	present        bool
	resumeRequired bool
	connection     net.Conn
	writeMu        sync.Mutex
	lastWrite      atomic.Int64
	pingMu         sync.Mutex
	pingAt         time.Time
}

type Client struct {
	config Config

	mu           sync.Mutex
	session      *session
	pending      map[uint16]*publishPending
	reserved     map[uint16]bool
	nextID       uint16
	publishOrder uint64
	sessionID    uint64
	closed       bool
	connected    chan struct{}
}

func New(config Config) (*Client, error) {
	if config.ClientID == "" || config.Username == "" || config.ClientID != config.Username {
		return nil, errors.New("MQTT ClientID and username must be the same non-empty principal")
	}
	if config.DialContext == nil {
		return nil, errors.New("MQTT DialContext is required")
	}
	if config.KeepAlive <= 0 {
		config.KeepAlive = 30 * time.Second
	}
	if config.KeepAlive < time.Second || config.KeepAlive > 65535*time.Second {
		return nil, errors.New("MQTT KeepAlive is outside the protocol range")
	}
	if config.SessionExpiry == 0 {
		config.SessionExpiry = 7 * 24 * 60 * 60
	}
	if config.MaximumPacketSize == 0 {
		config.MaximumPacketSize = DefaultMaximumPacketSize
	}
	if config.MaximumPacketSize < 128 || config.MaximumPacketSize > maxRemainingLength {
		return nil, errors.New("MQTT MaximumPacketSize is invalid")
	}
	if config.ReceiveMaximum == 0 {
		config.ReceiveMaximum = 20
	}
	if config.SubscribeTopic == "" {
		return nil, errors.New("MQTT down/sync subscription topic is required")
	}
	if config.Will.Topic == "" || config.Will.QoS != 1 || !config.Will.Retain {
		return nil, errors.New("MQTT presence LWT must be retained QoS 1")
	}
	if config.ReconnectDelay == nil {
		config.ReconnectDelay = defaultReconnectDelay
	}
	client := &Client{
		config: config, pending: make(map[uint16]*publishPending),
		reserved:  make(map[uint16]bool),
		connected: make(chan struct{}, 1),
	}
	if config.SessionStore == nil {
		return client, nil
	}
	storeContext, cancelStore := context.WithTimeout(context.Background(), sessionStoreTimeout)
	stored, err := config.SessionStore.LoadMQTTInflight(storeContext)
	cancelStore()
	if err != nil {
		return nil, fmt.Errorf("load MQTT transport in-flight state: %w", err)
	}
	orders := make(map[uint64]bool, len(stored))
	for _, record := range stored {
		if record.PacketID == 0 || record.Topic == "" || record.Order == 0 {
			return nil, errors.New("stored MQTT in-flight record is invalid")
		}
		if _, exists := client.pending[record.PacketID]; exists || orders[record.Order] {
			return nil, errors.New("stored MQTT in-flight packet id or order is duplicated")
		}
		if err := validatePublishSize(record.PacketID, Message{
			Topic: record.Topic, Payload: record.Payload, QoS: 1, Retain: record.Retain,
		}, config.MaximumPacketSize); err != nil {
			return nil, fmt.Errorf("validate stored MQTT publish: %w", err)
		}
		client.pending[record.PacketID] = &publishPending{
			packetID: record.PacketID, topic: record.Topic,
			payload: append([]byte{}, record.Payload...), retain: record.Retain,
			applicationKey: record.ApplicationKey, order: record.Order,
			everSent: record.EverSent,
		}
		orders[record.Order] = true
		if record.Order > client.publishOrder {
			client.publishOrder = record.Order
		}
		if record.PacketID > client.nextID {
			client.nextID = record.PacketID
		}
	}
	return client, nil
}

func TLSConfig(endpoint, caFile, clientCertFile, clientKeyFile, principal string, now time.Time) (*tls.Config, string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "mqtts" || parsed.Hostname() == "" || parsed.Port() != "8883" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, "", errors.New("MQTT endpoint must be mqtts://HOST:8883")
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, "", fmt.Errorf("read BDM CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, "", errors.New("BDM CA file contains no certificates")
	}
	certificate, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
	if err != nil {
		return nil, "", fmt.Errorf("load Block client certificate: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return nil, "", errors.New("Block client certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, "", fmt.Errorf("parse Block client certificate: %w", err)
	}
	if leaf.Subject.CommonName != principal {
		return nil, "", errors.New("Block client certificate CN does not equal MQTT principal")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, "", errors.New("Block client certificate is not currently valid")
	}
	clientAuth := false
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny {
			clientAuth = true
			break
		}
	}
	if !clientAuth {
		return nil, "", errors.New("Block client certificate lacks clientAuth extended key usage")
	}
	for _, raw := range certificate.Certificate[1:] {
		if _, err := x509.ParseCertificate(raw); err != nil {
			return nil, "", fmt.Errorf("parse Block client intermediate: %w", err)
		}
	}
	certificate.Leaf = leaf
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS13,
		RootCAs: roots, Certificates: []tls.Certificate{certificate},
		ServerName: parsed.Hostname(),
	}
	return tlsConfig, parsed.Host, nil
}

func TLSDialer(address string, tlsConfig *tls.Config, timeout time.Duration) DialContextFunc {
	return func(ctx context.Context) (net.Conn, error) {
		dialer := &tls.Dialer{
			NetDialer: &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second},
			Config:    tlsConfig,
		}
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		return connection, nil
	}
}

func (c *Client) Connected() <-chan struct{} {
	return c.connected
}

// SessionID is zero while disconnected and changes for every successfully
// established transport session. Higher layers use it to prevent application
// traffic from crossing their own per-connection Hello gate.
func (c *Client) SessionID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return 0
	}
	return c.session.id
}

// SessionPresent reports whether the broker resumed its stored MQTT session
// for the named current transport connection.
func (c *Client) SessionPresent(sessionID uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session != nil && c.session.id == sessionID && c.session.present
}

// HasPendingApplicationKey reports whether an application record is already
// represented by an MQTT QoS 1 packet whose outcome is still unknown.
func (c *Client) HasPendingApplicationKey(key string) bool {
	if key == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, item := range c.pending {
		if item.applicationKey == key {
			return true
		}
	}
	return false
}

// AbortSession closes only the named current session. It is used when an
// application-level connection preamble (Presence/Hello/Sync Status) fails,
// forcing a clean reconnect before reliable business traffic is allowed.
func (c *Client) AbortSession(sessionID uint64) {
	c.mu.Lock()
	current := c.session
	c.mu.Unlock()
	if current != nil && current.id == sessionID {
		_ = current.connection.Close()
	}
}

func (c *Client) Run(ctx context.Context) error {
	attempt := 0
	for {
		if ctx.Err() != nil {
			c.shutdown(ctx.Err())
			return nil
		}
		connection, err := c.config.DialContext(ctx)
		if err != nil {
			c.report(err)
			attempt++
			if !waitContext(ctx, c.config.ReconnectDelay(attempt)) {
				c.shutdown(ctx.Err())
				return nil
			}
			continue
		}
		current, err := c.openSession(ctx, connection)
		if err != nil {
			_ = connection.Close()
			c.report(err)
			attempt++
			if !waitContext(ctx, c.config.ReconnectDelay(attempt)) {
				c.shutdown(ctx.Err())
				return nil
			}
			continue
		}
		connectedAt := time.Now()
		lost, err := c.installSession(ctx, current)
		if err != nil {
			_ = current.connection.Close()
			c.report(err)
			attempt++
			if !waitContext(ctx, c.config.ReconnectDelay(attempt)) {
				c.shutdown(ctx.Err())
				return nil
			}
			continue
		}
		notifyPending(lost, ErrSessionLost)
		sessionDone := make(chan error, 1)
		go func() { sessionDone <- c.serveSession(ctx, current) }()
		if !c.config.DeferPendingUntilReady && current.present {
			err = c.resendPending(current)
		}
		if err != nil {
			_ = current.connection.Close()
		} else {
			select {
			case c.connected <- struct{}{}:
			default:
			}
		}
		err = <-sessionDone
		c.clearSession(current, err)
		if ctx.Err() == nil && err != nil {
			c.report(err)
			if time.Since(connectedAt) >= c.config.KeepAlive {
				attempt = 0
			}
			attempt++
			if !waitContext(ctx, c.config.ReconnectDelay(attempt)) {
				c.shutdown(ctx.Err())
				return nil
			}
		}
	}
}

func (c *Client) Publish(ctx context.Context, message Message) error {
	if message.QoS == 0 {
		c.mu.Lock()
		current := c.session
		if current == nil {
			c.mu.Unlock()
			return ErrDisconnected
		}
		if current.resumeRequired {
			c.mu.Unlock()
			return ErrPendingResumeRequired
		}
		c.mu.Unlock()
		return c.writePublish(current, 0, message, false)
	}
	if message.QoS != 1 {
		return errors.New("only MQTT QoS 0 and QoS 1 are supported")
	}
	c.mu.Lock()
	if c.closed || c.session == nil {
		c.mu.Unlock()
		return ErrDisconnected
	}
	current := c.session
	if current.resumeRequired {
		c.mu.Unlock()
		return ErrPendingResumeRequired
	}
	packetID, err := c.allocatePacketIDLocked()
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if err := validatePublishSize(packetID, message, c.config.MaximumPacketSize); err != nil {
		c.mu.Unlock()
		return err
	}
	if c.publishOrder >= uint64(1<<63-1) {
		c.mu.Unlock()
		return errors.New("MQTT persistent publish order is exhausted")
	}
	c.publishOrder++
	pending := &publishPending{
		packetID: packetID, topic: message.Topic, payload: append([]byte{}, message.Payload...),
		retain:         message.Retain,
		applicationKey: message.ApplicationKey,
		order:          c.publishOrder,
		everSent:       true,
		session:        current.id,
		done:           make(chan error, 1),
	}
	if c.config.SessionStore != nil {
		storeContext, cancelStore := context.WithTimeout(
			context.Background(), sessionStoreTimeout,
		)
		err := c.config.SessionStore.SaveMQTTInflight(storeContext, StoredPublish{
			PacketID: pending.packetID, Topic: pending.topic,
			Payload: append([]byte{}, pending.payload...), Retain: pending.retain,
			ApplicationKey: pending.applicationKey, Order: pending.order,
			// Persist the retry obligation before the first socket write. A
			// crash in the following tiny window is safely recovered as DUP.
			EverSent: true,
		})
		cancelStore()
		if err != nil {
			c.mu.Unlock()
			return fmt.Errorf("persist MQTT QoS 1 packet before publish: %w", err)
		}
	}
	waiter := pending.done
	c.pending[packetID] = pending
	c.mu.Unlock()
	if err := c.writePending(current, pending, false); err != nil {
		_ = current.connection.Close()
	}
	select {
	case err := <-waiter:
		return err
	case <-ctx.Done():
		c.mu.Lock()
		if currentPending := c.pending[packetID]; currentPending == pending {
			currentPending.done = nil
			if !pending.everSent {
				// Nothing reached a transport, so cancellation can safely
				// retract this request and release its packet identifier.
				delete(c.pending, packetID)
			}
			// Once written, QoS 1 has an unknown outcome. Keep the in-flight
			// packet internally and retransmit with the same id + DUP until
			// PUBACK, even though this caller no longer waits for completion.
		}
		c.mu.Unlock()
		return ctx.Err()
	}
}

func (c *Client) PublishQoS0(message Message) error {
	message.QoS = 0
	return c.Publish(context.Background(), message)
}

func (c *Client) installSession(
	_ context.Context,
	current *session,
) ([]*publishPending, error) {
	if !current.present && c.config.SessionStore != nil {
		storeContext, cancelStore := context.WithTimeout(
			context.Background(), sessionStoreTimeout,
		)
		err := c.config.SessionStore.ClearMQTTInflight(storeContext)
		cancelStore()
		if err != nil {
			return nil, fmt.Errorf("clear MQTT in-flight state after session loss: %w", err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = current
	current.resumeRequired = current.present && len(c.pending) > 0
	if current.present {
		return nil, nil
	}
	lost := make([]*publishPending, 0, len(c.pending))
	for packetID, item := range c.pending {
		if item.session == current.id {
			continue
		}
		delete(c.pending, packetID)
		if item.done != nil {
			lost = append(lost, &publishPending{done: item.done})
			item.done = nil
		}
	}
	return lost, nil
}

// ResumePending retransmits every unresolved packet from the broker's resumed
// MQTT session. Packet identifiers are retained and DUP is always set.
func (c *Client) ResumePending(sessionID uint64) error {
	current, err := c.resumableSession(sessionID)
	if err != nil {
		return err
	}
	return c.resendPending(current)
}

func (c *Client) resumableSession(sessionID uint64) (*session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil || c.session.id != sessionID {
		return nil, ErrDisconnected
	}
	if !c.session.present {
		return nil, ErrSessionLost
	}
	return c.session, nil
}

func (c *Client) resendPending(current *session) error {
	c.mu.Lock()
	pending := make([]*publishPending, 0, len(c.pending))
	for _, item := range c.pending {
		if item.session == current.id {
			continue
		}
		duplicate := item.everSent
		item.everSent = true
		item.session = current.id
		copy := *item
		copy.everSent = duplicate
		pending = append(pending, &copy)
	}
	c.mu.Unlock()
	sort.Slice(pending, func(left, right int) bool {
		if pending[left].order == pending[right].order {
			return pending[left].packetID < pending[right].packetID
		}
		return pending[left].order < pending[right].order
	})
	for _, item := range pending {
		if err := c.writePending(current, item, item.everSent); err != nil {
			return err
		}
	}
	c.mu.Lock()
	if c.session == current {
		current.resumeRequired = false
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) clearSession(current *session, cause error) {
	_ = current.connection.Close()
	_ = cause
	c.mu.Lock()
	if c.session == current {
		c.session = nil
	}
	var failed []*publishPending
	for _, item := range c.pending {
		if item.session != current.id {
			continue
		}
		if item.done != nil {
			failed = append(failed, &publishPending{done: item.done})
			item.done = nil
		}
	}
	c.mu.Unlock()
	notifyPending(failed, ErrDisconnected)
}

func (c *Client) openSession(ctx context.Context, connection net.Conn) (*session, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	}
	connectBody, err := c.connectBody()
	if err != nil {
		return nil, err
	}
	if err := writePacket(connection, 0x10, connectBody, c.config.MaximumPacketSize); err != nil {
		return nil, err
	}
	response, err := readPacket(connection, c.config.MaximumPacketSize)
	if err != nil {
		return nil, err
	}
	sessionPresent, err := parseConnack(response)
	if err != nil {
		return nil, err
	}
	subscriptionID := c.reserveSubscriptionID()
	defer c.releaseSubscriptionID(subscriptionID)
	if err := writePacket(connection, 0x82, subscribeBody(subscriptionID, c.config.SubscribeTopic), c.config.MaximumPacketSize); err != nil {
		return nil, err
	}
	for {
		response, err = readPacket(connection, c.config.MaximumPacketSize)
		if err != nil {
			return nil, err
		}
		switch response.header >> 4 {
		case 9:
			if err := parseSuback(response, subscriptionID); err != nil {
				return nil, err
			}
			_ = connection.SetDeadline(time.Time{})
			c.mu.Lock()
			c.sessionID++
			id := c.sessionID
			c.mu.Unlock()
			current := &session{id: id, present: sessionPresent, connection: connection}
			current.lastWrite.Store(time.Now().UnixNano())
			return current, nil
		case 3:
			if err := c.handlePublishPacket(connection, nil, response); err != nil {
				return nil, err
			}
		case 4:
			if err := c.handlePubackPacket(response); err != nil {
				return nil, err
			}
		case 13:
			if err := validatePingresp(response); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: expected SUBACK, received packet type %d", ErrProtocol, response.header>>4)
		}
	}
}

func (c *Client) serveSession(ctx context.Context, current *session) error {
	packetChannel := make(chan packet, 1)
	errorChannel := make(chan error, 1)
	readerDone := make(chan struct{})
	defer close(readerDone)
	go func() {
		for {
			value, err := readPacket(current.connection, c.config.MaximumPacketSize)
			if err != nil {
				select {
				case errorChannel <- err:
				case <-readerDone:
				}
				return
			}
			select {
			case packetChannel <- value:
			case <-readerDone:
				return
			}
		}
	}()
	ticker := time.NewTicker(c.config.KeepAlive / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = c.sendDisconnect(current)
			return nil
		case err := <-errorChannel:
			return err
		case value := <-packetChannel:
			if err := c.handleSessionPacket(current, value); err != nil {
				return err
			}
		case now := <-ticker.C:
			if err := c.keepAlive(current, now); err != nil {
				return err
			}
		}
	}
}

func (c *Client) handleSessionPacket(current *session, value packet) error {
	switch value.header >> 4 {
	case 3:
		return c.handlePublishPacket(current.connection, &current.writeMu, value)
	case 4:
		return c.handlePubackPacket(value)
	case 13:
		if err := validatePingresp(value); err != nil {
			return err
		}
		current.pingMu.Lock()
		current.pingAt = time.Time{}
		current.pingMu.Unlock()
		return nil
	case 14:
		return fmt.Errorf("%w: broker sent DISCONNECT", ErrProtocol)
	default:
		return fmt.Errorf("%w: unexpected packet type %d", ErrProtocol, value.header>>4)
	}
}

func (c *Client) handlePubackPacket(value packet) error {
	packetID, reason, err := parsePuback(value)
	if err != nil {
		return err
	}
	c.mu.Lock()
	pending := c.pending[packetID]
	c.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("%w: PUBACK has unknown packet id %d", ErrProtocol, packetID)
	}
	if c.config.SessionStore != nil {
		storeContext, cancelStore := context.WithTimeout(
			context.Background(), sessionStoreTimeout,
		)
		err := c.config.SessionStore.DeleteMQTTInflight(storeContext, packetID)
		cancelStore()
		if err != nil {
			return fmt.Errorf("delete MQTT QoS 1 packet after PUBACK: %w", err)
		}
	}
	c.mu.Lock()
	if c.pending[packetID] == pending {
		delete(c.pending, packetID)
	}
	c.mu.Unlock()
	var result error
	if reason >= 0x80 {
		result = fmt.Errorf("MQTT PUBACK rejected publish with reason 0x%02x", reason)
	}
	notifyPending([]*publishPending{pending}, result)
	return nil
}

func validatePingresp(value packet) error {
	if value.header != 0xd0 || len(value.body) != 0 {
		return fmt.Errorf("%w: malformed PINGRESP", ErrProtocol)
	}
	return nil
}

func (c *Client) handlePublishPacket(connection net.Conn, writeMu *sync.Mutex, value packet) error {
	message, packetID, err := parsePublish(value)
	if err != nil {
		return err
	}
	if message.Topic != c.config.SubscribeTopic {
		return fmt.Errorf("%w: broker published unauthorized topic %q", ErrProtocol, message.Topic)
	}
	if c.config.OnMessage != nil {
		if err := c.config.OnMessage(message); err != nil {
			return fmt.Errorf("reject inbound MQTT message before PUBACK: %w", err)
		}
	}
	if message.QoS == 1 {
		body := binary.BigEndian.AppendUint16(nil, packetID)
		if writeMu != nil {
			writeMu.Lock()
			defer writeMu.Unlock()
			_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
			defer connection.SetWriteDeadline(time.Time{})
		}
		if err := writePacket(connection, 0x40, body, c.config.MaximumPacketSize); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) keepAlive(current *session, now time.Time) error {
	current.pingMu.Lock()
	defer current.pingMu.Unlock()
	if !current.pingAt.IsZero() {
		if now.Sub(current.pingAt) >= c.config.KeepAlive {
			return errors.New("MQTT PINGRESP timeout")
		}
		return nil
	}
	lastWrite := time.Unix(0, current.lastWrite.Load())
	if now.Sub(lastWrite) < c.config.KeepAlive/2 {
		return nil
	}
	current.writeMu.Lock()
	_ = current.connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := writePacket(current.connection, 0xc0, nil, c.config.MaximumPacketSize)
	_ = current.connection.SetWriteDeadline(time.Time{})
	if err == nil {
		current.lastWrite.Store(now.UnixNano())
		current.pingAt = now
	}
	current.writeMu.Unlock()
	return err
}

func (c *Client) writePending(current *session, pending *publishPending, duplicate bool) error {
	return c.writePublish(current, pending.packetID, Message{
		Topic: pending.topic, Payload: pending.payload, QoS: 1, Retain: pending.retain,
		ApplicationKey: pending.applicationKey,
	}, duplicate)
}

func (c *Client) writePublish(current *session, packetID uint16, message Message, duplicate bool) error {
	body, err := publishBody(packetID, message)
	if err != nil {
		return err
	}
	header := byte(0x30)
	if message.Retain {
		header |= 0x01
	}
	if message.QoS == 1 {
		header |= 0x02
	}
	if duplicate {
		header |= 0x08
	}
	current.writeMu.Lock()
	defer current.writeMu.Unlock()
	_ = current.connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
	defer current.connection.SetWriteDeadline(time.Time{})
	if err := writePacket(current.connection, header, body, c.config.MaximumPacketSize); err != nil {
		return err
	}
	current.lastWrite.Store(time.Now().UnixNano())
	return nil
}

func (c *Client) sendDisconnect(current *session) error {
	current.writeMu.Lock()
	defer current.writeMu.Unlock()
	_ = current.connection.SetWriteDeadline(time.Now().Add(time.Second))
	defer current.connection.SetWriteDeadline(time.Time{})
	// Normal disconnect with a zero-length property set.
	return writePacket(current.connection, 0xe0, []byte{0x00, 0x00}, c.config.MaximumPacketSize)
}

func (c *Client) connectBody() ([]byte, error) {
	var body []byte
	var err error
	body, err = appendUTF(body, "MQTT")
	if err != nil {
		return nil, err
	}
	body = append(body, 0x05)
	flags := byte(0x80) // username; Clean Start deliberately remains false.
	flags |= 0x04 | (c.config.Will.QoS << 3)
	if c.config.Will.Retain {
		flags |= 0x20
	}
	body = append(body, flags)
	body = binary.BigEndian.AppendUint16(body, uint16(c.config.KeepAlive/time.Second))
	properties := []byte{0x11}
	properties = binary.BigEndian.AppendUint32(properties, c.config.SessionExpiry)
	properties = append(properties, 0x21)
	properties = binary.BigEndian.AppendUint16(properties, c.config.ReceiveMaximum)
	properties = append(properties, 0x27)
	properties = binary.BigEndian.AppendUint32(properties, uint32(c.config.MaximumPacketSize))
	propertyLength, _ := encodeVarInt(len(properties))
	body = append(body, propertyLength...)
	body = append(body, properties...)
	body, err = appendUTF(body, c.config.ClientID)
	if err != nil {
		return nil, err
	}
	body = append(body, 0x00) // Will properties length.
	body, err = appendUTF(body, c.config.Will.Topic)
	if err != nil {
		return nil, err
	}
	if len(c.config.Will.Payload) > 65535 {
		return nil, errors.New("MQTT Will payload is too large")
	}
	body = binary.BigEndian.AppendUint16(body, uint16(len(c.config.Will.Payload)))
	body = append(body, c.config.Will.Payload...)
	body, err = appendUTF(body, c.config.Username)
	return body, err
}

func subscribeBody(packetID uint16, topic string) []byte {
	body := binary.BigEndian.AppendUint16(nil, packetID)
	body = append(body, 0x00)
	body, _ = appendUTF(body, topic)
	return append(body, 0x01)
}

func publishBody(packetID uint16, message Message) ([]byte, error) {
	if message.QoS > 1 {
		return nil, errors.New("only QoS 0 and QoS 1 are supported")
	}
	body, err := appendUTF(nil, message.Topic)
	if err != nil {
		return nil, err
	}
	if message.QoS == 1 {
		if packetID == 0 {
			return nil, errors.New("QoS 1 publish requires a packet id")
		}
		body = binary.BigEndian.AppendUint16(body, packetID)
	}
	body = append(body, 0x00) // property length
	body = append(body, message.Payload...)
	return body, nil
}

func validatePublishSize(packetID uint16, message Message, maximum int) error {
	body, err := publishBody(packetID, message)
	if err != nil {
		return err
	}
	remaining, err := encodeVarInt(len(body))
	if err != nil {
		return err
	}
	size := 1 + len(remaining) + len(body)
	if size > maximum {
		return fmt.Errorf("MQTT packet is %d bytes, maximum is %d", size, maximum)
	}
	return nil
}

func parseConnack(value packet) (bool, error) {
	if value.header != 0x20 || len(value.body) < 3 {
		return false, fmt.Errorf("%w: malformed CONNACK", ErrProtocol)
	}
	if value.body[0]&0xfe != 0 {
		return false, fmt.Errorf("%w: invalid CONNACK acknowledge flags", ErrProtocol)
	}
	if value.body[1] != 0 {
		return false, fmt.Errorf("MQTT CONNACK rejected connection with reason 0x%02x", value.body[1])
	}
	rest, err := consumeProperties(value.body[2:], connackProperties)
	if err != nil || len(rest) != 0 {
		return false, fmt.Errorf("%w: malformed CONNACK properties: %v", ErrProtocol, err)
	}
	return value.body[0]&0x01 != 0, nil
}

func parseSuback(value packet, expectedPacketID uint16) error {
	if value.header != 0x90 || len(value.body) < 4 {
		return fmt.Errorf("%w: malformed SUBACK", ErrProtocol)
	}
	if binary.BigEndian.Uint16(value.body[:2]) != expectedPacketID {
		return fmt.Errorf("%w: SUBACK packet id mismatch", ErrProtocol)
	}
	rest, err := consumeProperties(value.body[2:], subackProperties)
	if err != nil || len(rest) != 1 {
		return fmt.Errorf("%w: malformed SUBACK properties/reasons: %v", ErrProtocol, err)
	}
	reason := rest[0]
	if reason >= 0x80 {
		return fmt.Errorf("MQTT SUBACK rejected subscription with reason 0x%02x", reason)
	}
	if reason != 1 {
		return fmt.Errorf("%w: broker did not grant required QoS 1 (reason 0x%02x)", ErrProtocol, reason)
	}
	return nil
}

func parsePuback(value packet) (uint16, byte, error) {
	if value.header != 0x40 || len(value.body) < 2 {
		return 0, 0, fmt.Errorf("%w: malformed PUBACK", ErrProtocol)
	}
	packetID := binary.BigEndian.Uint16(value.body[:2])
	if packetID == 0 {
		return 0, 0, fmt.Errorf("%w: PUBACK packet id is zero", ErrProtocol)
	}
	if len(value.body) == 2 {
		return packetID, 0, nil
	}
	reason := value.body[2]
	if len(value.body) == 3 {
		return packetID, reason, nil
	}
	rest, err := consumeProperties(value.body[3:], pubackProperties)
	if err != nil || len(rest) != 0 {
		return 0, 0, fmt.Errorf("%w: malformed PUBACK properties: %v", ErrProtocol, err)
	}
	return packetID, reason, nil
}

func parsePublish(value packet) (Message, uint16, error) {
	qos := (value.header >> 1) & 0x03
	if value.header>>4 != 3 || qos == 3 || qos == 2 {
		return Message{}, 0, fmt.Errorf("%w: invalid/unsupported PUBLISH QoS", ErrProtocol)
	}
	topic, rest, err := consumeUTF(value.body)
	if err != nil || topic == "" {
		return Message{}, 0, fmt.Errorf("%w: malformed PUBLISH topic", ErrProtocol)
	}
	var packetID uint16
	if qos == 1 {
		if len(rest) < 2 {
			return Message{}, 0, ioUnexpectedEOF()
		}
		packetID = binary.BigEndian.Uint16(rest[:2])
		if packetID == 0 {
			return Message{}, 0, fmt.Errorf("%w: PUBLISH packet id is zero", ErrProtocol)
		}
		rest = rest[2:]
	}
	rest, err = consumeProperties(rest, publishProperties)
	if err != nil {
		return Message{}, 0, fmt.Errorf("%w: malformed PUBLISH properties: %v", ErrProtocol, err)
	}
	return Message{
		Topic: topic, Payload: append([]byte{}, rest...), QoS: qos, Retain: value.header&0x01 != 0,
	}, packetID, nil
}

func ioUnexpectedEOF() error {
	return fmt.Errorf("%w: unexpected end of packet", ErrProtocol)
}

func (c *Client) reserveSubscriptionID() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, _ := c.allocatePacketIDLocked()
	c.reserved[id] = true
	return id
}

func (c *Client) releaseSubscriptionID(id uint16) {
	c.mu.Lock()
	delete(c.reserved, id)
	c.mu.Unlock()
}

func (c *Client) allocatePacketIDLocked() (uint16, error) {
	for count := 0; count < 65535; count++ {
		c.nextID++
		if c.nextID == 0 {
			c.nextID = 1
		}
		_, pending := c.pending[c.nextID]
		if !pending && !c.reserved[c.nextID] {
			return c.nextID, nil
		}
	}
	return 0, errors.New("all MQTT packet identifiers are in use")
}

func (c *Client) shutdown(cause error) {
	c.mu.Lock()
	c.closed = true
	current := c.session
	c.session = nil
	pending := make([]*publishPending, 0, len(c.pending))
	for _, item := range c.pending {
		pending = append(pending, item)
	}
	c.pending = make(map[uint16]*publishPending)
	c.reserved = make(map[uint16]bool)
	c.mu.Unlock()
	if current != nil {
		_ = current.connection.Close()
	}
	notifyPending(pending, cause)
}

func notifyPending(pending []*publishPending, cause error) {
	for _, item := range pending {
		if item == nil || item.done == nil {
			continue
		}
		select {
		case item.done <- cause:
		default:
		}
	}
}

func (c *Client) report(err error) {
	if c.config.OnError != nil && err != nil {
		c.config.OnError(err)
	}
}

func defaultReconnectDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	schedule := [...]time.Duration{
		time.Second, 2 * time.Second, 5 * time.Second,
		10 * time.Second, 30 * time.Second, 60 * time.Second,
	}
	if attempt > len(schedule) {
		attempt = len(schedule)
	}
	base := schedule[attempt-1]
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return base
	}
	// Uniform +/-20% jitter prevents a fleet from reconnecting in lockstep.
	fraction := float64(binary.BigEndian.Uint64(raw[:])) / float64(^uint64(0))
	factor := 0.8 + fraction*0.4
	return time.Duration(float64(base) * factor)
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
