package adapter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"block.local/block-agent/internal/config"
	"block.local/block-agent/internal/plccontract"
)

var (
	ErrUnavailable    = errors.New("device transport is unavailable")
	ErrBadData        = errors.New("device returned malformed or invalid data")
	ErrDisabled       = fmt.Errorf("%w: device adapter is disabled", ErrUnavailable)
	ErrOutcomeUnknown = errors.New("command outcome is unknown")
)

type Adapter interface {
	Read(context.Context) (plccontract.Snapshot, error)
	Execute(context.Context, plccontract.Command) (plccontract.CommandResult, error)
	Close()
}

// Coordinator serializes every adapter access with the state transition that
// records its result. Queue command readiness and Runtime sampling must share
// one Coordinator so a sample cannot change availability between the final
// readiness check and device execution.
type Coordinator struct {
	mu      sync.Mutex
	adapter Adapter
}

func NewCoordinator(device Adapter) *Coordinator {
	return &Coordinator{adapter: device}
}

func (c *Coordinator) Do(operation func(Adapter)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	operation(c.adapter)
}

func (c *Coordinator) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adapter.Close()
}

func New(cfg config.AgentAdapter, timeout time.Duration) (Adapter, error) {
	switch cfg.Type {
	case "simulator":
		return NewSimulator(cfg.IOSocket, timeout), nil
	case "disabled":
		return Disabled{}, nil
	default:
		return nil, errors.New("unsupported adapter type")
	}
}

type Disabled struct{}

func (Disabled) Read(context.Context) (plccontract.Snapshot, error) {
	return plccontract.Snapshot{}, ErrDisabled
}

func (Disabled) Execute(context.Context, plccontract.Command) (plccontract.CommandResult, error) {
	return plccontract.CommandResult{}, ErrDisabled
}

func (Disabled) Close() {}

func ValidateSnapshot(snapshot plccontract.Snapshot) error {
	switch {
	case snapshot.SchemaVersion != plccontract.SchemaVersion:
		return fmt.Errorf("%w: unsupported schema %q", ErrBadData, snapshot.SchemaVersion)
	case snapshot.SimulatorSessionID == "":
		return fmt.Errorf("%w: simulator session id is empty", ErrBadData)
	case snapshot.GeneratedAt.IsZero():
		return fmt.Errorf("%w: generatedAt is zero", ErrBadData)
	case snapshot.Quality != plccontract.QualityGood &&
		snapshot.Quality != plccontract.QualityUncertain &&
		snapshot.Quality != plccontract.QualityBad:
		return fmt.Errorf("%w: unsupported quality %q", ErrBadData, snapshot.Quality)
	}
	return nil
}
