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
		`data-maintenance-tab="production"`,
		`data-maintenance-tab="wifi"`,
		`data-maintenance-tab="plc"`,
		`data-maintenance-tab="accounts"`,
		`data-maintenance-panel="production"`,
		`data-maintenance-panel="wifi"`,
		`data-maintenance-panel="plc"`,
		`data-maintenance-panel="accounts"`,
		`/api/v2/maintenance/production`,
		`/api/v2/maintenance/connectivity`,
		`/api/v2/maintenance/wifi/connect`,
		`id="operatorName"`,
		`import("./assets/hmi.mjs")`,
		`function requireFrontendPermission(permission)`,
		`window.BlockHMIReady.then(syncFrontendPermissions)`,
		`name === "maintenance" && !requireFrontendPermission("maintenance")`,
		`window.addEventListener("block-hmi-guest", () => {`,
		`switchPage("home")`,
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("index is missing %q", required)
		}
	}
	if strings.Contains(page, "api-client.js") {
		t.Fatal("old REST polling client is still loaded by the HMI page")
	}
	if !regexp.MustCompile(`(?s)\.page\[data-page="maintenance"\] \.settings-layout \{.*?overflow: hidden;`).MatchString(page) ||
		!regexp.MustCompile(`(?s)\.maintenance-panel \{.*?overflow-y: auto;.*?overscroll-behavior: contain;`).MatchString(page) {
		t.Fatal("maintenance panels do not have isolated local scrolling")
	}
	passwordInputs := regexp.MustCompile(`<input id="([^"]+)"[^>]*type="password"`).FindAllStringSubmatch(page, -1)
	passwordToggles := regexp.MustCompile(`<button[^>]*aria-controls="([^"]+)"[^>]*data-password-toggle`).FindAllStringSubmatch(page, -1)
	if len(passwordInputs) != 7 || len(passwordToggles) != len(passwordInputs) {
		t.Fatalf("password visibility controls do not cover every password input: inputs=%d toggles=%d", len(passwordInputs), len(passwordToggles))
	}
	passwordInputIDs := map[string]bool{}
	for _, match := range passwordInputs {
		passwordInputIDs[match[1]] = true
	}
	for _, match := range passwordToggles {
		if !passwordInputIDs[match[1]] {
			t.Fatalf("password visibility control targets unknown input %q", match[1])
		}
	}
	if !strings.Contains(page, `class="password-visibility-toggle" type="button"`) || !strings.Contains(page, `aria-label="显示密码"`) || !strings.Contains(page, `aria-pressed="false"`) {
		t.Fatal("password visibility controls are missing button semantics or accessible state")
	}
	if !strings.Contains(page, `}, 650);`) || strings.Contains(page, `backend.updateSettings`) {
		t.Fatal("production settings do not use the dedicated 650 ms local Agent save path")
	}
	keyboardCSS, err := os.ReadFile("assets/soft-keyboard.css")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?s)#auth-panel\[data-keyboard-open="true"\] \{.*?--auth-sheet-top-gap:.*?padding-bottom: calc\(100vh - var\(--auth-keyboard-top`).Match(keyboardCSS) {
		t.Fatal("auth keyboard layout does not reserve a bounded area above the keyboard")
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
		`#accountSettingsPanel`,
		`private renderPLCReadOnly(): void`,
		`private bindPasswordVisibilityToggles(): void`,
		`private requirePermission(permission: "operate" | "maintenance"): boolean`,
		`this.openSocket();`,
		`buildRuntimeConfigure(this.config.points)`,
		`this.refreshLocalSession();`,
		`window.sessionStorage.setItem(localSessionStorageKey`,
		`window.sessionStorage.removeItem(localSessionStorageKey)`,
		`new Event("block-hmi-guest")`,
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
	if !strings.Contains(source, `toggle.addEventListener("pointerdown", (event) => event.preventDefault())`) || !strings.Contains(source, `input.type = input.type === "password" ? "text" : "password";`) {
		t.Fatal("password visibility toggle does not preserve the current input target")
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
