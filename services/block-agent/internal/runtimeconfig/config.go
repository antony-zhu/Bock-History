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
	PointID       string           `json:"pointId"`
	Address       string           `json:"address"`
	Type          string           `json:"type"`
	Access        string           `json:"access"`
	ReadPoint     string           `json:"readPoint"`
	WritePoint    string           `json:"writePoint"`
	WriteMethod   string           `json:"writeMethod"`
	RegisterCount int              `json:"registerCount"`
	WordOrder     string           `json:"wordOrder"`
	Write         *WriteDefinition `json:"write"`
	Alarm         *AlarmDefinition `json:"alarm"`
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
		if point.Access != "write" {
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
	if point.Access != "write" && strings.TrimSpace(point.ReadPoint) == "" {
		return fmt.Errorf("%s.readPoint is required", prefix)
	}
	if point.Access == "write" && strings.TrimSpace(point.ReadPoint) != "" {
		return fmt.Errorf("%s write-only point must not define readPoint", prefix)
	}
	if err := validateRegisterLayout(prefix, point); err != nil {
		return err
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
		if err := validateWriteMethod(prefix, point); err != nil {
			return err
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
	if write.Mode == "set" {
		return nil
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

func validateRegisterLayout(prefix string, point PointDefinition) error {
	switch point.Type {
	case "float32":
		if point.RegisterCount != 2 {
			return fmt.Errorf("%s.registerCount must be 2 for float32", prefix)
		}
		if point.WordOrder != "low-high" && point.WordOrder != "high-low" {
			return fmt.Errorf("%s.wordOrder must be low-high or high-low for float32", prefix)
		}
	case "int32":
		if point.RegisterCount != 2 {
			return fmt.Errorf("%s.registerCount must be 2 for int32", prefix)
		}
		if point.WordOrder != "high-low" {
			return fmt.Errorf("%s.wordOrder must be high-low for int32", prefix)
		}
	case "int16", "uint16":
		if point.RegisterCount != 1 {
			return fmt.Errorf("%s.registerCount must be 1 for %s", prefix, point.Type)
		}
		if point.WordOrder != "" {
			return fmt.Errorf("%s.wordOrder is only valid for int32 or float32", prefix)
		}
	default:
		if point.RegisterCount != 0 || point.WordOrder != "" {
			return fmt.Errorf("%s register layout is only valid for int16, uint16, int32, or float32", prefix)
		}
	}
	return nil
}

func validateWriteMethod(prefix string, point PointDefinition) error {
	switch point.Type {
	case "bool":
		if point.WriteMethod != "maskWrite" {
			return fmt.Errorf("%s bool writes require writeMethod maskWrite", prefix)
		}
	case "int16", "uint16":
		if point.WriteMethod != "fc06" {
			return fmt.Errorf("%s %s writes require writeMethod fc06", prefix, point.Type)
		}
	case "int32", "float32":
		if point.WriteMethod != "fc10" {
			return fmt.Errorf("%s %s writes require writeMethod fc10", prefix, point.Type)
		}
	default:
		return fmt.Errorf("%s type %q has no approved Easy521 write method", prefix, point.Type)
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
	case "int", "int16":
		if isInteger(value) {
			if pointType == "int16" && (numberAsFloat64(value) < -32768 || numberAsFloat64(value) > 32767) {
				break
			}
			return nil
		}
	case "int32":
		if isInteger(value) && numberAsFloat64(value) >= -2147483648 && numberAsFloat64(value) <= 2147483647 {
			return nil
		}
	case "uint16":
		if isInteger(value) && numberAsFloat64(value) >= 0 && numberAsFloat64(value) <= 65535 {
			return nil
		}
	case "float", "float32":
		if isNumber(value) {
			if pointType == "float32" && (math.IsNaN(numberAsFloat64(value)) || math.IsInf(numberAsFloat64(value), 0) || math.Abs(numberAsFloat64(value)) > math.MaxFloat32) {
				break
			}
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

func numberAsFloat64(value any) float64 {
	switch number := value.(type) {
	case int:
		return float64(number)
	case int8:
		return float64(number)
	case int16:
		return float64(number)
	case int32:
		return float64(number)
	case int64:
		return float64(number)
	case uint:
		return float64(number)
	case uint8:
		return float64(number)
	case uint16:
		return float64(number)
	case uint32:
		return float64(number)
	case uint64:
		return float64(number)
	case float32:
		return float64(number)
	case float64:
		return number
	case json.Number:
		converted, _ := number.Float64()
		return converted
	default:
		return 0
	}
}

func validType(value string) bool {
	return value == "bool" || value == "int" || value == "float" || value == "string" || value == "int16" || value == "uint16" || value == "int32" || value == "float32"
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
