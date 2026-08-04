// Package runtimeconfig validates the point table supplied by the local HMI.
package runtimeconfig

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
)

const (
	RequiredScanIntervalMs = 50
	DefaultPulseMs         = 100
)

type Config struct {
	ScanIntervalMs int               `json:"scanIntervalMs"`
	Points         []PointDefinition `json:"points"`
}

// PointDefinition contains only the fields the backend needs. Frontend-only
// fields such as component, label and displayPath are intentionally ignored
// when JSON is decoded into this type.
type PointDefinition struct {
	PointID     string           `json:"pointId"`
	Address     string           `json:"address"`
	Type        string           `json:"type"`
	Access      string           `json:"access"`
	ReadPoint   string           `json:"readPoint"`
	WritePoint  string           `json:"writePoint"`
	WriteMethod string           `json:"writeMethod"`
	Write       *WriteDefinition `json:"write"`
	Alarm       *AlarmDefinition `json:"alarm"`
}

type WriteDefinition struct {
	Mode         string `json:"mode"`
	ActiveValue  any    `json:"activeValue"`
	DefaultValue any    `json:"defaultValue"`
	PulseMs      int    `json:"pulseMs"`
}

type AlarmDefinition struct {
	NormalValue any    `json:"normalValue"`
	AlarmValue  any    `json:"alarmValue"`
	Message     string `json:"message"`
}

// Decode parses the runtime part of a configure message. Unknown fields inside
// a point are deliberately ignored because they are owned by the frontend.
func Decode(raw json.RawMessage) (Config, error) {
	var value Config
	if err := json.Unmarshal(raw, &value); err != nil {
		return Config{}, fmt.Errorf("decode runtime configuration: %w", err)
	}
	return Normalize(value)
}

// Normalize applies the documented pulse default and validates the complete
// point table before it becomes the active in-memory plan.
func Normalize(value Config) (Config, error) {
	copyValue := Config{
		ScanIntervalMs: value.ScanIntervalMs,
		Points:         make([]PointDefinition, len(value.Points)),
	}
	for index, point := range value.Points {
		copyValue.Points[index] = cloneDefinition(point)
		if write := copyValue.Points[index].Write; write != nil && write.Mode == "pulse" && write.PulseMs == 0 {
			write.PulseMs = DefaultPulseMs
		}
	}
	if err := Validate(copyValue); err != nil {
		return Config{}, err
	}
	return copyValue, nil
}

func Validate(value Config) error {
	if value.ScanIntervalMs != RequiredScanIntervalMs {
		return fmt.Errorf("scanIntervalMs must be %d", RequiredScanIntervalMs)
	}
	if len(value.Points) == 0 {
		return fmt.Errorf("points must not be empty")
	}

	byID := make(map[string]PointDefinition, len(value.Points))
	for index, point := range value.Points {
		if err := validatePoint(index, point); err != nil {
			return err
		}
		if _, exists := byID[point.PointID]; exists {
			return fmt.Errorf("points[%d].pointId duplicates %q", index, point.PointID)
		}
		byID[point.PointID] = point
	}

	for index, point := range value.Points {
		read, exists := byID[point.ReadPoint]
		if !exists {
			return fmt.Errorf("points[%d].readPoint %q does not exist", index, point.ReadPoint)
		}
		if read.Access == "write" {
			return fmt.Errorf("points[%d].readPoint %q is not readable", index, point.ReadPoint)
		}
		if read.Type != point.Type {
			return fmt.Errorf("points[%d].readPoint %q type does not match", index, point.ReadPoint)
		}
		if point.Access == "read" {
			continue
		}
		write, exists := byID[point.WritePoint]
		if !exists {
			return fmt.Errorf("points[%d].writePoint %q does not exist", index, point.WritePoint)
		}
		if write.Access == "read" {
			return fmt.Errorf("points[%d].writePoint %q is not writable", index, point.WritePoint)
		}
		if write.Type != point.Type {
			return fmt.Errorf("points[%d].writePoint %q type does not match", index, point.WritePoint)
		}
	}
	return nil
}

func validatePoint(index int, point PointDefinition) error {
	prefix := fmt.Sprintf("points[%d]", index)
	if strings.TrimSpace(point.PointID) == "" {
		return fmt.Errorf("%s.pointId is required", prefix)
	}
	if strings.TrimSpace(point.Address) == "" {
		return fmt.Errorf("%s.address is required", prefix)
	}
	if !validType(point.Type) {
		return fmt.Errorf("%s.type is unsupported", prefix)
	}
	if point.Access != "read" && point.Access != "write" && point.Access != "read_write" {
		return fmt.Errorf("%s.access is unsupported", prefix)
	}
	if strings.TrimSpace(point.ReadPoint) == "" {
		return fmt.Errorf("%s.readPoint is required", prefix)
	}

	if point.Access == "read" {
		if point.WritePoint != "" || point.WriteMethod != "" || point.Write != nil {
			return fmt.Errorf("%s read point must not define write fields", prefix)
		}
	} else {
		if strings.TrimSpace(point.WritePoint) == "" {
			return fmt.Errorf("%s.writePoint is required", prefix)
		}
		if strings.TrimSpace(point.WriteMethod) == "" {
			return fmt.Errorf("%s.writeMethod is required", prefix)
		}
		if err := validateWrite(prefix, point.Type, point.Write); err != nil {
			return err
		}
	}

	if point.Alarm != nil {
		if point.Access == "write" {
			return fmt.Errorf("%s alarm point must be readable", prefix)
		}
		if strings.TrimSpace(point.Alarm.Message) == "" {
			return fmt.Errorf("%s.alarm.message is required", prefix)
		}
		if err := ValidateValue(point.Type, point.Alarm.NormalValue); err != nil {
			return fmt.Errorf("%s.alarm.normalValue: %w", prefix, err)
		}
		if err := ValidateValue(point.Type, point.Alarm.AlarmValue); err != nil {
			return fmt.Errorf("%s.alarm.alarmValue: %w", prefix, err)
		}
		if reflect.DeepEqual(point.Alarm.NormalValue, point.Alarm.AlarmValue) {
			return fmt.Errorf("%s.alarm normalValue and alarmValue must differ", prefix)
		}
	}
	return nil
}

func validateWrite(prefix, pointType string, write *WriteDefinition) error {
	if write == nil {
		return fmt.Errorf("%s.write is required", prefix)
	}
	if write.Mode != "set" && write.Mode != "pulse" && write.Mode != "momentary" && write.Mode != "toggle" {
		return fmt.Errorf("%s.write.mode is unsupported", prefix)
	}
	if err := ValidateValue(pointType, write.ActiveValue); err != nil {
		return fmt.Errorf("%s.write.activeValue: %w", prefix, err)
	}
	if err := ValidateValue(pointType, write.DefaultValue); err != nil {
		return fmt.Errorf("%s.write.defaultValue: %w", prefix, err)
	}
	if write.Mode == "pulse" && write.PulseMs <= 0 {
		return fmt.Errorf("%s.write.pulseMs must be positive", prefix)
	}
	if (write.Mode == "pulse" || write.Mode == "momentary" || write.Mode == "toggle") && reflect.DeepEqual(write.ActiveValue, write.DefaultValue) {
		return fmt.Errorf("%s.write activeValue and defaultValue must differ", prefix)
	}
	return nil
}

// ValidateValue checks a value sent by the HMI against a configured point
// type. JSON numbers arrive as float64 after decoding into any.
func ValidateValue(pointType string, value any) error {
	switch pointType {
	case "bool":
		if _, ok := value.(bool); ok {
			return nil
		}
	case "int":
		if isInteger(value) {
			return nil
		}
	case "float":
		if isNumber(value) {
			return nil
		}
	case "string":
		if _, ok := value.(string); ok {
			return nil
		}
	default:
		return fmt.Errorf("unsupported point type %q", pointType)
	}
	return fmt.Errorf("must match point type %q", pointType)
}

func isInteger(value any) bool {
	switch number := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return math.Trunc(float64(number)) == float64(number)
	case float64:
		return math.Trunc(number) == number
	case json.Number:
		_, err := number.Int64()
		return err == nil
	default:
		return false
	}
}

func isNumber(value any) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	case json.Number:
		_, err := value.(json.Number).Float64()
		return err == nil
	default:
		return false
	}
}

func validType(value string) bool {
	return value == "bool" || value == "int" || value == "float" || value == "string"
}

func cloneDefinition(value PointDefinition) PointDefinition {
	copyValue := value
	if value.Write != nil {
		write := *value.Write
		copyValue.Write = &write
	}
	if value.Alarm != nil {
		alarm := *value.Alarm
		copyValue.Alarm = &alarm
	}
	return copyValue
}
