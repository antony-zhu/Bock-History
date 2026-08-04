package sshbootstrap

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"block.local/block-agent/internal/auth"
)

func TestKeyHandlerReturnsOneTimePrivateKeyAndReplacesAuthorizedKeys(t *testing.T) {
	hash, err := auth.HashPassword("super-key")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, []byte("old-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := NewKeyHandler(KeyConfig{
		SuperKeyHash: hash, AuthorizedKeysPath: path, DeviceID: "block-0001", AdvertisedHost: "192.168.1.20",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, keyRequest(t, "super-key"))
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, recorder.Body.String())
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" || response.Header.Get("Cache-Control") != "no-store" ||
		response.Header.Get("Pragma") != "no-cache" || response.Header.Get("X-SSH-Host") != "192.168.1.20" ||
		response.Header.Get("X-SSH-Port") != "22" || response.Header.Get("X-SSH-Username") != "block" ||
		response.Header.Get("X-SSH-Key-Fingerprint") == "" {
		t.Fatalf("headers=%v", response.Header)
	}
	if !strings.Contains(recorder.Body.String(), "BEGIN OPENSSH PRIVATE KEY") {
		t.Fatalf("response is not an OpenSSH private key: %q", recorder.Body.String())
	}
	installed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(installed), "ssh-ed25519 ") || bytes.Equal(installed, []byte("old-key\n")) {
		t.Fatalf("authorized_keys=%q", installed)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, keyRequest(t, "super-key"))
	if recorder.Code != http.StatusConflict || errorCodeFromBody(t, recorder) != "KEY_GENERATION_IN_PROGRESS" {
		t.Fatalf("cooldown response=%d %s", recorder.Code, recorder.Body.String())
	}
}

func TestKeyHandlerRejectsInvalidSuperKeyWithoutChangingAuthorizedKeys(t *testing.T) {
	hash, err := auth.HashPassword("super-key")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, []byte("old-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := NewKeyHandler(KeyConfig{
		SuperKeyHash: hash, AuthorizedKeysPath: path, DeviceID: "block-0001", AdvertisedHost: "192.168.1.20",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, keyRequest(t, "wrong"))
	if recorder.Code != http.StatusUnauthorized || errorCodeFromBody(t, recorder) != "INVALID_SUPER_KEY" {
		t.Fatalf("invalid response=%d %s", recorder.Code, recorder.Body.String())
	}
	installed, err := os.ReadFile(path)
	if err != nil || string(installed) != "old-key\n" {
		t.Fatalf("authorized keys changed=%q err=%v", installed, err)
	}
}

func keyRequest(t *testing.T, key string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]string{"super_key": key})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, keyEndpoint, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func errorCodeFromBody(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body["code"]
}
