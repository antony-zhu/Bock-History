// Command block-e2e exercises the local Block HMI API from the device side.
// It is a test tool: it never stores configuration or credentials.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

const (
	defaultBaseURL    = "https://127.0.0.1:8444"
	defaultCAPath     = "/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt"
	defaultPointsPath = "/opt/block/current/web/assets/points.json"
	defaultScanCIDR   = "192.168.1.0/24"
	requestTimeout    = 15 * time.Second
)

type options struct {
	baseURL             string
	caPath              string
	pointsPath          string
	scanCIDR            string
	observeScanDuration time.Duration
	username            string
	password            string
	output              io.Writer
}

type pointFile struct {
	Points json.RawMessage `json:"points"`
}

type pointDefinition struct {
	PointID    string  `json:"pointId"`
	WritePoint *string `json:"writePoint"`
	Write      *struct {
		Mode string `json:"mode"`
	} `json:"write"`
}

type device struct {
	DeviceID string `json:"deviceId"`
}

type resultLine struct {
	Stage       string         `json:"stage"`
	Status      string         `json:"status"`
	Details     map[string]any `json:"details,omitempty"`
	ErrorCode   string         `json:"errorCode,omitempty"`
	Message     string         `json:"message,omitempty"`
	RequestID   string         `json:"requestId,omitempty"`
	MessageType string         `json:"type,omitempty"`
}

type workflow struct {
	base   *url.URL
	client *http.Client
	tls    *tls.Config
	ws     *websocket.Conn
	output *json.Encoder
}

type protocolFailure struct {
	code        string
	message     string
	requestID   string
	messageType string
}

func (e protocolFailure) Error() string {
	if e.code == "" {
		return "Block returned an unsuccessful response"
	}
	return "Block returned " + e.code
}

type workflowFailure struct {
	stage string
	cause error
}

func (e workflowFailure) Error() string {
	return e.stage + ": " + e.cause.Error()
}

func (e workflowFailure) Unwrap() error {
	return e.cause
}

func main() {
	baseURL := flag.String("base-url", defaultBaseURL, "Block local HTTPS base URL")
	caPath := flag.String("ca-file", defaultCAPath, "public CA PEM used to verify Block local HTTPS/WSS")
	pointsPath := flag.String("points", defaultPointsPath, "path to HMI points.json")
	scanCIDR := flag.String("scan-cidr", defaultScanCIDR, "IPv4 CIDR to scan for the PLC")
	observeScanDuration := flag.Duration("observe-scan-duration", 0, "keep the WebSocket open after the initial PLC snapshot without sending commands")
	flag.Parse()

	username := os.Getenv("BLOCK_E2E_USERNAME")
	password := os.Getenv("BLOCK_E2E_PASSWORD")
	if username == "" || password == "" {
		fmt.Fprintln(os.Stderr, "block-e2e: BLOCK_E2E_USERNAME and BLOCK_E2E_PASSWORD are required")
		os.Exit(2)
	}

	err := run(context.Background(), options{
		baseURL: *baseURL, caPath: *caPath, pointsPath: *pointsPath, scanCIDR: *scanCIDR, observeScanDuration: *observeScanDuration,
		username: username, password: password, output: os.Stdout,
	})
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(failureResult(err))
		fmt.Fprintln(os.Stderr, "block-e2e: workflow failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, options options) error {
	if options.username == "" || options.password == "" {
		return errors.New("credentials are required")
	}
	if options.output == nil {
		return errors.New("output is required")
	}
	base, err := parseBaseURL(options.baseURL)
	if err != nil {
		return atStage("startup", err)
	}
	tlsConfig, err := loadTLSConfig(options.caPath, base.Hostname())
	if err != nil {
		return atStage("startup", err)
	}
	points, definitions, err := loadPoints(options.pointsPath)
	if err != nil {
		return atStage("points.load", err)
	}
	workflow := &workflow{
		base:   base,
		client: &http.Client{Timeout: requestTimeout, Transport: &http.Transport{TLSClientConfig: tlsConfig.Clone()}},
		tls:    tlsConfig,
		output: json.NewEncoder(options.output),
	}
	if err := workflow.authenticate(ctx, options.username, options.password); err != nil {
		return err
	}
	if err := workflow.openWebSocket(); err != nil {
		return atStage("ws.connect", err)
	}
	defer workflow.ws.Close()

	if err := workflow.configure(points, len(definitions)); err != nil {
		return atStage("runtime.configure", err)
	}
	deviceID, err := workflow.scan(options.scanCIDR)
	if err != nil {
		return atStage("plc.scan", err)
	}
	if err := workflow.connect(deviceID); err != nil {
		return atStage("plc.connect", err)
	}
	if err := workflow.observe(ctx, options.observeScanDuration); err != nil {
		return atStage("plc.observe", err)
	}
	if err := workflow.runActions(definitions); err != nil {
		return atStage("point.command", err)
	}
	if err := workflow.snapshot("points.snapshot.get"); err != nil {
		return atStage("points.snapshot.get", err)
	}
	if err := workflow.disconnect(); err != nil {
		return atStage("plc.disconnect", err)
	}
	return nil
}

func (w *workflow) observe(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	return w.report("plc.observe", "completed", map[string]any{"duration": duration.String()})
}

func parseBaseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(raw)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, errors.New("base URL must be an absolute https URL")
	}
	return base, nil
}

func loadTLSConfig(caPath, serverName string) (*tls.Config, error) {
	if caPath == "" {
		return nil, errors.New("CA file is required")
	}
	if serverName == "" {
		return nil, errors.New("TLS server name is required")
	}
	contents, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(contents) {
		return nil, errors.New("CA file contains no certificate")
	}
	return &tls.Config{
		RootCAs:    roots,
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}, nil
}

func loadPoints(path string) (json.RawMessage, []pointDefinition, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var file pointFile
	if err := json.Unmarshal(contents, &file); err != nil {
		return nil, nil, err
	}
	var definitions []pointDefinition
	if len(file.Points) == 0 || json.Unmarshal(file.Points, &definitions) != nil || len(definitions) == 0 {
		return nil, nil, errors.New("points.json must contain a non-empty points array")
	}
	return file.Points, definitions, nil
}

func (w *workflow) authenticate(ctx context.Context, username, password string) error {
	response, err := w.postJSON(ctx, "/api/auth/initial-admin", map[string]string{
		"username": username, "password": password, "confirmPassword": password,
	})
	if err != nil {
		return atStage("auth.initial-admin", err)
	}
	status := response.StatusCode
	_ = response.Body.Close()
	switch status {
	case http.StatusCreated:
		return w.report("auth.initial-admin", "created", nil)
	case http.StatusConflict:
		if err := w.report("auth.initial-admin", "already_initialized", nil); err != nil {
			return err
		}
	default:
		return atStage("auth.initial-admin", httpStatusError{status: status})
	}

	response, err = w.postJSON(ctx, "/api/auth/login", map[string]string{"username": username, "password": password})
	if err != nil {
		return atStage("auth.login", err)
	}
	status = response.StatusCode
	_ = response.Body.Close()
	if status != http.StatusOK {
		return atStage("auth.login", httpStatusError{status: status})
	}
	return atStage("auth.login", w.report("auth.login", "authenticated", nil))
}

func (w *workflow) openWebSocket() error {
	location := *w.base
	location.Scheme = "wss"
	location.Path = strings.TrimRight(location.Path, "/") + "/ws"
	location.RawQuery = ""
	config, err := websocket.NewConfig(location.String(), w.base.String())
	if err != nil {
		return err
	}
	config.TlsConfig = w.tls.Clone()
	connection, err := websocket.DialConfig(config)
	if err != nil {
		return err
	}
	w.ws = connection
	return w.report("ws.connect", "connected", nil)
}

func (w *workflow) configure(points json.RawMessage, pointCount int) error {
	if err := w.send("runtime.configure", map[string]any{"scanIntervalMs": 50, "points": points}); err != nil {
		return err
	}
	message, err := w.receive("runtime.configured")
	if err != nil {
		return err
	}
	if err := successful(message); err != nil {
		return err
	}
	return w.report("runtime.configure", "configured", map[string]any{"pointCount": pointCount})
}

func (w *workflow) scan(addressRange string) (string, error) {
	if err := w.send("plc.scan", map[string]any{"addressRange": addressRange}); err != nil {
		return "", err
	}
	message, err := w.receive("plc.scan.result")
	if err != nil {
		return "", err
	}
	if err := successful(message); err != nil {
		return "", err
	}
	var payload struct {
		Devices []device `json:"devices"`
	}
	if err := decodeMessage(message, &payload); err != nil {
		return "", err
	}
	if len(payload.Devices) == 0 || payload.Devices[0].DeviceID == "" {
		return "", errors.New("PLC scan returned no device")
	}
	if err := w.report("plc.scan", "found", map[string]any{"deviceCount": len(payload.Devices), "deviceId": payload.Devices[0].DeviceID}); err != nil {
		return "", err
	}
	return payload.Devices[0].DeviceID, nil
}

func (w *workflow) connect(deviceID string) error {
	if err := w.send("plc.connect", map[string]any{"deviceId": deviceID}); err != nil {
		return err
	}
	message, err := w.receive("plc.connect.result")
	if err != nil {
		return err
	}
	if err := successful(message); err != nil {
		return err
	}
	if err := w.report("plc.connect", "connected", map[string]any{"deviceId": deviceID}); err != nil {
		return err
	}
	return w.snapshot("points.snapshot.initial")
}

func (w *workflow) runActions(points []pointDefinition) error {
	for _, mode := range []string{"pulse", "momentary", "toggle"} {
		point, found := actionPoint(points, mode)
		if !found {
			if err := w.report("point.command", "skipped", map[string]any{"mode": mode, "reason": "points.json has no matching action"}); err != nil {
				return err
			}
			continue
		}
		switch mode {
		case "pulse":
			if err := w.command(point, "pulse", 1); err != nil {
				return err
			}
		case "momentary":
			if err := w.command(point, "press", 1); err != nil {
				return err
			}
			if err := w.command(point, "release", 1); err != nil {
				return err
			}
		case "toggle":
			if err := w.command(point, "toggle", 1); err != nil {
				return err
			}
			if err := w.command(point, "toggle", 2); err != nil {
				return err
			}
		}
	}
	return nil
}

func actionPoint(points []pointDefinition, mode string) (pointDefinition, bool) {
	for _, point := range points {
		if point.Write != nil && point.Write.Mode == mode && point.commandID() != "" {
			return point, true
		}
	}
	return pointDefinition{}, false
}

func (p pointDefinition) commandID() string {
	if p.WritePoint != nil && *p.WritePoint != "" {
		return *p.WritePoint
	}
	return p.PointID
}

func (w *workflow) command(point pointDefinition, action string, attempt int) error {
	pointID := point.commandID()
	if err := w.send("point.command", map[string]any{"pointId": pointID, "action": action}); err != nil {
		return err
	}
	message, err := w.receive("point.result")
	if err != nil {
		return err
	}
	if err := successful(message); err != nil {
		return err
	}
	var payload struct {
		PointID string `json:"pointId"`
	}
	if err := decodeMessage(message, &payload); err != nil {
		return err
	}
	if payload.PointID != pointID {
		return errors.New("point command result did not match the requested point")
	}
	return w.report("point.command", "completed", map[string]any{"mode": point.Write.Mode, "pointId": pointID, "action": action, "attempt": attempt})
}

func (w *workflow) snapshot(stage string) error {
	if stage == "points.snapshot.get" {
		if err := w.send("points.snapshot.get", nil); err != nil {
			return err
		}
	}
	message, err := w.receive("points.snapshot")
	if err != nil {
		return err
	}
	var payload struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := decodeMessage(message, &payload); err != nil {
		return err
	}
	return w.report(stage, "received", map[string]any{"pointCount": len(payload.Values)})
}

func (w *workflow) disconnect() error {
	if err := w.send("plc.disconnect", nil); err != nil {
		return err
	}
	message, err := w.receive("plc.disconnect.result")
	if err != nil {
		return err
	}
	if err := successful(message); err != nil {
		return err
	}
	return w.report("plc.disconnect", "disconnected", nil)
}

func (w *workflow) postJSON(ctx context.Context, path string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint(path), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	return w.client.Do(request)
}

func (w *workflow) endpoint(path string) string {
	location := *w.base
	location.Path = path
	location.RawQuery = ""
	return location.String()
}

func (w *workflow) send(messageType string, fields map[string]any) error {
	requestID, err := newRequestID()
	if err != nil {
		return err
	}
	message := map[string]any{
		"protocolVersion": "1.0",
		"type":            messageType,
		"requestId":       requestID,
		"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
	}
	for key, value := range fields {
		message[key] = value
	}
	if err := w.ws.SetDeadline(time.Now().Add(requestTimeout)); err != nil {
		return err
	}
	return websocket.JSON.Send(w.ws, message)
}

func (w *workflow) receive(expectedType string) (map[string]json.RawMessage, error) {
	for {
		if err := w.ws.SetDeadline(time.Now().Add(requestTimeout)); err != nil {
			return nil, err
		}
		var message map[string]json.RawMessage
		if err := websocket.JSON.Receive(w.ws, &message); err != nil {
			return nil, err
		}
		messageType := ""
		if err := json.Unmarshal(message["type"], &messageType); err != nil {
			return nil, errors.New("WebSocket message has no type")
		}
		if messageType == "error" {
			return nil, failureFrom(message)
		}
		if messageType == expectedType {
			return message, nil
		}
	}
}

func successful(message map[string]json.RawMessage) error {
	if raw, exists := message["success"]; exists {
		var success bool
		if err := json.Unmarshal(raw, &success); err != nil {
			return err
		}
		if !success {
			return failureFrom(message)
		}
	}
	return nil
}

func failureFrom(message map[string]json.RawMessage) error {
	var payload struct {
		Type      string `json:"type"`
		RequestID string `json:"requestId"`
		Error     struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = decodeMessage(message, &payload)
	return protocolFailure{code: payload.Error.Code, message: payload.Error.Message, requestID: payload.RequestID, messageType: payload.Type}
}

func decodeMessage(message map[string]json.RawMessage, target any) error {
	contents, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return json.Unmarshal(contents, target)
}

func (w *workflow) report(stage, status string, details map[string]any) error {
	return w.output.Encode(resultLine{Stage: stage, Status: status, Details: details})
}

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

type httpStatusError struct {
	status int
}

func (e httpStatusError) Error() string {
	return fmt.Sprintf("unexpected HTTP status %d", e.status)
}

func errorCode(err error) string {
	var protocol protocolFailure
	if errors.As(err, &protocol) && protocol.code != "" {
		return protocol.code
	}
	var status httpStatusError
	if errors.As(err, &status) {
		return fmt.Sprintf("HTTP_%d", status.status)
	}
	return "FAILED"
}

func atStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	var existing workflowFailure
	if errors.As(err, &existing) {
		return err
	}
	return workflowFailure{stage: stage, cause: err}
}

func failureResult(err error) resultLine {
	result := resultLine{Stage: "workflow", Status: "failed", ErrorCode: errorCode(err)}
	var staged workflowFailure
	if errors.As(err, &staged) {
		result.Stage = staged.stage
	}
	var protocol protocolFailure
	if errors.As(err, &protocol) {
		result.ErrorCode = protocol.code
		result.Message = protocol.message
		result.RequestID = protocol.requestID
		result.MessageType = protocol.messageType
	}
	return result
}
