package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStaticAndHealthRoutes(t *testing.T) {
	handler := newTestHandler(t, "")
	tests := []struct {
		name        string
		path        string
		contentType string
		contains    string
	}{
		{name: "health", path: "/healthz", contentType: "text/plain", contains: "ok"},
		{name: "base health", path: "/block-apple-style/healthz", contentType: "text/plain", contains: "ok"},
		{name: "index", path: "/", contentType: "text/html", contains: "id=\"hmi\""},
		{name: "base index", path: "/block-apple-style/", contentType: "text/html", contains: "id=\"hmi\""},
		{name: "machine image", path: "/assets/machine-bin.png", contentType: "image/png"},
		{name: "soft keyboard script", path: "/assets/soft-keyboard.js", contentType: "text/javascript", contains: "HMISoftKeyboard"},
		{name: "soft keyboard stylesheet", path: "/assets/soft-keyboard.css", contentType: "text/css", contains: ".soft-keyboard-dock"},
		{name: "vendored keyboard", path: "/assets/vendor/simple-keyboard/index.js", contentType: "text/javascript", contains: "simple-keyboard v3.8.165"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := performRequest(handler, http.MethodGet, tt.path, nil, nil)
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, tt.contentType) {
				t.Fatalf("Content-Type = %q, want prefix %q", got, tt.contentType)
			}
			if response.Header.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing X-Content-Type-Options header")
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if len(body) == 0 {
				t.Fatal("empty response body")
			}
			if tt.contains != "" && !strings.Contains(string(body), tt.contains) {
				t.Fatalf("response does not contain %q", tt.contains)
			}
		})
	}
}

func TestStateAvailableAtRootAndPublicBasePath(t *testing.T) {
	handler := newTestHandler(t, "")
	for _, path := range []string{"/api/v1/state", "/block-apple-style/api/v1/state"} {
		response := performRequest(handler, http.MethodGet, path, nil, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, response.StatusCode)
		}
		if got := response.Header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q", path, got)
		}
		if got := response.Header.Get("ETag"); got != "\"rev-1\"" {
			t.Fatalf("GET %s ETag = %q", path, got)
		}
		var payload struct {
			State HMIState `json:"state"`
		}
		decodeResponse(t, response, &payload)
		if payload.State.Target != 30 || payload.State.Mode != "auto" {
			t.Fatalf("unexpected initial state: %+v", payload.State)
		}
	}
}

func TestSettingsCommandAlarmAndAuditFlow(t *testing.T) {
	handler := newTestHandler(t, "")

	settingsResponse := performJSON(t, handler, http.MethodPut, "/api/v1/settings", map[string]interface{}{
		"target": 48, "toolLimit": 120, "inspectInterval": 16, "expectedRevision": 1,
	}, map[string]string{"X-Operator": "MAINTAINER-02", "X-Request-ID": "settings-1"})
	if settingsResponse.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d: %s", settingsResponse.StatusCode, readBody(t, settingsResponse))
	}
	var settingsPayload struct {
		State   HMIState `json:"state"`
		Message string   `json:"message"`
	}
	decodeResponse(t, settingsResponse, &settingsPayload)
	if settingsPayload.State.ToolLimit != 120 || settingsPayload.State.Revision != 2 {
		t.Fatalf("unexpected settings response: %+v", settingsPayload)
	}

	commandResponse := performJSON(t, handler, http.MethodPost, "/block-apple-style/api/v1/commands", map[string]interface{}{
		"command": "set_mode", "mode": "manual", "operator": "OPERATOR-01",
	}, map[string]string{"If-Match": "\"rev-2\""})
	if commandResponse.StatusCode != http.StatusOK {
		t.Fatalf("command status = %d: %s", commandResponse.StatusCode, readBody(t, commandResponse))
	}
	var commandPayload struct {
		State HMIState `json:"state"`
	}
	decodeResponse(t, commandResponse, &commandPayload)
	if commandPayload.State.Mode != "manual" || commandPayload.State.Revision != 3 {
		t.Fatalf("unexpected command state: %+v", commandPayload.State)
	}

	ackResponse := performJSON(t, handler, http.MethodPost, "/api/v1/alarms/3/ack", map[string]interface{}{
		"operator": "OPERATOR-01", "expectedRevision": 3,
	}, nil)
	if ackResponse.StatusCode != http.StatusOK {
		t.Fatalf("ack status = %d: %s", ackResponse.StatusCode, readBody(t, ackResponse))
	}
	var ackPayload struct {
		State HMIState `json:"state"`
	}
	decodeResponse(t, ackResponse, &ackPayload)
	if !ackPayload.State.Alarms[0].Acknowledged || ackPayload.State.Revision != 4 {
		t.Fatalf("unexpected alarm state: %+v", ackPayload.State.Alarms)
	}

	auditResponse := performRequest(handler, http.MethodGet, "/api/v1/audit?limit=2", nil, nil)
	if auditResponse.StatusCode != http.StatusOK {
		t.Fatalf("audit status = %d", auditResponse.StatusCode)
	}
	var auditPayload struct {
		Items        []AuditEntry `json:"items"`
		NextBeforeID *uint64      `json:"nextBeforeId"`
	}
	decodeResponse(t, auditResponse, &auditPayload)
	if len(auditPayload.Items) != 2 || auditPayload.Items[0].Action != "alarm.acknowledge" {
		t.Fatalf("unexpected audit page: %+v", auditPayload)
	}
	if auditPayload.NextBeforeID == nil {
		t.Fatal("expected audit pagination cursor")
	}
	if auditPayload.Items[1].Operator != "OPERATOR-01" {
		t.Fatalf("audit operator = %q", auditPayload.Items[1].Operator)
	}
}

func TestValidationAndJSONErrors(t *testing.T) {
	handler := newTestHandler(t, "")
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		headers    map[string]string
		wantStatus int
		wantCode   string
	}{
		{name: "operator required", method: http.MethodPut, path: "/api/v1/settings", body: `{"target":30,"toolLimit":100,"inspectInterval":30}`, headers: jsonHeader(), wantStatus: http.StatusBadRequest, wantCode: "operator_required"},
		{name: "range", method: http.MethodPut, path: "/api/v1/settings", body: `{"target":0,"toolLimit":100,"inspectInterval":30,"operator":"A"}`, headers: jsonHeader(), wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_error"},
		{name: "unknown field", method: http.MethodPost, path: "/api/v1/commands", body: `{"command":"start","operator":"A","typo":true}`, headers: jsonHeader(), wantStatus: http.StatusBadRequest, wantCode: "malformed_json"},
		{name: "wrong content type", method: http.MethodPost, path: "/api/v1/commands", body: `{"command":"start","operator":"A"}`, headers: map[string]string{"Content-Type": "text/plain"}, wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type"},
		{name: "unsupported command", method: http.MethodPost, path: "/api/v1/commands", body: `{"command":"launch","operator":"A"}`, headers: jsonHeader(), wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_error"},
		{name: "method", method: http.MethodDelete, path: "/api/v1/state", wantStatus: http.StatusMethodNotAllowed, wantCode: "method_not_allowed"},
		{name: "missing route", method: http.MethodGet, path: "/api/v1/missing", wantStatus: http.StatusNotFound, wantCode: "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := performRequest(handler, tt.method, tt.path, strings.NewReader(tt.body), tt.headers)
			if response.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.StatusCode, tt.wantStatus, readBody(t, response))
			}
			var payload errorEnvelope
			decodeResponse(t, response, &payload)
			if payload.Error.Code != tt.wantCode {
				t.Fatalf("error code = %q, want %q", payload.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestRevisionConflictDoesNotMutateState(t *testing.T) {
	handler := newTestHandler(t, "")
	response := performJSON(t, handler, http.MethodPost, "/api/v1/commands", map[string]interface{}{
		"command": "pause", "operator": "OPERATOR-01", "expectedRevision": 99,
	}, nil)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.StatusCode, readBody(t, response))
	}
	state := getState(t, handler)
	if !state.Running || state.Revision != 1 {
		t.Fatalf("state changed after conflict: %+v", state)
	}
}

func TestConcurrentCommandsAreSerialized(t *testing.T) {
	handler := newTestHandler(t, "")
	const operations = 40
	var wait sync.WaitGroup
	errorsChannel := make(chan error, operations)
	for i := 0; i < operations; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response := performRequest(handler, http.MethodPost, "/api/v1/commands", strings.NewReader(`{"command":"inspect","operator":"QA"}`), jsonHeader())
			if response.StatusCode != http.StatusOK {
				errorsChannel <- fmt.Errorf("operation %d status %d", index, response.StatusCode)
			}
			_ = response.Body.Close()
		}(i)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
	state := getState(t, handler)
	if got, want := state.Inspected, 30+operations; got != want {
		t.Fatalf("inspected = %d, want %d", got, want)
	}
	if got, want := state.Revision, uint64(1+operations); got != want {
		t.Fatalf("revision = %d, want %d", got, want)
	}
}

func TestSettingsAndAuditPersistAcrossRestart(t *testing.T) {
	dataFile := filepath.Join(t.TempDir(), "state.json")
	fixedTime := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	controller, err := newMemoryController(dataFile, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHandlerWithController(controller, defaultBasePath)
	if err != nil {
		t.Fatal(err)
	}
	response := performJSON(t, handler, http.MethodPut, "/api/v1/settings", map[string]interface{}{
		"target": 55, "toolLimit": 250, "inspectInterval": 20, "operator": "MAINTAINER-09",
	}, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d: %s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	reloaded, err := newMemoryController(dataFile, time.Now)
	if err != nil {
		t.Fatalf("reload controller: %v", err)
	}
	reloadedHandler, err := newHandlerWithController(reloaded, defaultBasePath)
	if err != nil {
		t.Fatal(err)
	}
	state := getState(t, reloadedHandler)
	if state.Target != 55 || state.ToolLimit != 250 {
		t.Fatalf("settings were not persisted: %+v", state)
	}
	auditResponse := performRequest(reloadedHandler, http.MethodGet, "/api/v1/audit", nil, nil)
	var payload struct {
		Items []AuditEntry `json:"items"`
	}
	decodeResponse(t, auditResponse, &payload)
	if len(payload.Items) != 1 || payload.Items[0].Operator != "MAINTAINER-09" {
		t.Fatalf("audit was not persisted: %+v", payload.Items)
	}
}

func TestInvalidBasePath(t *testing.T) {
	controller, err := newMemoryController("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newHandlerWithController(controller, "/bad/../path"); err == nil {
		t.Fatal("expected invalid base path error")
	}
}

func newTestHandler(t *testing.T, dataFile string) http.Handler {
	t.Helper()
	controller, err := newMemoryController(dataFile, time.Now)
	if err != nil {
		t.Fatalf("newMemoryController: %v", err)
	}
	handler, err := newHandlerWithController(controller, defaultBasePath)
	if err != nil {
		t.Fatalf("newHandlerWithController: %v", err)
	}
	return handler
}

func performJSON(t *testing.T, handler http.Handler, method, path string, body interface{}, headers map[string]string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if headers == nil {
		headers = make(map[string]string)
	}
	headers["Content-Type"] = "application/json"
	return performRequest(handler, method, path, bytes.NewReader(encoded), headers)
}

func performRequest(handler http.Handler, method, path string, body io.Reader, headers map[string]string) *http.Response {
	request := httptest.NewRequest(method, path, body)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Result()
}

func getState(t *testing.T, handler http.Handler) HMIState {
	t.Helper()
	response := performRequest(handler, http.MethodGet, "/api/v1/state", nil, nil)
	var payload struct {
		State HMIState `json:"state"`
	}
	decodeResponse(t, response, &payload)
	return payload.State
}

func decodeResponse(t *testing.T, response *http.Response, target interface{}) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func jsonHeader() map[string]string {
	return map[string]string{"Content-Type": "application/json"}
}
