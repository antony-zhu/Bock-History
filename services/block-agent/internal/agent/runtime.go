package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"block.local/block-agent/internal/adapter"
	"block.local/block-agent/internal/bdm"
	"block.local/block-agent/internal/command"
	"block.local/block-agent/internal/config"
	"block.local/block-agent/internal/localapi"
	"block.local/block-agent/internal/storage"
	"block.local/block-agent/internal/uplink"
)

type Runtime struct {
	config         config.Agent
	device         *adapter.Coordinator
	store          *storage.Store
	queue          *command.Queue
	api            *localapi.Server
	bdm            *bdm.Manager
	samplePeriod   time.Duration
	staleAfter     time.Duration
	commandTimeout time.Duration
	now            func() time.Time
	closeOnce      sync.Once
}

func Open(cfg config.Agent, now func() time.Time) (*Runtime, error) {
	if now == nil {
		now = time.Now
	}
	samplePeriod, staleAfter, commandTimeout, err := cfg.Durations()
	if err != nil {
		return nil, err
	}
	var (
		store      *storage.Store
		bdmManager *bdm.Manager
	)
	if cfg.BDM.Enabled {
		bootID, err := uplink.NewUUID()
		if err != nil {
			return nil, fmt.Errorf("create Block boot identity: %w", err)
		}
		source := uplink.Source{
			SiteID: cfg.SiteID, BlockID: cfg.BlockID, DeviceID: cfg.DeviceID,
		}
		store, err = storage.OpenWithOptions(cfg.DatabasePath, now, storage.UplinkOptions{
			Enabled: true, Source: source, BootID: bootID,
			StreamGeneration: cfg.BDM.StreamGeneration, StaleAfter: staleAfter,
		})
		if err == nil {
			bdmManager = bdm.New(cfg.BDM, source, bootID, store, now)
		}
	} else {
		store, err = storage.Open(cfg.DatabasePath, now)
	}
	if err != nil {
		return nil, fmt.Errorf("open Block database: %w", err)
	}
	rawDevice, err := adapter.New(cfg.Adapter, commandTimeout)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	device := adapter.NewCoordinator(rawDevice)
	queue := command.New(device, store, commandTimeout, staleAfter, now)
	return &Runtime{
		config: cfg, device: device, store: store, queue: queue,
		api:          localapi.New(cfg.LocalAPISocket, cfg.LocalAPISocketGroup, cfg.Adapter.Type, store, queue, staleAfter, now),
		bdm:          bdmManager,
		samplePeriod: samplePeriod, staleAfter: staleAfter,
		commandTimeout: commandTimeout, now: now,
	}, nil
}

func (r *Runtime) Close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		r.queue.Close()
		r.device.Close()
		closeErr = r.store.Close()
	})
	return closeErr
}

func (r *Runtime) Run(ctx context.Context) error {
	defer r.Close()
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsChannel := make(chan error, 1)
	go func() { errorsChannel <- r.api.Serve(runContext) }()
	if r.bdm != nil {
		// BDM is an optional observer. Its certificate, DNS, route, broker or
		// protocol failures never terminate the local sampling/HMI loop.
		go func() { _ = r.bdm.Run(runContext) }()
	}
	_ = r.SampleOnce(runContext)
	ticker := time.NewTicker(r.samplePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-r.queue.Errors():
			return fmt.Errorf("command queue stopped after persistence failure: %w", err)
		case err := <-errorsChannel:
			if err == nil && ctx.Err() != nil {
				return nil
			}
			if err == nil {
				return errors.New("Block Local API stopped unexpectedly")
			}
			return fmt.Errorf("Block Local API stopped: %w", err)
		case <-ticker.C:
			_ = r.SampleOnce(runContext)
		}
	}
}

func (r *Runtime) SampleOnce(parent context.Context) error {
	var sampleErr error
	r.device.Do(func(device adapter.Adapter) {
		readContext, cancel := context.WithTimeout(parent, r.commandTimeout)
		snapshot, err := device.Read(readContext)
		cancel()
		if err != nil {
			if parent.Err() == nil {
				code := storage.AvailabilityDeviceUnavailable
				if errors.Is(err, adapter.ErrBadData) {
					code = storage.AvailabilityBadQuality
				}
				r.markUnavailable(code)
			}
			sampleErr = err
			return
		}
		if err := adapter.ValidateSnapshot(snapshot); err != nil {
			r.markUnavailable(storage.AvailabilityBadQuality)
			sampleErr = err
			return
		}
		now := r.now().UTC()
		_, err = r.store.SavePLC(parent, snapshot, now, r.staleAfter)
		switch {
		case errors.Is(err, storage.ErrStaleSnapshot):
			r.markUnavailable(storage.AvailabilityDataStale)
		case err != nil:
			r.store.SetSourceUnavailable(storage.AvailabilityBackendUnavailable)
		}
		sampleErr = err
	})
	return sampleErr
}

func (r *Runtime) markUnavailable(code string) {
	r.store.SetSourceUnavailable(code)
	ctx, cancel := context.WithTimeout(context.Background(), r.commandTimeout)
	defer cancel()
	_ = r.store.MarkStale(ctx, code)
}

func (r *Runtime) Store() *storage.Store {
	return r.store
}
