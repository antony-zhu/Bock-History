package runtimeconfig

import (
	"encoding/json"
	"testing"
)

func TestNormalizeAcceptsCompleteTableAndDefaultsPulse(t *testing.T) {
	value, err := Normalize(Config{
		ScanIntervalMs: RequiredScanIntervalMs,
		Points: []PointDefinition{
			{
				PointID: "machine.startCommand", Address: "D504.1", Type: "bool", Access: "read_write",
				ReadPoint: "machine.startFeedback", WritePoint: "machine.startCommand", WriteMethod: "maskWrite",
				Write: &WriteDefinition{Mode: "pulse", ActiveValue: true, DefaultValue: false},
			},
			{PointID: "machine.startFeedback", Address: "D504.2", Type: "bool", Access: "read", ReadPoint: "machine.startFeedback"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if value.Points[0].Write.PulseMs != DefaultPulseMs {
		t.Fatalf("pulseMs = %d, want %d", value.Points[0].Write.PulseMs, DefaultPulseMs)
	}
}

func TestNormalizeRejectsIncompleteOrInconsistentTable(t *testing.T) {
	valid := Config{
		ScanIntervalMs: RequiredScanIntervalMs,
		Points:         []PointDefinition{{PointID: "feedback", Address: "M0.1", Type: "bool", Access: "read", ReadPoint: "feedback"}},
	}
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "wrong scan interval", change: func(value *Config) { value.ScanIntervalMs = 51 }},
		{name: "duplicate point id", change: func(value *Config) { value.Points = append(value.Points, value.Points[0]) }},
		{name: "unknown read point", change: func(value *Config) { value.Points[0].ReadPoint = "missing" }},
		{name: "read point with write fields", change: func(value *Config) { value.Points[0].WritePoint = "feedback" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			value.Points = append([]PointDefinition(nil), valid.Points...)
			test.change(&value)
			if _, err := Normalize(value); err == nil {
				t.Fatal("Normalize unexpectedly accepted invalid table")
			}
		})
	}
}

func TestDecodeIgnoresFrontendOnlyPointFields(t *testing.T) {
	config, err := Decode(json.RawMessage(`{
		"scanIntervalMs": 50,
		"points": [{
			"pointId": "machine.ready",
			"address": "M0.1",
			"type": "bool",
			"access": "read",
			"readPoint": "machine.ready",
			"displayPath": "home.ready",
			"component": "indicator",
			"label": "设备就绪"
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Points) != 1 || config.Points[0].PointID != "machine.ready" {
		t.Fatalf("decoded configuration = %#v", config)
	}
}
