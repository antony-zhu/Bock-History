package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestStaticHMIUsesV2RuntimeAssets(t *testing.T) {
	index, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"id=\"hmi\"",
		"data-page=\"home\"",
		"data-page=\"data\"",
		"data-page=\"maintenance\"",
		"data-page=\"alarm\"",
		"data-page=\"history\"",
		"data-nav=\"home\"",
		"data-nav=\"data\"",
		"data-nav=\"maintenance\"",
		"data-nav=\"alarm\"",
		"data-nav=\"history\"",
		"data-theme-option=\"light\"",
		"data-theme-option=\"graphite\"",
		"data-theme-option=\"ocean\"",
		"data-theme-option=\"midnight\"",
		"data-theme-option=\"titanium\"",
		"id=\"themeMenu\"",
		"id=\"toast\"",
		"id=\"softKeyboardDock\"",
		"id=\"login-form\"",
		"id=\"initial-admin-form\"",
		"id=\"password-form\"",
		"id=\"logout-button\"",
		"id=\"plc-scan-button\"",
		"id=\"plc-disconnect-button\"",
		"id=\"snapshot-button\"",
		"id=\"plc-candidates\"",
		"import(\"./assets/hmi.mjs\")",
	} {
		if !strings.Contains(string(index), required) {
			t.Fatalf("index is missing %q", required)
		}
	}
	if strings.Contains(string(index), "api-client.js") {
		t.Fatal("old REST polling client is still loaded by the HMI page")
	}
	editableControl := regexp.MustCompile(`(?is)<(?:input|textarea)\b[^>]*>`)
	excludedControl := regexp.MustCompile(`(?i)\b(?:hidden|disabled|readonly)\b|\btype=(?:"|')(?:hidden|button|submit|reset|checkbox|radio|file|image)(?:"|')`)
	keyboardControl := regexp.MustCompile(`(?i)\bdata-soft-keyboard=(?:"|')(?:full|numeric)(?:"|')`)
	controls := editableControl.FindAllString(string(index), -1)
	if len(controls) != 12 {
		t.Fatalf("editable control count = %d, want 12", len(controls))
	}
	for _, control := range controls {
		if excludedControl.MatchString(control) {
			continue
		}
		if !keyboardControl.MatchString(control) {
			t.Fatalf("editable control is missing soft keyboard support: %s", control)
		}
	}
	for _, required := range []string{
		`role="dialog" aria-modal="true"`,
		`background: transparent;`,
		`pointer-events: none;`,
		`pointer-events: auto;`,
		`id="hmi-topbar" inert aria-hidden="true"`,
		`id="hmi-pages" inert aria-hidden="true"`,
		`id="hmi-footer" inert aria-hidden="true"`,
		`id="softKeyboardLayer"`,
	} {
		if !strings.Contains(string(index), required) {
			t.Fatalf("index is missing floating-auth requirement %q", required)
		}
	}
	keyboard, err := os.ReadFile("assets/soft-keyboard.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"function isKeyboardCandidate(input)",
		`document.querySelectorAll("input, textarea")`,
		`["hidden", "button", "submit", "reset", "checkbox", "radio", "file", "image"]`,
		"new window.MutationObserver",
		`attributeFilter: ["disabled", "hidden", "type", "inputmode"]`,
		`activeInput.type === "password"`,
	} {
		if !strings.Contains(string(keyboard), required) {
			t.Fatalf("soft keyboard is missing %q", required)
		}
	}
	bridge, err := os.ReadFile("assets/hmi.mts")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"function demoAuthPreviewMode(): DemoAuthPreview",
		`auth === "login" || auth === "bootstrap"`,
		"private setHMIInteractive(interactive: boolean)",
		`element.toggleAttribute("inert", !interactive)`,
		"if (this.authPreview !== null)",
	} {
		if !strings.Contains(string(bridge), required) {
			t.Fatalf("HMI bridge is missing %q", required)
		}
	}
	for _, asset := range []string{
		"assets/machine-bin.png",
		"assets/soft-keyboard.css",
		"assets/soft-keyboard.js",
		"assets/vendor/simple-keyboard/index.css",
		"assets/vendor/simple-keyboard/index.js",
		"assets/vendor/simple-keyboard/LICENSE",
		"THIRD_PARTY_NOTICES.md",
	} {
		if _, err := os.Stat(asset); err != nil {
			t.Fatalf("V1 asset %q is missing: %v", asset, err)
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
		if !strings.ContainsAny(binding.Description, "启动设备正向点动设备使能反馈") {
			t.Fatalf("binding description is not Chinese: %q", binding.Description)
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
