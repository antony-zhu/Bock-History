// Package maintenance stores the small set of local HMI maintenance values.
package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Production struct {
	TargetProduction   int `json:"targetProduction"`
	ToolChangePieces   int `json:"toolChangePieces"`
	InspectionInterval int `json:"inspectionInterval"`
	PiecesPerBox       int `json:"piecesPerBox"`
}

type ProductionPatch struct {
	TargetProduction   *int `json:"targetProduction"`
	ToolChangePieces   *int `json:"toolChangePieces"`
	InspectionInterval *int `json:"inspectionInterval"`
	PiecesPerBox       *int `json:"piecesPerBox"`
}

type Store struct {
	path string
	mu   sync.Mutex
	data Production
}

func DefaultProduction() Production {
	return Production{
		TargetProduction:   30,
		ToolChangePieces:   100,
		InspectionInterval: 30,
		PiecesPerBox:       1,
	}
}

func Open(path string) (*Store, error) {
	store := &Store{path: path, data: DefaultProduction()}
	if path == "" {
		return store, nil
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(contents, &store.data); err != nil {
		return nil, fmt.Errorf("read maintenance production data: %w", err)
	}
	if err := validate(store.data); err != nil {
		return nil, fmt.Errorf("read maintenance production data: %w", err)
	}
	return store, nil
}

func (s *Store) Get() Production {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

func (s *Store) Patch(patch ProductionPatch) (Production, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.data
	if patch.TargetProduction != nil {
		next.TargetProduction = *patch.TargetProduction
	}
	if patch.ToolChangePieces != nil {
		next.ToolChangePieces = *patch.ToolChangePieces
	}
	if patch.InspectionInterval != nil {
		next.InspectionInterval = *patch.InspectionInterval
	}
	if patch.PiecesPerBox != nil {
		next.PiecesPerBox = *patch.PiecesPerBox
	}
	if err := validate(next); err != nil {
		return Production{}, err
	}
	if err := writeAtomic(s.path, next); err != nil {
		return Production{}, err
	}
	s.data = next
	return next, nil
}

func validate(data Production) error {
	if data.TargetProduction < 1 || data.TargetProduction > 60000 {
		return errors.New("targetProduction must be between 1 and 60000")
	}
	if data.ToolChangePieces < 1 || data.ToolChangePieces > 99999 {
		return errors.New("toolChangePieces must be between 1 and 99999")
	}
	if data.InspectionInterval < 1 || data.InspectionInterval > 9999 {
		return errors.New("inspectionInterval must be between 1 and 9999")
	}
	if data.PiecesPerBox < 1 {
		return errors.New("piecesPerBox must be greater than zero")
	}
	return nil
}

func writeAtomic(path string, data Production) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	contents, err := json.Marshal(data)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".maintenance-production-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
