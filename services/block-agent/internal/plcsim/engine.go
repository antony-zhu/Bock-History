package plcsim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"block.local/block-agent/internal/config"
	"block.local/block-agent/internal/plccontract"
)

const persistenceSchemaVersion = 2

type processedCommand struct {
	Fingerprint string                    `json:"fingerprint"`
	Result      plccontract.CommandResult `json:"result"`
}

type persistedState struct {
	SchemaVersion    int                         `json:"schemaVersion"`
	SessionID        string                      `json:"simulatorSessionId"`
	SampleSequence   uint64                      `json:"sampleSequence"`
	GeneratedAt      time.Time                   `json:"generatedAt"`
	NextProductionAt time.Time                   `json:"nextProductionAt,omitempty"`
	RejectBin        int                         `json:"rejectBin"`
	Points           plccontract.Points          `json:"points"`
	Faults           plccontract.Faults          `json:"faults"`
	Processed        map[string]processedCommand `json:"processedCommands"`
}

type Engine struct {
	mu     sync.Mutex
	config config.Simulator
	state  persistedState
	now    func() time.Time
}

func Open(cfg config.Simulator, now func() time.Time) (*Engine, error) {
	if now == nil {
		now = time.Now
	}
	engine := &Engine{config: cfg, now: now}
	contents, err := os.ReadFile(cfg.StateFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		engine.state, err = engine.initialState()
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fmt.Errorf("read simulator state %s: %w", cfg.StateFile, err)
	default:
		if err := json.Unmarshal(contents, &engine.state); err != nil {
			return nil, fmt.Errorf("decode simulator state %s: %w", cfg.StateFile, err)
		}
		if err := engine.validatePersisted(); err != nil {
			return nil, fmt.Errorf("invalid simulator state %s: %w", cfg.StateFile, err)
		}
		engine.state.SessionID, err = newSessionID()
		if err != nil {
			return nil, err
		}
		engine.state.GeneratedAt = now().UTC()
		if canProduce(engine.state.Points) {
			engine.state.NextProductionAt = engine.state.GeneratedAt.Add(time.Duration(engine.state.Points.CycleSeconds) * time.Second)
		}
	}
	engine.applyFaultState(&engine.state)
	engine.refreshDerived(&engine.state)
	if err := engine.persist(engine.state); err != nil {
		return nil, fmt.Errorf("persist simulator startup state: %w", err)
	}
	return engine, nil
}

func (e *Engine) initialState() (persistedState, error) {
	sessionID, err := newSessionID()
	if err != nil {
		return persistedState{}, err
	}
	points := plccontract.Points{
		ControlRevision: 1,
		Mode:            "auto", SafetyReady: true, GuardDoorClosed: true, PLCConnected: true,
		Target: e.config.InitialTarget, CycleSeconds: e.config.InitialCycleSeconds,
		ToolLimit: e.config.InitialToolLimit, InspectInterval: e.config.InitialInspectInterval,
		Blank: e.config.BinCapacities[0],
	}
	state := persistedState{
		SchemaVersion: persistenceSchemaVersion,
		SessionID:     sessionID,
		GeneratedAt:   e.now().UTC(),
		Points:        points,
		Processed:     make(map[string]processedCommand),
	}
	e.refreshDerived(&state)
	return state, nil
}

func (e *Engine) validatePersisted() error {
	if e.state.SchemaVersion != persistenceSchemaVersion {
		return fmt.Errorf("unsupported schema version %d", e.state.SchemaVersion)
	}
	if e.state.SessionID == "" || e.state.GeneratedAt.IsZero() || e.state.Points.ControlRevision == 0 {
		return errors.New("session id, generated timestamp and control revision are required")
	}
	points := e.state.Points
	if points.CycleSeconds <= 0 {
		return errors.New("cycleSeconds must be positive")
	}
	if points.Target <= 0 || points.ToolLimit <= 0 || points.InspectInterval <= 0 {
		return errors.New("target, toolLimit and inspectInterval must be positive")
	}
	if points.Mode != "auto" && points.Mode != "manual" {
		return fmt.Errorf("unsupported persisted mode %q", points.Mode)
	}
	if points.ToolUsed < 0 || points.Blank < 0 || points.Finished < 0 {
		return errors.New("tool and bin counters must not be negative")
	}
	if len(e.config.BinCapacities) != 3 ||
		points.Blank > e.config.BinCapacities[0] ||
		points.Finished > e.config.BinCapacities[1] {
		return errors.New("persisted bin quantity is outside configured capacity")
	}
	if e.state.Processed == nil {
		e.state.Processed = make(map[string]processedCommand)
	}
	if err := validateCounters(e.state.Points); err != nil {
		return err
	}
	if e.state.RejectBin < 0 || e.state.RejectBin > e.config.BinCapacities[2] {
		return errors.New("reject bin quantity is outside configured capacity")
	}
	return nil
}

func (e *Engine) Snapshot() plccontract.Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return snapshotOf(e.state)
}

func (e *Engine) Tick() (plccontract.Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Faults.FreezeData {
		return snapshotOf(e.state), nil
	}
	candidate := clonePersisted(e.state)
	candidate.SampleSequence++
	candidate.GeneratedAt = e.now().UTC()
	e.applyFaultState(&candidate)
	points := &candidate.Points
	cycle := time.Duration(points.CycleSeconds) * time.Second
	if !canProduce(*points) || !e.hasProductionCapacity(candidate) {
		candidate.NextProductionAt = time.Time{}
	} else {
		if candidate.NextProductionAt.IsZero() {
			candidate.NextProductionAt = candidate.GeneratedAt.Add(cycle)
		}
		for !candidate.GeneratedAt.Before(candidate.NextProductionAt) && canProduce(*points) && e.hasProductionCapacity(candidate) {
			e.produceOne(&candidate)
			candidate.NextProductionAt = candidate.NextProductionAt.Add(cycle)
		}
	}
	if err := validateCounters(*points); err != nil {
		return plccontract.Snapshot{}, err
	}
	e.refreshDerived(&candidate)
	if err := e.persist(candidate); err != nil {
		return plccontract.Snapshot{}, err
	}
	e.state = candidate
	return snapshotOf(e.state), nil
}

func (e *Engine) ApplyCommand(command plccontract.Command) (plccontract.CommandResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	command = plccontract.NormalizeCommand(command)
	if command.CommandID == "" || len(command.CommandID) > 128 {
		return plccontract.CommandResult{}, errors.New("commandId must contain 1 to 128 characters")
	}
	fingerprint := plccontract.CommandFingerprint(command)
	if previous, ok := e.state.Processed[command.CommandID]; ok {
		if previous.Fingerprint != fingerprint {
			return plccontract.CommandResult{
				CommandID: command.CommandID, Name: command.Name, Status: plccontract.CommandRejected,
				Code:            plccontract.ResultCodeIdempotencyConflict,
				Reason:          "commandId is already bound to different command content",
				ControlRevision: e.state.Points.ControlRevision,
			}, nil
		}
		return cloneResult(previous.Result), nil
	}
	result := plccontract.CommandResult{CommandID: command.CommandID, Name: command.Name, ControlRevision: e.state.Points.ControlRevision}
	if command.ExpectedControlRevision != nil && *command.ExpectedControlRevision != e.state.Points.ControlRevision {
		result.Status = plccontract.CommandRejected
		result.Code = plccontract.ResultCodeRevisionConflict
		result.Reason = "control revision conflict"
		return e.recordResult(command, fingerprint, result)
	}
	if e.state.Faults.RejectCommands {
		result.Status = plccontract.CommandRejected
		result.Code = plccontract.ResultCodeCommandRejected
		result.Reason = "fault injection rejected command"
		return e.recordResult(command, fingerprint, result)
	}
	if e.state.Faults.FailCommands {
		result.Status = plccontract.CommandFailed
		result.Code = plccontract.ResultCodeCommandFailed
		result.Reason = "fault injection failed command"
		return e.recordResult(command, fingerprint, result)
	}
	if !e.state.Points.PLCConnected {
		result.Status = plccontract.CommandRejected
		result.Code = plccontract.ResultCodeDeviceUnavailable
		result.Reason = "PLC is disconnected"
		return e.recordResult(command, fingerprint, result)
	}

	candidate := clonePersisted(e.state)
	points := &candidate.Points
	result.SimulationOnly = simulationOnly(command.Name)
	code, err := e.applyCommand(&candidate, command)
	if err != nil {
		result.Status = plccontract.CommandRejected
		result.Code = code
		result.Reason = err.Error()
		return e.recordResult(command, fingerprint, result)
	}
	points.ControlRevision++
	candidate.GeneratedAt = e.now().UTC()
	e.applyFaultState(&candidate)
	if canProduce(*points) {
		if command.Name == "start" || command.Name == "set_mode" || command.Name == "set_single_paused" || command.Name == "set_frame_paused" || candidate.NextProductionAt.IsZero() {
			candidate.NextProductionAt = candidate.GeneratedAt.Add(time.Duration(points.CycleSeconds) * time.Second)
		}
	} else {
		candidate.NextProductionAt = time.Time{}
	}
	e.refreshDerived(&candidate)
	if err := validateCounters(*points); err != nil {
		return plccontract.CommandResult{}, err
	}
	result.Status = plccontract.CommandExecuted
	result.ControlRevision = points.ControlRevision
	snapshot := snapshotOf(candidate)
	result.Snapshot = &snapshot
	candidate.Processed[command.CommandID] = processedCommand{Fingerprint: fingerprint, Result: cloneResult(result)}
	if err := e.persist(candidate); err != nil {
		return plccontract.CommandResult{}, err
	}
	e.state = candidate
	return cloneResult(result), nil
}

func (e *Engine) applyCommand(candidate *persistedState, command plccontract.Command) (string, error) {
	points := &candidate.Points
	switch command.Name {
	case "start":
		if points.EmergencyStop || !points.GuardDoorClosed || !points.SafetyReady {
			return plccontract.ResultCodeSafetyInterlock, errors.New("start rejected by safety interlock")
		}
		points.Running = true
	case "pause":
		points.Running = false
	case "reset":
		points.Running = false
		points.SinglePaused = false
		points.FramePaused = false
		points.Output, points.Inspected, points.Passed, points.NG, points.Pending = 0, 0, 0, 0, 0
		points.ToolUsed = 0
		points.Blank = e.config.BinCapacities[0]
		points.Finished = 0
		candidate.RejectBin = 0
	case "set_mode":
		if command.Mode != "auto" && command.Mode != "manual" {
			return plccontract.ResultCodeCommandRejected, errors.New("mode must be auto or manual")
		}
		points.Mode = command.Mode
	case "set_single_paused":
		if command.Paused == nil {
			return plccontract.ResultCodeCommandRejected, errors.New("paused is required")
		}
		points.SinglePaused = *command.Paused
	case "set_frame_paused":
		if command.Paused == nil {
			return plccontract.ResultCodeCommandRejected, errors.New("paused is required")
		}
		points.FramePaused = *command.Paused
	case "inspect":
		if points.Inspected >= points.Output {
			return plccontract.ResultCodeCommandRejected, errors.New("no produced item is pending inspection")
		}
		e.inspectOne(candidate, true)
	case "clear_bins":
		points.Blank, points.Finished, candidate.RejectBin = 0, 0, 0
	case "update_settings":
		if command.Settings == nil || command.Settings.Target < 1 || command.Settings.ToolLimit < 1 || command.Settings.InspectInterval < 1 {
			return plccontract.ResultCodeCommandRejected, errors.New("positive target, toolLimit and inspectInterval are required")
		}
		points.Target = command.Settings.Target
		points.ToolLimit = command.Settings.ToolLimit
		points.InspectInterval = command.Settings.InspectInterval
	case "acknowledge_alarm":
		found := false
		for index := range points.Alarms {
			if points.Alarms[index].AlarmID == command.AlarmID && points.Alarms[index].Active {
				points.Alarms[index].Acknowledged = true
				found = true
			}
		}
		if !found {
			return plccontract.ResultCodeAlarmNotFound, errors.New("active alarm not found")
		}
	default:
		return plccontract.ResultCodeCommandRejected, fmt.Errorf("unsupported command %q", command.Name)
	}
	return "", nil
}

func (e *Engine) recordResult(command plccontract.Command, fingerprint string, result plccontract.CommandResult) (plccontract.CommandResult, error) {
	candidate := clonePersisted(e.state)
	candidate.Processed[command.CommandID] = processedCommand{Fingerprint: fingerprint, Result: cloneResult(result)}
	if err := e.persist(candidate); err != nil {
		return plccontract.CommandResult{}, err
	}
	e.state = candidate
	return cloneResult(result), nil
}

func (e *Engine) SetFault(request plccontract.FaultRequest) (plccontract.Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.config.FaultInjectionEnabled {
		return plccontract.Snapshot{}, errors.New("fault injection is disabled")
	}
	candidate := clonePersisted(e.state)
	enabled := request.Enabled != nil && *request.Enabled
	switch request.Fault {
	case "plc_disconnected":
		candidate.Faults.PLCDisconnected = enabled
	case "freeze_data":
		candidate.Faults.FreezeData = enabled
	case "emergency_stop":
		candidate.Faults.EmergencyStop = enabled
	case "guard_door_open":
		candidate.Faults.GuardDoorOpen = enabled
	case "severe_alarm":
		candidate.Faults.SevereAlarm = enabled
	case "command_reject":
		candidate.Faults.RejectCommands = enabled
	case "command_fail":
		candidate.Faults.FailCommands = enabled
	case "applied_response_timeout":
		candidate.Faults.AppliedResponseTimeout = enabled
	case "response_delay":
		if request.DelayMillis == nil || *request.DelayMillis < 0 || *request.DelayMillis > 60000 {
			return plccontract.Snapshot{}, errors.New("delayMillis must be between 0 and 60000")
		}
		candidate.Faults.ResponseDelayMillis = *request.DelayMillis
	case "quality":
		if request.Quality != "" && request.Quality != plccontract.QualityGood && request.Quality != plccontract.QualityUncertain && request.Quality != plccontract.QualityBad {
			return plccontract.Snapshot{}, errors.New("quality must be GOOD, UNCERTAIN, BAD or empty")
		}
		candidate.Faults.ForcedQuality = request.Quality
	default:
		return plccontract.Snapshot{}, fmt.Errorf("unsupported fault %q", request.Fault)
	}
	candidate.GeneratedAt = e.now().UTC()
	e.applyFaultState(&candidate)
	e.refreshDerived(&candidate)
	if err := e.persist(candidate); err != nil {
		return plccontract.Snapshot{}, err
	}
	e.state = candidate
	return snapshotOf(e.state), nil
}

func (e *Engine) RestartSession() (plccontract.Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.config.FaultInjectionEnabled {
		return plccontract.Snapshot{}, errors.New("fault injection is disabled")
	}
	candidate := clonePersisted(e.state)
	var err error
	candidate.SessionID, err = newSessionID()
	if err != nil {
		return plccontract.Snapshot{}, err
	}
	candidate.SampleSequence = 0
	candidate.GeneratedAt = e.now().UTC()
	if canProduce(candidate.Points) {
		candidate.NextProductionAt = candidate.GeneratedAt.Add(time.Duration(candidate.Points.CycleSeconds) * time.Second)
	} else {
		candidate.NextProductionAt = time.Time{}
	}
	if err := e.persist(candidate); err != nil {
		return plccontract.Snapshot{}, err
	}
	e.state = candidate
	return snapshotOf(e.state), nil
}

func (e *Engine) ResponseBehavior() (time.Duration, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Duration(e.state.Faults.ResponseDelayMillis) * time.Millisecond, e.state.Faults.AppliedResponseTimeout
}

func (e *Engine) applyFaultState(candidate *persistedState) {
	points := &candidate.Points
	points.PLCConnected = !candidate.Faults.PLCDisconnected
	points.EmergencyStop = candidate.Faults.EmergencyStop
	points.GuardDoorClosed = !candidate.Faults.GuardDoorOpen
	points.SafetyReady = points.PLCConnected && !points.EmergencyStop && points.GuardDoorClosed
	if !points.SafetyReady {
		points.Running = false
	}
}

func (e *Engine) refreshDerived(candidate *persistedState) {
	points := &candidate.Points
	points.Bins = []plccontract.Bin{
		{ID: "BIN-01", Quantity: points.Blank, Capacity: e.config.BinCapacities[0]},
		{ID: "BIN-02", Quantity: points.Finished, Capacity: e.config.BinCapacities[1]},
		{ID: "BIN-03", Quantity: candidate.RejectBin, Capacity: e.config.BinCapacities[2]},
	}
	for index := range points.Bins {
		points.Bins[index].Status = binStatus(points.Bins[index].Quantity, points.Bins[index].Capacity)
	}
	if candidate.Faults.SevereAlarm {
		points.Bins[2].Status = "fault"
	}
	previous := make(map[string]plccontract.Alarm, len(points.Alarms))
	for _, item := range points.Alarms {
		previous[item.AlarmID] = item
	}
	points.Alarms = nil
	e.addAlarm(candidate, previous, points.EmergencyStop, "9001", "E_STOP", "danger", "急停已激活")
	e.addAlarm(candidate, previous, !points.GuardDoorClosed, "9002", "GUARD_DOOR", "danger", "安全门已打开")
	e.addAlarm(candidate, previous, points.ToolUsed >= points.ToolLimit, "1001", "TOOL_LIMIT", "warning", "刀具使用次数达到上限")
	e.addAlarm(candidate, previous, points.Bins[1].Status == "warning" || points.Bins[1].Status == "full", "2001", "FINISHED_BIN", "warning", "成品料仓接近或达到容量")
	e.addAlarm(candidate, previous, candidate.Faults.SevereAlarm, "9999", "SIM_SEVERE", "danger", "模拟严重报警")
}

func (e *Engine) addAlarm(candidate *persistedState, previous map[string]plccontract.Alarm, active bool, id, code, level, text string) {
	if !active {
		return
	}
	item, existed := previous[id]
	if !existed {
		item = plccontract.Alarm{AlarmID: id, Code: code, Level: level, Text: text, OccurredAt: candidate.GeneratedAt}
	}
	item.Active = true
	item.ClearedAt = nil
	candidate.Points.Alarms = append(candidate.Points.Alarms, item)
}

func (e *Engine) persist(value persistedState) error {
	directory := filepath.Dir(e.config.StateFile)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".plc-simulator-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	keep := true
	defer func() {
		_ = temporary.Close()
		if keep {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, e.config.StateFile); err != nil {
		return err
	}
	keep = false
	if handle, err := os.Open(directory); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}

func snapshotOf(value persistedState) plccontract.Snapshot {
	quality := plccontract.QualityGood
	if !value.Points.PLCConnected {
		quality = plccontract.QualityBad
	}
	if value.Faults.ForcedQuality != "" {
		quality = value.Faults.ForcedQuality
	}
	points := clonePoints(value.Points)
	return plccontract.Snapshot{
		SchemaVersion:      plccontract.SchemaVersion,
		SimulatorSessionID: value.SessionID,
		SampleSequence:     value.SampleSequence,
		GeneratedAt:        value.GeneratedAt,
		Quality:            quality,
		Points:             points,
	}
}

func clonePersisted(value persistedState) persistedState {
	value.Points = clonePoints(value.Points)
	original := value.Processed
	value.Processed = make(map[string]processedCommand, len(original))
	for key, processed := range original {
		processed.Result = cloneResult(processed.Result)
		value.Processed[key] = processed
	}
	return value
}

func canProduce(points plccontract.Points) bool {
	return points.PLCConnected && points.Running && points.Mode == "auto" &&
		!points.SinglePaused && !points.FramePaused && points.SafetyReady &&
		!points.EmergencyStop && points.GuardDoorClosed
}

func (e *Engine) hasProductionCapacity(candidate persistedState) bool {
	points := candidate.Points
	return points.Output < points.Target && points.Blank > 0 &&
		points.Finished < e.config.BinCapacities[1] && candidate.RejectBin < e.config.BinCapacities[2]
}

func (e *Engine) produceOne(candidate *persistedState) {
	points := &candidate.Points
	points.Output++
	points.ToolUsed++
	points.Blank--
	if points.InspectInterval > 0 && points.Output%points.InspectInterval == 0 {
		e.inspectOne(candidate, false)
	} else {
		points.Finished++
	}
	points.Pending = points.Output - points.Inspected
}

func (e *Engine) inspectOne(candidate *persistedState, fromFinishedBin bool) {
	points := &candidate.Points
	points.Inspected++
	if deterministicPass(e.config.Seed, uint64(points.Inspected), e.config.PassRate) {
		points.Passed++
		if !fromFinishedBin {
			points.Finished++
		}
	} else {
		points.NG++
		candidate.RejectBin++
		if fromFinishedBin && points.Finished > 0 {
			points.Finished--
		}
	}
	points.Pending = points.Output - points.Inspected
}

func validateCounters(points plccontract.Points) error {
	if points.Output < 0 || points.Inspected < 0 || points.Passed < 0 || points.NG < 0 {
		return errors.New("production counters must not be negative")
	}
	if points.Inspected > points.Output {
		return errors.New("inspected count exceeds produced output")
	}
	if points.Passed+points.NG != points.Inspected {
		return errors.New("passed plus NG must equal inspected")
	}
	if points.Pending != points.Output-points.Inspected {
		return errors.New("pending count must equal output minus inspected")
	}
	return nil
}

func clonePoints(value plccontract.Points) plccontract.Points {
	value.Bins = append([]plccontract.Bin(nil), value.Bins...)
	value.Alarms = append([]plccontract.Alarm(nil), value.Alarms...)
	return value
}

func cloneResult(value plccontract.CommandResult) plccontract.CommandResult {
	if value.Snapshot != nil {
		snapshot := *value.Snapshot
		snapshot.Points = clonePoints(snapshot.Points)
		value.Snapshot = &snapshot
	}
	return value
}

func newSessionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate simulator session id: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func deterministicPass(seed int64, index uint64, passRate float64) bool {
	value := uint64(seed) + index*0x9e3779b97f4a7c15
	value ^= value >> 30
	value *= 0xbf58476d1ce4e5b9
	value ^= value >> 27
	value *= 0x94d049bb133111eb
	value ^= value >> 31
	return float64(value%1_000_000)/1_000_000 < passRate
}

func binStatus(quantity, capacity int) string {
	if quantity <= 0 {
		return "empty"
	}
	if quantity >= capacity {
		return "full"
	}
	if quantity*100 >= capacity*80 {
		return "warning"
	}
	return "normal"
}

func simulationOnly(name string) bool {
	return name == "clear_bins" || name == "update_settings" || name == "acknowledge_alarm"
}
