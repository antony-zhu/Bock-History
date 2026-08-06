package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"block.local/block-agent/internal/maintenance"
	"block.local/block-agent/internal/wifi"
)

type fakeWiFiBackend struct {
	ssid     string
	password string
}

func (f *fakeWiFiBackend) Status(context.Context, string) (wifi.Status, error) {
	ssid := f.ssid
	ipv4 := "192.168.1.104"
	return wifi.Status{State: "connected", Interface: "wlan0", SSID: &ssid, IPv4: &ipv4}, nil
}

func (f *fakeWiFiBackend) Apply(_ context.Context, _ string, ssid, password string) (wifi.Status, error) {
	f.ssid = ssid
	f.password = password
	return f.Status(context.Background(), "wlan0")
}

func TestMaintenanceProductionAndConnectivityHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maintenance.json")
	fakeWiFi := &fakeWiFiBackend{ssid: "Factory"}
	_, address, cancel, done := startRuntimeWithOptions(t, RuntimeOptions{
		MaintenancePath: path, WiFiBackend: fakeWiFi, WiFiInterface: "wlan0",
	})
	defer stopRuntime(t, cancel, done)
	baseURL := "http://" + address

	response, err := http.Get(baseURL + "/api/v2/maintenance/production")
	if err != nil {
		t.Fatal(err)
	}
	var initial maintenance.Production
	if err := json.NewDecoder(response.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || initial.PiecesPerBox != 1 {
		t.Fatalf("initial production status=%d body=%#v", response.StatusCode, initial)
	}

	response = patchMaintenance(t, baseURL+"/api/v2/maintenance/production", map[string]int{
		"targetProduction": 800, "piecesPerBox": 24,
	})
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("patch production status=%d", response.StatusCode)
	}
	response.Body.Close()
	persisted, err := maintenance.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := persisted.Get(); got.TargetProduction != 800 || got.PiecesPerBox != 24 {
		t.Fatalf("persisted production = %#v", got)
	}

	response = patchMaintenance(t, baseURL+"/api/v2/maintenance/production", map[string]int{"piecesPerBox": 0})
	if response.StatusCode != http.StatusBadRequest {
		response.Body.Close()
		t.Fatalf("invalid production status=%d", response.StatusCode)
	}
	response.Body.Close()

	response, err = http.Get(baseURL + "/api/v2/maintenance/connectivity")
	if err != nil {
		t.Fatal(err)
	}
	var connectivity connectivityResponse
	if err := json.NewDecoder(response.Body).Decode(&connectivity); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || connectivity.WiFi.State != "connected" || connectivity.BDM.State != "not_configured" {
		t.Fatalf("connectivity status=%d body=%#v", response.StatusCode, connectivity)
	}

	response = postMaintenance(t, baseURL+"/api/v2/maintenance/wifi/connect", map[string]string{"ssid": "Workshop", "password": "test-secret"})
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("wifi connect status=%d", response.StatusCode)
	}
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if fakeWiFi.ssid != "Workshop" || fakeWiFi.password != "test-secret" {
		t.Fatal("Wi-Fi backend did not receive the requested connection")
	}
	if strings.Contains(string(responseBody), fakeWiFi.password) {
		t.Fatal("Wi-Fi response leaked the password")
	}
	var wifiResponse map[string]any
	if err := json.Unmarshal(responseBody, &wifiResponse); err != nil {
		t.Fatal(err)
	}
	if _, found := wifiResponse["password"]; found {
		t.Fatal("Wi-Fi response returned a password")
	}
}

func patchMaintenance(t *testing.T, endpoint string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPatch, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func postMaintenance(t *testing.T, endpoint string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return response
}
