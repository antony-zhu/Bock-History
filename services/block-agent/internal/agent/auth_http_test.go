package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"block.local/block-agent/internal/auth"
	"block.local/block-agent/internal/storage"
	"golang.org/x/net/websocket"
)

func TestLocalAuthBootstrapLoginActivityLogoutAndStaticHMI(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "block.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var runtime *Runtime
	authService, err := auth.NewService(store, time.Now, func(auth.Session) {
		if runtime != nil {
			runtime.StopSession()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer authService.Close()
	runtime, err = NewLocalRuntimeWithServices("127.0.0.1:0", time.Now, nil, fstest.MapFS{
		"index.html":    {Data: []byte("<main>Block HMI</main>")},
		"assets/app.js": {Data: []byte("console.log('block')")},
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
	client := newCookieClient(t)

	response := postJSON(t, client, address+"/api/v2/auth/initial-admin", map[string]string{
		"username": "admin", "password": "one",
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("initial admin without confirmation status=%d", response.StatusCode)
	}
	response.Body.Close()

	response = postJSON(t, client, address+"/api/v2/auth/initial-admin", map[string]string{
		"username": "admin", "password": "one", "confirmPassword": "one",
	})
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("bootstrap status=%d body=%s", response.StatusCode, body)
	}
	response.Body.Close()
	if len(client.Jar.Cookies(mustURL(t, address))) != 1 {
		t.Fatal("bootstrap did not set a session cookie")
	}

	response = postJSON(t, client, address+"/api/v2/auth/initial-admin", map[string]string{
		"username": "again", "password": "one", "confirmPassword": "one",
	})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("repeat bootstrap status=%d", response.StatusCode)
	}
	response.Body.Close()

	response = postJSON(t, client, address+"/api/v2/auth/activity", map[string]string{})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("activity status=%d", response.StatusCode)
	}
	response.Body.Close()
	response, err = client.Get(address + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "<main>Block HMI</main>" {
		t.Fatalf("static HMI response=%d body=%q", response.StatusCode, body)
	}

	response = postJSON(t, client, address+"/api/v2/auth/logout", map[string]string{})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = postJSON(t, client, address+"/api/v2/auth/activity", map[string]string{})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("activity after logout status=%d", response.StatusCode)
	}
	response.Body.Close()

	response = postJSON(t, client, address+"/api/v2/auth/login", map[string]string{"username": "admin", "password": "one"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d", response.StatusCode)
	}
	response.Body.Close()
	connection := dialAuthenticated(t, client, address)
	connection.Close()

	response = putJSON(t, client, address+"/api/v2/config/session", map[string]int{"idleTimeoutSeconds": 120})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("session policy status=%d", response.StatusCode)
	}
	response.Body.Close()
	if timeout := authService.IdleTimeout(); timeout != 120*time.Second {
		t.Fatalf("active idle timeout=%s, want 120s", timeout)
	}
	if timeout, err := store.IdleTimeout(context.Background()); err != nil || timeout != 120*time.Second {
		t.Fatalf("persisted idle timeout=%s, error=%v", timeout, err)
	}
	session, err := authService.Session(sessionCookieValue(t, client, address))
	if err != nil || session.ExpiresAt.Sub(session.LastActivity) != 120*time.Second {
		t.Fatalf("current session policy=%#v, error=%v", session, err)
	}

	response = postJSON(t, client, address+"/api/v2/auth/password", map[string]string{
		"currentPassword": "one", "newPassword": "two",
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("password without confirmation status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = postJSON(t, client, address+"/api/v2/auth/password", map[string]string{
		"currentPassword": "one", "newPassword": "two", "confirmPassword": "two",
	})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("password change status=%d", response.StatusCode)
	}
	response.Body.Close()
}

func TestAuthStatusMatchesContractAndDoesNotRefreshSession(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return now }
	store, err := storage.Open(filepath.Join(t.TempDir(), "block.db"), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService, err := auth.NewService(store, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer authService.Close()
	runtime, err := NewLocalRuntimeWithServices("127.0.0.1:0", clock, nil, nil, authService)
	if err != nil {
		t.Fatal(err)
	}

	status := func(method string, cookie *http.Cookie) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "http://127.0.0.1/api/v2/auth/status", nil)
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		runtime.server.Handler.ServeHTTP(response, request)
		return response
	}

	assertAuthStatus(t, status(http.MethodGet, nil), true, false)

	bootstrapRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v2/auth/initial-admin", bytes.NewBufferString(`{"username":"admin","password":"one","confirmPassword":"one"}`))
	bootstrapRequest.Header.Set("Content-Type", "application/json")
	bootstrapResponse := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(bootstrapResponse, bootstrapRequest)
	if bootstrapResponse.Code != http.StatusCreated {
		t.Fatalf("bootstrap status = %d, body=%s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	cookies := bootstrapResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || cookies[0].Value == "" {
		t.Fatalf("bootstrap cookies = %#v", cookies)
	}
	sessionCookie := cookies[0]

	assertAuthStatus(t, status(http.MethodGet, nil), false, false)
	assertAuthStatus(t, status(http.MethodGet, sessionCookie), false, true)
	original, err := authService.Session(sessionCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(auth.DefaultIdleTimeout - time.Second)
	assertAuthStatus(t, status(http.MethodGet, sessionCookie), false, true)
	current, err := authService.Session(sessionCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !current.ExpiresAt.Equal(original.ExpiresAt) {
		t.Fatalf("status extended expiry to %s, want %s", current.ExpiresAt, original.ExpiresAt)
	}

	invalidCookie := *sessionCookie
	invalidCookie.Value = "invalid"
	assertAuthStatus(t, status(http.MethodGet, &invalidCookie), false, false)
	now = original.ExpiresAt
	assertAuthStatus(t, status(http.MethodGet, sessionCookie), false, false)

	methodNotAllowed := status(http.MethodPost, nil)
	if methodNotAllowed.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", methodNotAllowed.Code)
	}
	if allow := methodNotAllowed.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("POST Allow = %q, want %q", allow, http.MethodGet)
	}
}

func TestAuthStatusReturnsInternalServerErrorWhenStoreIsClosed(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "block.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	authService, err := auth.NewService(store, time.Now, nil)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	defer authService.Close()
	runtime, err := NewLocalRuntimeWithServices("127.0.0.1:0", time.Now, nil, nil, authService)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/v2/auth/status", nil)
	response := httptest.NewRecorder()
	runtime.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status with closed store = %d, body=%s", response.Code, response.Body.String())
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("status error Cache-Control = %q", cacheControl)
	}
}

func assertAuthStatus(t *testing.T, response *httptest.ResponseRecorder, wantBootstrapRequired, wantAuthenticated bool) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
	if cookies := response.Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("status unexpectedly set cookies: %#v", cookies)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("status fields = %#v, want exactly bootstrapRequired and authenticated", fields)
	}
	bootstrap, ok := fields["bootstrapRequired"]
	if !ok {
		t.Fatalf("status fields = %#v, bootstrapRequired is missing", fields)
	}
	authenticated, ok := fields["authenticated"]
	if !ok {
		t.Fatalf("status fields = %#v, authenticated is missing", fields)
	}
	var gotBootstrapRequired, gotAuthenticated bool
	if err := json.Unmarshal(bootstrap, &gotBootstrapRequired); err != nil {
		t.Fatalf("bootstrapRequired is not a boolean: %s", bootstrap)
	}
	if err := json.Unmarshal(authenticated, &gotAuthenticated); err != nil {
		t.Fatalf("authenticated is not a boolean: %s", authenticated)
	}
	if gotBootstrapRequired && gotAuthenticated {
		t.Fatal("status returned bootstrapRequired=true and authenticated=true")
	}
	if gotBootstrapRequired != wantBootstrapRequired || gotAuthenticated != wantAuthenticated {
		t.Fatalf("status = bootstrapRequired=%t authenticated=%t, want bootstrapRequired=%t authenticated=%t", gotBootstrapRequired, gotAuthenticated, wantBootstrapRequired, wantAuthenticated)
	}
}

func newCookieClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
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

func dialAuthenticated(t *testing.T, client *http.Client, address string) *websocket.Conn {
	t.Helper()
	endpoint := mustURL(t, address)
	config, err := websocket.NewConfig("ws://"+endpoint.Host+"/ws", address)
	if err != nil {
		t.Fatal(err)
	}
	config.Header = make(http.Header)
	config.Header.Set("Cookie", sessionCookieName+"="+sessionCookieValue(t, client, address))
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func sessionCookieValue(t *testing.T, client *http.Client, address string) string {
	t.Helper()
	for _, cookie := range client.Jar.Cookies(mustURL(t, address)) {
		if cookie.Name == sessionCookieName && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatal("local login did not retain block_session cookie")
	return ""
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	value, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
