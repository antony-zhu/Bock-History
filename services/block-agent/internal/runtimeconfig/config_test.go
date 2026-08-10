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
		"scanIntervalMs": 500,
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

func TestNormalizeAcceptsExplicitSimulatorFloat32Profile(t *testing.T) {
	config, err := Normalize(Config{ScanIntervalMs: RequiredScanIntervalMs, Points: []PointDefinition{
		{
			PointID: "manual.motion.x.jog.speed.parameter", Address: "D800", Type: "float32", Access: "read_write",
			ReadPoint: "manual.motion.x.jog.speed.parameter", WritePoint: "manual.motion.x.jog.speed.parameter", WriteMethod: "fc10",
			RegisterCount: 2, WordOrder: "low-high", Write: &WriteDefinition{Mode: "set"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if point := config.Points[0]; point.RegisterCount != 2 || point.WordOrder != "low-high" || point.WriteMethod != "fc10" {
		t.Fatalf("normalized simulator profile = %#v", point)
	}
}

func TestNormalizeAllowsInt32FC10WordOrders(t *testing.T) {
	valid := Config{ScanIntervalMs: RequiredScanIntervalMs, Points: []PointDefinition{{
		PointID: "maintenance.production.target", Address: "D1000", Type: "int32", Access: "read_write",
		ReadPoint: "maintenance.production.target", WritePoint: "maintenance.production.target", WriteMethod: "fc10",
		RegisterCount: 2, WordOrder: "high-low", Write: &WriteDefinition{Mode: "set"},
	}}}
	if _, err := Normalize(valid); err != nil {
		t.Fatalf("Normalize high-low int32: %v", err)
	}
	lowHigh := valid
	lowHigh.Points = append([]PointDefinition(nil), valid.Points...)
	lowHigh.Points[0].WordOrder = "low-high"
	if _, err := Normalize(lowHigh); err != nil {
		t.Fatalf("Normalize low-high int32: %v", err)
	}

	for _, change := range []func(*PointDefinition){
		func(point *PointDefinition) { point.RegisterCount = 1 },
		func(point *PointDefinition) { point.WordOrder = "byte-swap" },
		func(point *PointDefinition) { point.WriteMethod = "fc06" },
	} {
		invalid := valid
		invalid.Points = append([]PointDefinition(nil), valid.Points...)
		change(&invalid.Points[0])
		if _, err := Normalize(invalid); err == nil {
			t.Fatal("Normalize unexpectedly accepted an invalid int32 profile")
		}
	}
}

func TestNormalizeAllowsWriteOnlyPulseAndRejectsUndeclaredFloat32Layout(t *testing.T) {
	writeOnly := Config{ScanIntervalMs: RequiredScanIntervalMs, Points: []PointDefinition{{
		PointID: "manual.motion.x.relative.trigger.action", Address: "D550.3", Type: "bool", Access: "write",
		WritePoint: "manual.motion.x.relative.trigger.action", WriteMethod: "maskWrite",
		Write: &WriteDefinition{Mode: "pulse", ActiveValue: true, DefaultValue: false},
	}}}
	normalized, err := Normalize(writeOnly)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Points[0].Write.PulseMs != DefaultPulseMs {
		t.Fatalf("write-only pulse default = %d", normalized.Points[0].Write.PulseMs)
	}

	invalid := Config{ScanIntervalMs: RequiredScanIntervalMs, Points: []PointDefinition{{
		PointID: "manual.invalid.float", Address: "D800", Type: "float32", Access: "read",
		ReadPoint: "manual.invalid.float",
	}}}
	if _, err := Normalize(invalid); err == nil {
		t.Fatal("Normalize unexpectedly accepted float32 without a span and word order")
	}
}
