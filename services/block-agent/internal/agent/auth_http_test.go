package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"block.local/block-agent/internal/auth"
	"block.local/block-agent/internal/storage"
	"golang.org/x/net/websocket"
)

func TestLocalStatelessAuthAndStaticHMI(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "block.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService, err := auth.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewLocalRuntimeWithServices("127.0.0.1:0", time.Now, nil, fstest.MapFS{
		"index.html":               {Data: []byte("<main>Block HMI</main>")},
		"assets/hmi.mjs":           {Data: []byte("console.log('block')")},
		"downloads/diagnostic.zip": {Data: []byte("download")},
	}, authService)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.ServeListener(ctx, listener) }()
	defer stopRuntime(t, cancel, done)
	address := "http://" + listener.Addr().String()
	client := &http.Client{}

	response, err := client.Get(address + "/api/v2/auth/initial-admin")
	if err != nil {
		t.Fatal(err)
	}
	var bootstrap bootstrapStatusResponse
	decodeHTTPJSON(t, response, &bootstrap)
	if response.StatusCode != http.StatusOK || !bootstrap.BootstrapRequired {
		t.Fatalf("bootstrap status=%d body=%+v", response.StatusCode, bootstrap)
	}

	response = postJSON(t, client, address+"/api/v2/auth/initial-admin", map[string]string{
		"username": "admin", "password": "one",
	})
	if response.StatusCode != http.StatusBadRequest || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("initial admin without confirmation status=%d cache-control=%q", response.StatusCode, response.Header.Get("Cache-Control"))
	}
	response.Body.Close()

	response = postJSON(t, client, address+"/api/v2/auth/initial-admin", map[string]string{
		"username": "admin", "password": "one", "confirmPassword": "one",
	})
	var identity identityResponse
	payload := decodeHTTPJSON(t, response, &identity)
	if response.StatusCode != http.StatusCreated || identity.Username != "admin" || identity.Role != auth.RoleAdmin || !identity.Permissions.Operate || !identity.Permissions.Maintenance {
		t.Fatalf("bootstrap status=%d identity=%+v", response.StatusCode, identity)
	}
	assertStatelessAuthResponse(t, response, payload)

	response = postJSON(t, client, address+"/api/v2/auth/initial-admin", map[string]string{
		"username": "again", "password": "one", "confirmPassword": "one",
	})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("repeat bootstrap status=%d", response.StatusCode)
	}
	response.Body.Close()

	response = postJSON(t, client, address+"/api/v2/auth/login", map[string]string{"username": "admin", "password": "bad"})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong login status=%d", response.StatusCode)
	}
	payload = readHTTPBody(t, response)
	assertStatelessAuthResponse(t, response, payload)
	response.Body.Close()

	response = postJSON(t, client, address+"/api/v2/auth/login", map[string]string{"username": "admin", "password": "one"})
	identity = identityResponse{}
	payload = decodeHTTPJSON(t, response, &identity)
	if response.StatusCode != http.StatusOK || identity.Username != "admin" || identity.Role != auth.RoleAdmin {
		t.Fatalf("login status=%d identity=%+v", response.StatusCode, identity)
	}
	assertStatelessAuthResponse(t, response, payload)

	response, err = client.Get(address + "/api/v2/config/session")
	if err != nil {
		t.Fatal(err)
	}
	var idle idleTimeoutResponse
	decodeHTTPJSON(t, response, &idle)
	if response.StatusCode != http.StatusOK || idle.IdleTimeoutSeconds != 300 {
		t.Fatalf("initial idle timeout status=%d response=%+v", response.StatusCode, idle)
	}
	response = putJSON(t, client, address+"/api/v2/config/session", map[string]int{"idleTimeoutSeconds": 59})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("short idle timeout status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = putJSON(t, client, address+"/api/v2/config/session", map[string]int{"idleTimeoutSeconds": 120})
	idle = idleTimeoutResponse{}
	decodeHTTPJSON(t, response, &idle)
	if response.StatusCode != http.StatusOK || idle.IdleTimeoutSeconds != 120 {
		t.Fatalf("updated idle timeout status=%d response=%+v", response.StatusCode, idle)
	}

	response = postJSON(t, client, address+"/api/v2/auth/password", map[string]string{
		"username": "admin", "currentPassword": "wrong", "newPassword": "two",
	})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong current password status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = postJSON(t, client, address+"/api/v2/auth/password", map[string]string{
		"username": "admin", "currentPassword": "one", "newPassword": "two",
	})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("password change status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = postJSON(t, client, address+"/api/v2/auth/login", map[string]string{"username": "admin", "password": "one"})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password login status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = postJSON(t, client, address+"/api/v2/auth/login", map[string]string{"username": "admin", "password": "two"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("new password login status=%d", response.StatusCode)
	}
	payload = readHTTPBody(t, response)
	assertStatelessAuthResponse(t, response, payload)
	response.Body.Close()

	for _, endpoint := range []string{"/api/v2/auth/activity", "/api/v2/auth/logout"} {
		response = postJSON(t, client, address+endpoint, map[string]string{})
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("retired endpoint %s status=%d", endpoint, response.StatusCode)
		}
		response.Body.Close()
	}

	response, err = client.Get(address + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "<main>Block HMI</main>" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("static HMI response=%d cache-control=%q body=%q", response.StatusCode, response.Header.Get("Cache-Control"), body)
	}
	response, err = client.Get(address + "/assets/hmi.mjs")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("HMI module response=%d cache-control=%q", response.StatusCode, response.Header.Get("Cache-Control"))
	}
	response, err = client.Get(address + "/downloads/diagnostic.zip")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "" {
		t.Fatalf("download response=%d cache-control=%q", response.StatusCode, response.Header.Get("Cache-Control"))
	}
}

func TestStatelessAuthSurvivesStoreRebuild(t *testing.T) {
	database := filepath.Join(t.TempDir(), "block.db")
	store, err := storage.Open(database, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FirstSetup(context.Background(), "admin", "one", "one"); err != nil {
		t.Fatal(err)
	}
	if err := service.SetIdleTimeout(context.Background(), 180*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	rebuiltStore, err := storage.Open(database, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuiltStore.Close()
	rebuiltService, err := auth.NewService(rebuiltStore)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := rebuiltService.Login(context.Background(), "admin", "one")
	if err != nil || identity != (auth.Identity{Username: "admin", Role: auth.RoleAdmin}) {
		t.Fatalf("rebuilt login identity=%+v error=%v", identity, err)
	}
	timeout, err := rebuiltService.IdleTimeout(context.Background())
	if err != nil || timeout != 180*time.Second {
		t.Fatalf("rebuilt timeout=%s error=%v", timeout, err)
	}
}

func TestStaticHMICacheControlExcludesAPIRoutes(t *testing.T) {
	handler := staticHMI(fstest.MapFS{
		"index.html":               {Data: []byte("<main>Block HMI</main>")},
		"assets/hmi.mjs":           {Data: []byte("console.log('block')")},
		"downloads/diagnostic.zip": {Data: []byte("download")},
	})
	for _, test := range []struct {
		path         string
		cacheControl string
		status       int
	}{
		{path: "/", cacheControl: "no-store", status: http.StatusOK},
		{path: "/assets/hmi.mjs", cacheControl: "no-store", status: http.StatusOK},
		{path: "/api/v2/maintenance/production", cacheControl: "", status: http.StatusNotFound},
		{path: "/downloads/diagnostic.zip", cacheControl: "", status: http.StatusOK},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if got := response.Header().Get("Cache-Control"); got != test.cacheControl || response.Code != test.status {
				t.Fatalf("status=%d cache-control=%q, want status=%d cache-control=%q", response.Code, got, test.status, test.cacheControl)
			}
		})
	}
}

func TestWebSocketAllowsGuestRuntimeAndRejectsForeignOrigin(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "block.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService, err := auth.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewLocalRuntimeWithServices("127.0.0.1:0", time.Now, nil, nil, authService)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.ServeListener(ctx, listener) }()
	defer stopRuntime(t, cancel, done)

	host := listener.Addr().String()
	guestConfig, err := websocket.NewConfig("ws://"+host+"/ws", "http://"+host)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := websocket.DialConfig(guestConfig)
	if err != nil {
		t.Fatalf("anonymous local HMI websocket: %v", err)
	}
	defer guest.Close()
	configure(t, guest)
	if configured := receive(t, guest); configured["type"] != "runtime.configured" {
		t.Fatalf("guest runtime configure = %#v", configured)
	}
	send(t, guest, map[string]any{"type": "points.snapshot.get"})
	if snapshot := receive(t, guest); snapshot["type"] != "points.snapshot" {
		t.Fatalf("guest snapshot = %#v", snapshot)
	}
	send(t, guest, map[string]any{"type": "point.command", "requestId": "guest-point", "pointId": "machine.startCommand", "action": "pulse"})
	if pointResult := receive(t, guest); errorCode(t, pointResult) != "PLC_NOT_CONNECTED" {
		t.Fatalf("guest point command must reach point validation, got %#v", pointResult)
	}
	send(t, guest, map[string]any{"type": "plc.disconnect", "requestId": "guest-disconnect"})
	if disconnectResult := receive(t, guest); disconnectResult["type"] != "plc.disconnect.result" || disconnectResult["success"] != true {
		t.Fatalf("guest PLC maintenance message = %#v", disconnectResult)
	}

	response, err := http.Get("http://" + host + "/api/v2/auth/initial-admin")
	if err != nil {
		t.Fatal(err)
	}
	var bootstrap bootstrapStatusResponse
	decodeHTTPJSON(t, response, &bootstrap)
	if response.StatusCode != http.StatusOK || !bootstrap.BootstrapRequired {
		t.Fatalf("bootstrap endpoint status=%d body=%+v", response.StatusCode, bootstrap)
	}

	foreignConfig, err := websocket.NewConfig("ws://"+host+"/ws", "http://example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := websocket.DialConfig(foreignConfig); err == nil {
		t.Fatal("foreign WebSocket Origin was accepted")
	}
}

func postJSON(t *testing.T, client *http.Client, endpoint string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func putJSON(t *testing.T, client *http.Client, endpoint string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeHTTPJSON(t *testing.T, response *http.Response, target any) []byte {
	t.Helper()
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatalf("decode HTTP response: %v", err)
	}
	return payload
}

func readHTTPBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	return payload
}

func assertStatelessAuthResponse(t *testing.T, response *http.Response, payload []byte) {
	t.Helper()
	if response.Header.Get("Set-Cookie") != "" {
		t.Fatalf("authentication unexpectedly set a cookie: %q", response.Header.Get("Set-Cookie"))
	}
	if strings.Contains(strings.ToLower(string(payload)), "token") || strings.Contains(strings.ToLower(string(payload)), "cookie") || strings.Contains(strings.ToLower(string(payload)), "session") || strings.Contains(string(payload), "expiresAt") {
		t.Fatalf("authentication response contains stateful data: %s", payload)
	}
}
