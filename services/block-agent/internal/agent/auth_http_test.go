package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
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
