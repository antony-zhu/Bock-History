// Package pointstore holds the current session's PLC values in memory.
package pointstore

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	"block.local/block-agent/internal/runtimeconfig"
)

type PointValue struct {
	Value       any       `json:"value"`
	Quality     string    `json:"quality"`
	UpdatedAt   time.Time `json:"updatedAt"`
	AlarmActive *bool     `json:"alarmActive"`
}

type Store struct {
	mu          sync.RWMutex
	definitions map[string]runtimeconfig.PointDefinition
	values      map[string]PointValue
}

func New() *Store {
	return &Store{definitions: map[string]runtimeconfig.PointDefinition{}, values: map[string]PointValue{}}
}

// Replace atomically discards the previous session plan and all of its values.
func (s *Store) Replace(config runtimeconfig.Config) error {
	normalized, err := runtimeconfig.Normalize(config)
	if err != nil {
		return err
	}
	definitions := make(map[string]runtimeconfig.PointDefinition, len(normalized.Points))
	for _, point := range normalized.Points {
		definitions[point.PointID] = cloneDefinition(point)
	}
	s.mu.Lock()
	s.definitions = definitions
	s.values = map[string]PointValue{}
	s.mu.Unlock()
	return nil
}

// Clear ends the current runtime session. Nothing is persisted.
func (s *Store) Clear() {
	s.mu.Lock()
	s.definitions = map[string]runtimeconfig.PointDefinition{}
	s.values = map[string]PointValue{}
	s.mu.Unlock()
}

func (s *Store) Configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.definitions) > 0
}

func (s *Store) Definition(pointID string) (runtimeconfig.PointDefinition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	point, ok := s.definitions[pointID]
	return cloneDefinition(point), ok
}

func (s *Store) Snapshot() map[string]PointValue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make(map[string]PointValue, len(s.values))
	for pointID, value := range s.values {
		values[pointID] = cloneValue(value)
	}
	return values
}

// Update applies confirmed absolute values and returns only values whose
// displayed state changed. The update timestamp alone is not a WS change.
func (s *Store) Update(values map[string]PointValue) (map[string]PointValue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := make(map[string]PointValue)
	for pointID, value := range values {
		definition, exists := s.definitions[pointID]
		if !exists {
			return nil, fmt.Errorf("point %q is not configured", pointID)
		}
		if err := runtimeconfig.ValidateValue(definition.Type, value.Value); err != nil {
			return nil, fmt.Errorf("point %q value: %w", pointID, err)
		}
		if value.Quality != "good" && value.Quality != "stale" && value.Quality != "error" {
			return nil, fmt.Errorf("point %q quality is unsupported", pointID)
		}
		if value.UpdatedAt.IsZero() {
			return nil, fmt.Errorf("point %q updatedAt is required", pointID)
		}
		previous, hadPrevious := s.values[pointID]
		s.values[pointID] = cloneValue(value)
		if !hadPrevious || !sameDisplayState(previous, value) {
			changed[pointID] = cloneValue(value)
		}
	}
	return changed, nil
}

func sameDisplayState(left, right PointValue) bool {
	return left.Quality == right.Quality && reflect.DeepEqual(left.Value, right.Value) && boolEqual(left.AlarmActive, right.AlarmActive)
}

func boolEqual(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneDefinition(value runtimeconfig.PointDefinition) runtimeconfig.PointDefinition {
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

func cloneValue(value PointValue) PointValue {
	copyValue := value
	if value.AlarmActive != nil {
		alarmActive := *value.AlarmActive
		copyValue.AlarmActive = &alarmActive
	}
	return copyValue
}
