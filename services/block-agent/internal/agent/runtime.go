// Package agent owns the local HMI runtime session.
package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"block.local/block-agent/internal/alarmhistory"
	"block.local/block-agent/internal/auth"
	"block.local/block-agent/internal/maintenance"
	"block.local/block-agent/internal/mqttv2"
	"block.local/block-agent/internal/plcworker"
	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
	"block.local/block-agent/internal/sshbootstrap"
	"block.local/block-agent/internal/wifi"
	"golang.org/x/net/websocket"
)

const (
	protocolVersion = "1.0"
	maxWSFrameBytes = 1 << 20
	wsQueueSize     = 128
)

type Runtime struct {
	address         string
	now             func() time.Time
	store           *pointstore.Store
	server          *http.Server
	factory         plcworker.Factory
	auth            *auth.Service
	mqtt            MQTTOptions
	alarms          *alarmhistory.Service
	plcEndpointPath string
	production      *maintenance.Store
	wifiBackend     wifi.Backend
	wifiInterface   string
	testPlaintext   bool
	alarmID         atomic.Uint64

	mu         sync.Mutex
	owner      *wsClient
	session    *runtimeSession
	scanMu     sync.Mutex
	scanCancel context.CancelFunc
}

type runtimeSession struct {
	config        runtimeconfig.Config
	worker        *plcworker.Worker
	cancel        context.CancelFunc
	mqtt          *mqttv2.Session
	mqttCancel    context.CancelFunc
	alarms        map[string]bool
	broadcasts    bool
	deviceID      string
	disconnecting bool
}

// MQTTOptions defaults to disabled so a Block retains full local PLC/HMI
// behavior when BDM or Wi-Fi is absent.
type MQTTOptions struct {
	Enabled    bool
	Connection mqttv2.ConnectionConfig
}

type RuntimeOptions struct {
	AlarmStore      alarmhistory.Store
	MQTT            MQTTOptions
	PLCEndpointPath string
	MaintenancePath string
	WiFiBackend     wifi.Backend
	WiFiInterface   string
}

// NewLocalRuntime creates the empty local runtime. It performs no PLC, MQTT
// or SQLite work until a WebSocket owner supplies a complete point table.
func NewLocalRuntime(address string, now func() time.Time) (*Runtime, error) {
	return NewLocalRuntimeWithServices(address, now, nil, nil, nil)
}

// NewLocalRuntimeWithWorkerFactory is used by the selected PLC connection
// path. Keeping the factory at the runtime edge lets a session create exactly
// one worker/connection, while an idle kiosk still creates none.
func NewLocalRuntimeWithWorkerFactory(address string, now func() time.Time, factory plcworker.Factory) (*Runtime, error) {
	return NewLocalRuntimeWithServices(address, now, factory, nil, nil)
}

// NewLocalRuntimeWithServices attaches the local account service and the HMI
// build owned by the frontend. A nil HMI filesystem keeps the runtime idle
// until a bundle is supplied by deployment.
func NewLocalRuntimeWithServices(address string, now func() time.Time, factory plcworker.Factory, hmi fs.FS, authService *auth.Service) (*Runtime, error) {
	return NewLocalRuntimeWithOptions(address, now, factory, hmi, authService, RuntimeOptions{})
}

// NewLocalRuntimeWithOptions adds optional local alarm history and MQTTS-v2
// integration. Both are disabled by default; neither is required for the
// loopback HMI or PLC worker to run.
func NewLocalRuntimeWithOptions(address string, now func() time.Time, factory plcworker.Factory, hmi fs.FS, authService *auth.Service, options RuntimeOptions) (*Runtime, error) {
	if err := validateLoopbackAddress(address); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	production, err := maintenance.Open(options.MaintenancePath)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		address: address, now: now, store: pointstore.New(), factory: factory, auth: authService,
		mqtt: options.MQTT, plcEndpointPath: options.PLCEndpointPath, production: production,
		wifiBackend: options.WiFiBackend, wifiInterface: options.WiFiInterface,
	}
	if options.AlarmStore != nil {
		runtime.alarms = alarmhistory.New(options.AlarmStore, runtimeAlarmNotifier{runtime: runtime})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", runtime.health)
	mux.HandleFunc("/api/auth/initial-admin", runtime.bootstrap)
	mux.HandleFunc("/api/auth/login", runtime.login)
	mux.HandleFunc("/api/auth/password", runtime.changePassword)
	mux.HandleFunc("/api/config/session", runtime.sessionPolicy)
	mux.HandleFunc("/api/maintenance/production", runtime.productionSettings)
	mux.HandleFunc("/api/maintenance/connectivity", runtime.connectivity)
	mux.HandleFunc("/api/maintenance/wifi/connect", runtime.connectWiFi)
	mux.Handle("/ws", websocket.Server{Handler: websocket.Handler(runtime.serveWS), Handshake: runtime.checkHandshake})
	mux.Handle("/", staticHMI(hmi))
	runtime.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       30 * time.Second,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return runtime, nil
}

// RunTLS listens only on 127.0.0.1 and accepts only TLS 1.2 or newer. It
// remains idle when the kiosk is absent.
func (r *Runtime) RunTLS(ctx context.Context, certificatePath, privateKeyPath string) error {
	if certificatePath == "" || privateKeyPath == "" {
		return errors.New("local HTTPS certificate and private key are required")
	}
	certificate, err := tls.LoadX509KeyPair(certificatePath, privateKeyPath)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", r.address)
	if err != nil {
		return err
	}
	return r.serveTLSListener(ctx, listener, certificate)
}

// serveTLSListener is split out so package tests can use a loopback port
// selected by the operating system. Production callers use RunTLS.
func (r *Runtime) serveTLSListener(ctx context.Context, listener net.Listener, certificate tls.Certificate) error {
	if err := validateLoopbackListener(listener); err != nil {
		_ = listener.Close()
		return err
	}
	r.testPlaintext = false
	return r.serve(ctx, func() error {
		return sshbootstrap.ServeTLSListener(r.server, listener, certificate)
	})
}

// serveListener is retained only for package-level handler tests. Production
// business traffic must use RunTLS.
func (r *Runtime) serveListener(ctx context.Context, listener net.Listener) error {
	if err := validateLoopbackListener(listener); err != nil {
		_ = listener.Close()
		return err
	}
	r.testPlaintext = true
	return r.serve(ctx, func() error { return r.server.Serve(listener) })
}

func (r *Runtime) serve(ctx context.Context, serve func() error) error {
	errorsChannel := make(chan error, 1)
	go func() { errorsChannel <- serve() }()
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
	session := r.session
	r.owner = nil
	r.session = nil
	worker, cancel := detachWorkerLocked(session)
	mqttCancel := detachMQTTLocked(session)
	r.mu.Unlock()
	r.cancelScan()
	if mqttCancel != nil {
		mqttCancel()
	}
	stopWorker(worker, cancel)
	r.store.Clear()
	if owner != nil {
		owner.close()
	}
}

// UpdateConfirmed is reserved for the later PLCWorker. It only accepts
// confirmed absolute values and broadcasts the values that actually changed.
func (r *Runtime) UpdateConfirmed(values map[string]pointstore.PointValue) error {
	r.mu.Lock()
	owner := r.owner
	session := r.session
	r.mu.Unlock()
	if owner == nil {
		return errors.New("runtime session is not active")
	}
	return r.applyValues(session, owner, true, values)
}

func (r *Runtime) updateFromWorker(session *runtimeSession, worker *plcworker.Worker, values map[string]pointstore.PointValue) error {
	r.mu.Lock()
	if r.session != session || session.worker != worker || r.owner == nil {
		r.mu.Unlock()
		return nil
	}
	owner := r.owner
	broadcasts := session.broadcasts
	r.mu.Unlock()

	return r.applyValues(session, owner, broadcasts, values)
}

func (r *Runtime) applyValues(session *runtimeSession, owner *wsClient, broadcasts bool, values map[string]pointstore.PointValue) error {
	changed, err := r.store.Update(values)
	if err != nil || len(changed) == 0 {
		return err
	}
	if err := r.recordAlarmChanges(session, changed); err != nil {
		return err
	}
	r.mu.Lock()
	mqtt := (*mqttv2.Session)(nil)
	if r.session == session && session != nil {
		mqtt = session.mqtt
	}
	r.mu.Unlock()
	if mqtt != nil {
		mqtt.ObserveSnapshot(toMQTTSnapshot(r.store.Snapshot(), r.now))
	}
	if broadcasts {
		owner.enqueue(eventEnvelope(r.now, "points.changed", changed), false)
	}
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
	client, err := r.newWSClient(connection)
	if err != nil {
		_ = connection.Close()
		return
	}
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
			client.enqueue(errorEnvelope(r.now, "", "INVALID_REQUEST", err.Error()), true)
			<-client.done
			return
		}
		if !configured {
			if messageType != "runtime.configure" {
				client.enqueue(errorEnvelope(r.now, "", "INVALID_REQUEST", "first WebSocket message must be runtime.configure"), true)
				<-client.done
				return
			}
			if err := r.configure(client, raw); err != nil {
				client.enqueue(errorEnvelope(r.now, "", "INVALID_REQUEST", err.Error()), false)
				continue
			}
			configured = true
			continue
		}

		switch messageType {
		case "runtime.configure":
			client.enqueue(errorEnvelope(r.now, "", "INVALID_REQUEST", "runtime.configure is only allowed as the first WebSocket message"), false)
		case "points.snapshot.get":
			requestID, err := validateSnapshotRequest(raw)
			if err != nil {
				client.enqueue(errorEnvelope(r.now, "", "INVALID_REQUEST", err.Error()), false)
				continue
			}
			_ = requestID
			client.enqueue(snapshotEnvelope(r.now, r.store.Snapshot()), false)
		case "plc.scan":
			r.handlePLCScan(client, raw)
		case "plc.connect":
			r.handlePLCConnect(client, raw)
		case "plc.disconnect":
			r.handlePLCDisconnect(client, raw)
		case "point.command":
			r.handlePointCommand(client, raw)
		default:
			client.enqueue(errorEnvelope(r.now, "", "UNKNOWN_MESSAGE", "message type is not supported"), false)
		}
	}
}

func (r *Runtime) configure(client *wsClient, raw []byte) error {
	var request struct {
		ProtocolVersion string                          `json:"protocolVersion"`
		Type            string                          `json:"type"`
		Timestamp       string                          `json:"timestamp"`
		RequestID       string                          `json:"requestId"`
		ScanIntervalMs  int                             `json:"scanIntervalMs"`
		Points          []runtimeconfig.PointDefinition `json:"points"`
	}
	if err := decodeAllowed(raw, &request, "protocolVersion", "type", "timestamp", "requestId", "scanIntervalMs", "points"); err != nil {
		return err
	}
	if request.Type != "runtime.configure" {
		return errors.New("type must be runtime.configure")
	}
	if err := validateRequestEnvelope(request.ProtocolVersion, request.Timestamp); err != nil {
		return err
	}
	config, err := runtimeconfig.Normalize(runtimeconfig.Config{ScanIntervalMs: request.ScanIntervalMs, Points: request.Points})
	if err != nil {
		return err
	}
	savedEndpoint, hasSavedEndpoint, err := loadPLCEndpoint(r.plcEndpointPath)
	if err != nil {
		return fmt.Errorf("load saved PLC endpoint: %w", err)
	}
	mqttSession, mqttContext, mqttCancel, err := r.newMQTTSession()
	if err != nil {
		return err
	}
	session := &runtimeSession{config: config, mqtt: mqttSession, mqttCancel: mqttCancel, alarms: make(map[string]bool)}

	r.mu.Lock()
	if r.owner != nil && r.owner != client {
		r.mu.Unlock()
		return errors.New("another local HMI session is already active")
	}
	if err := r.store.Replace(config); err != nil {
		r.mu.Unlock()
		return err
	}
	r.owner = client
	r.session = session
	client.enqueue(configuredEnvelope(r.now, config.ScanIntervalMs), false)
	r.mu.Unlock()
	if mqttSession != nil {
		go mqttSession.Run(mqttContext)
	}
	if !hasSavedEndpoint {
		return nil
	}
	r.connectPLC(client, "", savedEndpoint, false)
	return nil
}

func (r *Runtime) handlePointCommand(client *wsClient, raw []byte) {
	var request struct {
		ProtocolVersion string           `json:"protocolVersion"`
		Type            string           `json:"type"`
		Timestamp       string           `json:"timestamp"`
		RequestID       string           `json:"requestId"`
		PointID         string           `json:"pointId"`
		Action          string           `json:"action"`
		Value           *json.RawMessage `json:"value"`
	}
	if err := decodeAllowed(raw, &request, "protocolVersion", "type", "timestamp", "requestId", "pointId", "action", "value"); err != nil {
		client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, "INVALID_REQUEST", err.Error()), false)
		return
	}
	if err := validateRequestEnvelope(request.ProtocolVersion, request.Timestamp); err != nil {
		client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, "INVALID_REQUEST", err.Error()), false)
		return
	}
	if request.Type != "point.command" || request.PointID == "" || request.Action == "" {
		client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, "INVALID_REQUEST", "pointId and action are required"), false)
		return
	}
	definition, exists := r.store.Definition(request.PointID)
	if !exists {
		client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, "POINT_NOT_FOUND", "point is not configured"), false)
		return
	}
	if !allowsAction(definition, request.Action) {
		client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, "POINT_NOT_WRITABLE", "point does not allow this action"), false)
		return
	}
	if request.Action == "set" {
		if request.Value == nil {
			client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, "INVALID_REQUEST", "set requires value"), false)
			return
		}
		var value any
		if err := json.Unmarshal(*request.Value, &value); err != nil || runtimeconfig.ValidateValue(definition.Type, value) != nil {
			client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, "INVALID_REQUEST", "value does not match the configured point type"), false)
			return
		}
	} else if request.Value != nil {
		client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, "INVALID_REQUEST", "only set may carry value"), false)
		return
	}

	r.mu.Lock()
	session := r.session
	active := r.owner == client
	r.mu.Unlock()
	if !active || session == nil || session.worker == nil {
		client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, "PLC_NOT_CONNECTED", "PLC worker is disabled"), false)
		return
	}
	reply, rejected, accepted := session.worker.TrySubmit(plcworker.Command{
		PointID: request.PointID, Action: request.Action, Value: valueFromRaw(request.Value),
	})
	if !accepted {
		client.enqueue(pointErrorEnvelope(r.now, request.RequestID, request.PointID, rejected.Code, rejected.Message), false)
		return
	}
	go func() {
		result := <-reply
		if result.Success {
			client.enqueue(pointSuccessEnvelope(r.now, request.RequestID, result.PointID, result.ActualValue), false)
			return
		}
		client.enqueue(pointErrorEnvelope(r.now, request.RequestID, result.PointID, result.Code, result.Message), false)
	}()
}

func (r *Runtime) disconnect(client *wsClient) {
	r.mu.Lock()
	owned := r.owner == client
	r.mu.Unlock()
	if owned {
		r.StopSession()
	}
}

func valueFromRaw(raw *json.RawMessage) any {
	if raw == nil {
		return nil
	}
	var value any
	if json.Unmarshal(*raw, &value) != nil {
		return nil
	}
	return value
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

func validateSnapshotRequest(raw []byte) (string, error) {
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
		Type            string `json:"type"`
		Timestamp       string `json:"timestamp"`
		RequestID       string `json:"requestId"`
	}
	if err := decodeAllowed(raw, &request, "protocolVersion", "type", "timestamp", "requestId"); err != nil {
		return "", err
	}
	if request.Type != "points.snapshot.get" {
		return "", errors.New("type must be points.snapshot.get")
	}
	if err := validateRequestEnvelope(request.ProtocolVersion, request.Timestamp); err != nil {
		return "", err
	}
	return request.RequestID, nil
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
		return fmt.Errorf("local HTTPS address must be 127.0.0.1:PORT")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("local HTTPS port is invalid")
	}
	return nil
}

func validateLoopbackListener(listener net.Listener) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.ParseIP("127.0.0.1")) {
		return errors.New("local HTTPS listener must bind 127.0.0.1")
	}
	return nil
}

func (r *Runtime) websocketOriginScheme() string {
	if r.testPlaintext {
		return "http"
	}
	return "https"
}

func checkLocalOrigin(config *websocket.Config, request *http.Request, scheme string) error {
	origin, err := websocket.Origin(config, request)
	if err != nil {
		return err
	}
	if origin == nil {
		return nil // non-browser local diagnostics do not send Origin
	}
	if origin.Scheme != scheme || origin.Host != config.Location.Host || origin.Hostname() != "127.0.0.1" {
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

func (r *Runtime) newWSClient(connection *websocket.Conn) (*wsClient, error) {
	return newWSClient(connection), nil
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
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func baseEnvelope(now func() time.Time, messageType string) map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"type":            messageType,
		"timestamp":       now().UTC().Format(time.RFC3339Nano),
	}
}

func configuredEnvelope(now func() time.Time, scanIntervalMs int) map[string]any {
	envelope := baseEnvelope(now, "runtime.configured")
	envelope["scanIntervalMs"] = scanIntervalMs
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
	envelope["success"] = false
	envelope["error"] = protocolError{Code: code, Message: message, Details: map[string]any{}}
	return envelope
}

func pointErrorEnvelope(now func() time.Time, requestID, pointID, code, message string) map[string]any {
	envelope := baseEnvelope(now, "point.result")
	if requestID != "" {
		envelope["requestId"] = requestID
	}
	envelope["success"] = false
	envelope["pointId"] = pointID
	envelope["error"] = protocolError{Code: code, Message: message, Details: map[string]any{}}
	return envelope
}

func plcScanResultEnvelope(now func() time.Time, requestID string, devices []plcDevice) map[string]any {
	envelope := baseEnvelope(now, "plc.scan.result")
	if requestID != "" {
		envelope["requestId"] = requestID
	}
	envelope["success"] = true
	envelope["devices"] = devices
	return envelope
}

func plcConnectionEnvelope(now func() time.Time, deviceID, state string) map[string]any {
	envelope := baseEnvelope(now, "plc.connection.changed")
	if deviceID != "" {
		envelope["deviceId"] = deviceID
	}
	envelope["state"] = state
	return envelope
}

func plcConnectResultEnvelope(now func() time.Time, requestID, deviceID string, success bool, state, code, message string) map[string]any {
	envelope := baseEnvelope(now, "plc.connect.result")
	if requestID != "" {
		envelope["requestId"] = requestID
	}
	envelope["success"] = success
	envelope["deviceId"] = deviceID
	envelope["state"] = state
	if !success {
		envelope["error"] = protocolError{Code: code, Message: message, Details: map[string]any{}}
	}
	return envelope
}

func plcDisconnectResultEnvelope(now func() time.Time, requestID string, success bool) map[string]any {
	envelope := baseEnvelope(now, "plc.disconnect.result")
	if requestID != "" {
		envelope["requestId"] = requestID
	}
	envelope["success"] = success
	envelope["state"] = "disconnected"
	return envelope
}

func pointSuccessEnvelope(now func() time.Time, requestID, pointID string, actualValue any) map[string]any {
	envelope := baseEnvelope(now, "point.result")
	if requestID != "" {
		envelope["requestId"] = requestID
	}
	envelope["success"] = true
	envelope["pointId"] = pointID
	envelope["actualValue"] = actualValue
	return envelope
}
