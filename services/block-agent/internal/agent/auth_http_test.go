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

	response := postJSON(t, client, address+"/api/auth/bootstrap", map[string]string{
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

	response = postJSON(t, client, address+"/api/auth/bootstrap", map[string]string{
		"username": "again", "password": "one", "confirmPassword": "one",
	})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("repeat bootstrap status=%d", response.StatusCode)
	}
	response.Body.Close()

	response = postJSON(t, client, address+"/api/auth/activity", map[string]string{})
	if response.StatusCode != http.StatusOK {
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

	response = postJSON(t, client, address+"/api/auth/logout", map[string]string{})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = postJSON(t, client, address+"/api/auth/activity", map[string]string{})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("activity after logout status=%d", response.StatusCode)
	}
	response.Body.Close()

	response = postJSON(t, client, address+"/api/auth/login", map[string]string{"username": "admin", "password": "one"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d", response.StatusCode)
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

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	value, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
