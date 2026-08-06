package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestStaticHMIUsesLocalGuestPermissions(t *testing.T) {
	index, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for _, required := range []string{
		`id="hmi"`,
		`data-page="home"`,
		`data-page="data"`,
		`data-page="maintenance"`,
		`data-page="alarm"`,
		`data-page="history"`,
		`id="login-form"`,
		`id="initial-admin-form"`,
		`id="password-form"`,
		`id="session-policy-form"`,
		`id="plc-scan-button"`,
		`id="plc-disconnect-button"`,
		`id="snapshot-button"`,
		`id="operatorName"`,
		`import("./assets/hmi.mjs")`,
		`function requireFrontendPermission(permission)`,
		`window.BlockHMIReady.then(syncFrontendPermissions)`,
		`name === "maintenance" && !requireFrontendPermission("maintenance")`,
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("index is missing %q", required)
		}
	}
	if strings.Contains(page, "api-client.js") {
		t.Fatal("old REST polling client is still loaded by the HMI page")
	}

	bridge, err := os.ReadFile("assets/hmi.mts")
	if err != nil {
		t.Fatal(err)
	}
	source := string(bridge)
	for _, required := range []string{
		`export const localAdminStorageKey = "block-hmi-local-admin-v1"`,
		`export const localSessionStorageKey = "block-hmi-local-session-v1"`,
		`export const localSettingsStorageKey = "block-hmi-local-settings-v1"`,
		`crypto.subtle.digest("SHA-256"`,
		`private prepareGuestHMI(): void`,
		`private moveLocalAdministrationToMaintenance(): void`,
		`private requirePermission(permission: "operate" | "maintenance"): boolean`,
		`this.openSocket();`,
		`buildRuntimeConfigure(this.config.points)`,
		`this.refreshLocalSession();`,
		`window.sessionStorage.setItem(localSessionStorageKey`,
		`window.sessionStorage.removeItem(localSessionStorageKey)`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("HMI bridge is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`/api/v2/auth/status`,
		`/api/v2/auth/login`,
		`/api/v2/auth/activity`,
		`/api/v2/auth/logout`,
		`jsonRequest(`,
		`event.code === 4401`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("HMI bridge still contains backend auth dependency %q", forbidden)
		}
	}
	if !regexp.MustCompile(`private sendCommand[\s\S]*?requirePermission\("operate"\)`).MatchString(source) {
		t.Fatal("HMI point commands are not protected by the frontend permission gate")
	}
	if !regexp.MustCompile(`private sendPLCScan[\s\S]*?requirePermission\("maintenance"\)`).MatchString(source) {
		t.Fatal("HMI PLC actions are not protected by the frontend maintenance gate")
	}
	for _, asset := range []string{
		"assets/hmi.mjs",
		"assets/machine-bin.png",
		"assets/soft-keyboard.css",
		"assets/soft-keyboard.js",
		"assets/vendor/simple-keyboard/index.css",
		"assets/vendor/simple-keyboard/index.js",
		"THIRD_PARTY_NOTICES.md",
	} {
		if _, err := os.Stat(asset); err != nil {
			t.Fatalf("required HMI asset %q is missing: %v", asset, err)
		}
	}
}

func TestPointsJSONKeepsDisplayBindingsOutOfRuntimePoints(t *testing.T) {
	contents, err := os.ReadFile("assets/points.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		ScanIntervalMs int              `json:"scanIntervalMs"`
		Points         []map[string]any `json:"points"`
		Bindings       []struct {
			DisplayPath string `json:"displayPath"`
			Description string `json:"description"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	if config.ScanIntervalMs != 50 || len(config.Points) == 0 || len(config.Bindings) == 0 {
		t.Fatalf("incomplete points.json: %+v", config)
	}
	for _, point := range config.Points {
		if point["writeMethod"] != nil && point["writeMethod"] != "maskWrite" {
			t.Fatalf("point has unexpected write method: %+v", point)
		}
		if _, ok := point["displayPath"]; ok {
			t.Fatalf("runtime point leaked displayPath: %+v", point)
		}
		if _, ok := point["description"]; ok {
			t.Fatalf("runtime point leaked description: %+v", point)
		}
	}
	for _, binding := range config.Bindings {
		if !isEnglishDisplayPath(binding.DisplayPath) {
			t.Fatalf("display path is not an English dotted path: %q", binding.DisplayPath)
		}
		if binding.Description == "" {
			t.Fatalf("binding description is empty for %q", binding.DisplayPath)
		}
	}
}

func isEnglishDisplayPath(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, letter := range part {
			if letter < 'a' || letter > 'z' {
				return false
			}
		}
	}
	return true
}
