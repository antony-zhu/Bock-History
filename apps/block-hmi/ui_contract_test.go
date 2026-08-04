package main

import (
	"encoding/json"
	"os"
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
		"id=\"login-form\"",
		"id=\"initial-admin-form\"",
		"id=\"password-form\"",
		"id=\"plc-scan-button\"",
		"id=\"plc-disconnect-button\"",
		"id=\"snapshot-button\"",
		"id=\"plc-candidates\"",
		"src=\"assets/hmi.mjs\"",
	} {
		if !strings.Contains(string(index), required) {
			t.Fatalf("index is missing %q", required)
		}
	}
	if strings.Contains(string(index), "api-client.js") {
		t.Fatal("old REST polling client is still loaded by the HMI page")
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
