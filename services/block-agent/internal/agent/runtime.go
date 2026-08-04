// Package agent owns the local HMI runtime session.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
	"golang.org/x/net/websocket"
)

const (
	protocolVersion = "1.0"
	maxWSFrameBytes = 1 << 20
	wsQueueSize     = 128
)

type Runtime struct {
	address string
	now     func() time.Time
	store   *pointstore.Store
	server  *http.Server

	mu    sync.Mutex
	owner *wsClient
}

// NewLocalRuntime creates the empty local runtime. It performs no PLC, MQTT
// or SQLite work until a WebSocket owner supplies a complete point table.
func NewLocalRuntime(address string, now func() time.Time) (*Runtime, error) {
	if err := validateLoopbackAddress(address); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	runtime := &Runtime{address: address, now: now, store: pointstore.New()}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", runtime.health)
	mux.Handle("/ws", websocket.Server{Handler: websocket.Handler(runtime.serveWS), Handshake: checkLocalOrigin})
	runtime.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return runtime, nil
}

// Run listens only on 127.0.0.1. It remains idle when the kiosk is absent.
func (r *Runtime) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", r.address)
	if err != nil {
		return err
	}
	return r.ServeListener(ctx, listener)
}

// ServeListener is split out so tests can use a loopback port selected by the
// operating system. Production callers use Run.
func (r *Runtime) ServeListener(ctx context.Context, listener net.Listener) error {
	if err := validateLoopbackListener(listener); err != nil {
		_ = listener.Close()
		return err
	}
	errorsChannel := make(chan error, 1)
	go func() { errorsChannel <- r.server.Serve(listener) }()
	select {
	case <-ctx.Done():
		r.StopSession()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := r.server.Shutdown(shutdownContext); err != nil {
			return err
		}
		return nil
	case err := <-errorsChannel:
		r.StopSession()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (r *Runtime) Store() *pointstore.Store {
	return r.store
}

func (r *Runtime) Active() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owner != nil
}

// StopSession is the hook for WebSocket disconnect and future login expiry.
func (r *Runtime) StopSession() {
	r.mu.Lock()
	owner := r.owner
	r.owner = nil
	r.store.Clear()
	r.mu.Unlock()
	if owner != nil {
		owner.close()
	}
}

// UpdateConfirmed is reserved for the later PLCWorker. It only accepts
// confirmed absolute values and broadcasts the values that actually changed.
func (r *Runtime) UpdateConfirmed(values map[string]pointstore.PointValue) error {
	r.mu.Lock()
	owner := r.owner
	r.mu.Unlock()
	if owner == nil {
		return errors.New("runtime session is not active")
	}
	changed, err := r.store.Update(values)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		return nil
	}
	owner.enqueue(eventEnvelope(r.now(), "points.changed", changed), false)
	return nil
}

func (r *Runtime) health(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (r *Runtime) serveWS(connection *websocket.Conn) {
	connection.MaxPayloadBytes = maxWSFrameBytes
	client := newWSClient(connection)
	defer r.disconnect(client)
	defer client.close()
	go client.writeLoop()

	configured := false
	for {
		var raw []byte
		if err := websocket.Message.Receive(connection, &raw); err != nil {
			return
		}
		messageType, err := readMessageType(raw)
		if err != nil {
			client.enqueue(errorEnvelope(r.now(), "", "INVALID_REQUEST", err.Error()), true)
			<-client.done
			return
		}
		if !configured {
			if messageType != "runtime.configure" {
				client.enqueue(errorEnvelope(r.now(), "", "INVALID_REQUEST", "first WebSocket message must be runtime.configure"), true)
				<-client.done
				return
			}
			if err := r.configure(client, raw); err != nil {
				client.enqueue(errorEnvelope(r.now(), "", "INVALID_REQUEST", err.Error()), false)
				continue
			}
			configured = true
			continue
		}

		switch messageType {
		case "runtime.configure":
			client.enqueue(errorEnvelope(r.now(), "", "INVALID_REQUEST", "runtime.configure is only allowed as the first WebSocket message"), false)
		case "points.snapshot.get":
			if err := validateSnapshotRequest(raw); err != nil {
				client.enqueue(errorEnvelope(r.now(), "", "INVALID_REQUEST", err.Error()), false)
				continue
			}
			client.enqueue(snapshotEnvelope(r.now(), r.store.Snapshot()), false)
		case "point.command":
			r.handlePointCommand(client, raw)
		default:
			client.enqueue(errorEnvelope(r.now(), "", "UNKNOWN_MESSAGE", "message type is not supported"), false)
		}
	}
}

func (r *Runtime) configure(client *wsClient, raw []byte) error {
	var request struct {
		ProtocolVersion string                          `json:"protocolVersion"`
		Type            string                          `json:"type"`
		ScanIntervalMs  int                             `json:"scanIntervalMs"`
		Points          []runtimeconfig.PointDefinition `json:"points"`
	}
	if err := decodeAllowed(raw, &request, "protocolVersion", "type", "timestamp", "scanIntervalMs", "points"); err != nil {
		return err
	}
	if request.Type != "runtime.configure" {
		return errors.New("type must be runtime.configure")
	}
	if request.ProtocolVersion != "" && request.ProtocolVersion != protocolVersion {
		return fmt.Errorf("protocolVersion must be %s", protocolVersion)
	}
	config, err := runtimeconfig.Normalize(runtimeconfig.Config{ScanIntervalMs: request.ScanIntervalMs, Points: request.Points})
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owner != nil && r.owner != client {
		return errors.New("another local HMI session is already active")
	}
	if err := r.store.Replace(config); err != nil {
		return err
	}
	r.owner = client
	client.enqueue(configuredEnvelope(r.now(), len(config.Points)), false)
	client.enqueue(snapshotEnvelope(r.now(), r.store.Snapshot()), false)
	return nil
}

func (r *Runtime) handlePointCommand(client *wsClient, raw []byte) {
	var request struct {
		ProtocolVersion string           `json:"protocolVersion"`
		Type            string           `json:"type"`
		RequestID       string           `json:"requestId"`
		PointID         string           `json:"pointId"`
		Action          string           `json:"action"`
		Value           *json.RawMessage `json:"value"`
	}
	if err := decodeAllowed(raw, &request, "protocolVersion", "type", "timestamp", "requestId", "pointId", "action", "value"); err != nil {
		client.enqueue(pointErrorEnvelope(r.now(), request.RequestID, request.PointID, "INVALID_REQUEST", err.Error()), false)
		return
	}
	if request.ProtocolVersion != "" && request.ProtocolVersion != protocolVersion {
		client.enqueue(pointErrorEnvelope(r.now(), request.RequestID, request.PointID, "INVALID_REQUEST", "protocolVersion is unsupported"), false)
		return
	}
	if request.Type != "point.command" || request.PointID == "" || request.Action == "" {
		client.enqueue(pointErrorEnvelope(r.now(), request.RequestID, request.PointID, "INVALID_REQUEST", "pointId and action are required"), false)
		return
	}
	definition, exists := r.store.Definition(request.PointID)
	if !exists {
		client.enqueue(pointErrorEnvelope(r.now(), request.RequestID, request.PointID, "POINT_NOT_FOUND", "point is not configured"), false)
		return
	}
	if !allowsAction(definition, request.Action) {
		client.enqueue(pointErrorEnvelope(r.now(), request.RequestID, request.PointID, "POINT_NOT_WRITABLE", "point does not allow this action"), false)
		return
	}
	if request.Action == "set" {
		if request.Value == nil {
			client.enqueue(pointErrorEnvelope(r.now(), request.RequestID, request.PointID, "INVALID_REQUEST", "set requires value"), false)
			return
		}
		var value any
		if err := json.Unmarshal(*request.Value, &value); err != nil || runtimeconfig.ValidateValue(definition.Type, value) != nil {
			client.enqueue(pointErrorEnvelope(r.now(), request.RequestID, request.PointID, "INVALID_REQUEST", "value does not match the configured point type"), false)
			return
		}
	} else if request.Value != nil {
		client.enqueue(pointErrorEnvelope(r.now(), request.RequestID, request.PointID, "INVALID_REQUEST", "only set may carry value"), false)
		return
	}

	// The real FIFO PLCWorker is intentionally outside this milestone. No
	// command updates PointStore until a PLC write and fresh readback exist.
	client.enqueue(pointErrorEnvelope(r.now(), request.RequestID, request.PointID, "PLC_NOT_CONNECTED", "PLC worker is disabled"), false)
}

func (r *Runtime) disconnect(client *wsClient) {
	r.mu.Lock()
	if r.owner == client {
		r.owner = nil
		r.store.Clear()
	}
	r.mu.Unlock()
}

func allowsAction(definition runtimeconfig.PointDefinition, action string) bool {
	if definition.Access == "read" || definition.Write == nil {
		return false
	}
	switch definition.Write.Mode {
	case "set":
		return action == "set"
	case "pulse":
		return action == "pulse"
	case "momentary":
		return action == "press" || action == "release"
	case "toggle":
		return action == "toggle"
	default:
		return false
	}
}

func validateSnapshotRequest(raw []byte) error {
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
		Type            string `json:"type"`
	}
	if err := decodeAllowed(raw, &request, "protocolVersion", "type", "timestamp", "requestId"); err != nil {
		return err
	}
	if request.Type != "points.snapshot.get" {
		return errors.New("type must be points.snapshot.get")
	}
	if request.ProtocolVersion != "" && request.ProtocolVersion != protocolVersion {
		return fmt.Errorf("protocolVersion must be %s", protocolVersion)
	}
	return nil
}

func decodeAllowed(raw []byte, target any, allowed ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("message must be a JSON object")
	}
	allowedFields := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedFields[name] = struct{}{}
	}
	for name := range fields {
		if _, exists := allowedFields[name]; !exists {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return json.Unmarshal(raw, target)
}

func readMessageType(raw []byte) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", errors.New("message must be a JSON object")
	}
	if fields == nil {
		return "", errors.New("message must be a JSON object")
	}
	rawType, exists := fields["type"]
	if !exists {
		return "", errors.New("type is required")
	}
	var messageType string
	if err := json.Unmarshal(rawType, &messageType); err != nil || messageType == "" {
		return "", errors.New("type is required")
	}
	return messageType, nil
}

func validateLoopbackAddress(address string) error {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("local HTTP address must be 127.0.0.1:PORT")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("local HTTP port is invalid")
	}
	return nil
}

func validateLoopbackListener(listener net.Listener) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.ParseIP("127.0.0.1")) {
		return errors.New("local HTTP listener must bind 127.0.0.1")
	}
	return nil
}

func checkLocalOrigin(config *websocket.Config, request *http.Request) error {
	origin, err := websocket.Origin(config, request)
	if err != nil {
		return err
	}
	if origin == nil {
		return nil // non-browser local diagnostics do not send Origin
	}
	if origin.Scheme != "http" || origin.Host != config.Location.Host || origin.Hostname() != "127.0.0.1" {
		return errors.New("WebSocket Origin is not local")
	}
	return nil
}

type wsClient struct {
	connection *websocket.Conn
	send       chan outboundMessage
	done       chan struct{}
	closeOnce  sync.Once
}

type outboundMessage struct {
	payload    []byte
	closeAfter bool
}

func newWSClient(connection *websocket.Conn) *wsClient {
	return &wsClient{connection: connection, send: make(chan outboundMessage, wsQueueSize), done: make(chan struct{})}
}

func (c *wsClient) enqueue(value any, closeAfter bool) bool {
	payload, err := json.Marshal(value)
	if err != nil {
		return false
	}
	message := outboundMessage{payload: payload, closeAfter: closeAfter}
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- message:
		return true
	case <-c.done:
		return false
	default:
		c.close()
		return false
	}
}

func (c *wsClient) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case message := <-c.send:
			if err := websocket.Message.Send(c.connection, string(message.payload)); err != nil {
				c.close()
				return
			}
			if message.closeAfter {
				c.close()
				return
			}
		}
	}
}

func (c *wsClient) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.connection.Close()
	})
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func baseEnvelope(now func() time.Time, messageType string) map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"type":            messageType,
		"timestamp":       now().UTC().Format(time.RFC3339Nano),
	}
}

func configuredEnvelope(now func() time.Time, pointCount int) map[string]any {
	envelope := baseEnvelope(now, "runtime.configured")
	envelope["pointCount"] = pointCount
	return envelope
}

func snapshotEnvelope(now func() time.Time, values map[string]pointstore.PointValue) map[string]any {
	envelope := baseEnvelope(now, "points.snapshot")
	envelope["values"] = values
	return envelope
}

func eventEnvelope(now func() time.Time, messageType string, values map[string]pointstore.PointValue) map[string]any {
	envelope := baseEnvelope(now, messageType)
	envelope["values"] = values
	return envelope
}

func errorEnvelope(now func() time.Time, requestID, code, message string) map[string]any {
	envelope := baseEnvelope(now, "error")
	if requestID != "" {
		envelope["requestId"] = requestID
	}
	envelope["error"] = protocolError{Code: code, Message: message}
	return envelope
}

func pointErrorEnvelope(now func() time.Time, requestID, pointID, code, message string) map[string]any {
	envelope := baseEnvelope(now, "point.result")
	if requestID != "" {
		envelope["requestId"] = requestID
	}
	envelope["success"] = false
	envelope["pointId"] = pointID
	envelope["error"] = protocolError{Code: code, Message: message}
	return envelope
}
