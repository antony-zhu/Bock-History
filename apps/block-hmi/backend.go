package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	persistenceSchemaVersion = 1
	maxAuditEntries          = 2000
)

var (
	errRevisionConflict    = errors.New("state revision conflict")
	errAlarmNotFound       = errors.New("alarm not found")
	errUnknownCommand      = errors.New("unknown device command")
	errIdempotencyConflict = errors.New("idempotency key conflict")
	errSafetyInterlock     = errors.New("safety interlock rejected command")
	errDeviceUnavailable   = errors.New("device unavailable")
	errBadQuality          = errors.New("device data quality unavailable")
	errDataStale           = errors.New("device data stale")
	errCommandFailed       = errors.New("device command failed")
	errOutcomeUnknown      = errors.New("device command outcome unknown")
	errSourceMismatch      = errors.New("Agent data source changed")
)

type SourceInfo struct {
	Kind       string
	Simulation bool
}

type Parameters struct {
	Target          int `json:"target"`
	ToolLimit       int `json:"toolLimit"`
	InspectInterval int `json:"inspectInterval"`
}

type BinState struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

type Alarm struct {
	ID           uint64    `json:"id"`
	Level        string    `json:"level"`
	Code         string    `json:"code"`
	Text         string    `json:"text"`
	OccurredAt   time.Time `json:"occurredAt"`
	Acknowledged bool      `json:"acknowledged"`
}

type HistoryEntry struct {
	ID        uint64    `json:"id"`
	Level     string    `json:"level"`
	Code      string    `json:"code"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

type HMIState struct {
	Revision        uint64         `json:"revision"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	Running         bool           `json:"running"`
	Mode            string         `json:"mode"`
	SinglePaused    bool           `json:"singlePaused"`
	FramePaused     bool           `json:"framePaused"`
	Target          int            `json:"target"`
	Output          int            `json:"output"`
	Cycle           int            `json:"cycle"`
	OEE             int            `json:"oee"`
	Inspected       int            `json:"inspected"`
	Passed          int            `json:"passed"`
	NG              int            `json:"ng"`
	Pending         int            `json:"pending"`
	Blank           int            `json:"blank"`
	Finished        int            `json:"finished"`
	ToolLimit       int            `json:"toolLimit"`
	InspectInterval int            `json:"inspectInterval"`
	Bins            []BinState     `json:"bins"`
	Alarms          []Alarm        `json:"alarms"`
	History         []HistoryEntry `json:"history"`
}

// MarshalJSON keeps collection fields stable for browser clients. Go's default
// encoding turns nil slices into null, but an empty alarm/history/bin set is a
// valid trusted state and must remain a JSON array.
func (state HMIState) MarshalJSON() ([]byte, error) {
	type stateAlias HMIState
	if state.Bins == nil {
		state.Bins = []BinState{}
	}
	if state.Alarms == nil {
		state.Alarms = []Alarm{}
	}
	if state.History == nil {
		state.History = []HistoryEntry{}
	}
	return json.Marshal(stateAlias(state))
}

type MutationMeta struct {
	Operator  string
	RequestID string
	CommandID string
}

type DeviceCommand struct {
	Name   string
	Mode   string
	Paused *bool
}

type AuditEntry struct {
	ID        uint64                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	Operator  string                 `json:"operator"`
	Action    string                 `json:"action"`
	Message   string                 `json:"message"`
	Revision  uint64                 `json:"revision"`
	RequestID string                 `json:"requestId,omitempty"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

type AuditPage struct {
	Items        []AuditEntry
	NextBeforeID *uint64
}

// HMIController is the boundary between HTTP and equipment/data access. The
// demo controller below can be replaced by a PLC/SCADA-backed implementation
// without changing the API handler or browser contract.
type HMIController interface {
	State(context.Context) (HMIState, error)
	UpdateSettings(context.Context, Parameters, MutationMeta, *uint64) (HMIState, string, error)
	ExecuteCommand(context.Context, DeviceCommand, MutationMeta, *uint64) (HMIState, string, error)
	AcknowledgeAlarm(context.Context, uint64, MutationMeta, *uint64) (HMIState, string, error)
	Audit(context.Context, int, *uint64) (AuditPage, error)
}

type persistedController struct {
	SchemaVersion int          `json:"schemaVersion"`
	State         HMIState     `json:"state"`
	Audit         []AuditEntry `json:"audit"`
	NextAuditID   uint64       `json:"nextAuditId"`
}

type memoryController struct {
	mu          sync.RWMutex
	state       HMIState
	audit       []AuditEntry
	nextAuditID uint64
	dataFile    string
	now         func() time.Time
}

func newMemoryController(dataFile string, now func() time.Time) (*memoryController, error) {
	if now == nil {
		now = time.Now
	}
	c := &memoryController{
		state:       initialState(now().UTC()),
		nextAuditID: 4,
		dataFile:    dataFile,
		now:         now,
	}
	if dataFile == "" {
		return c, nil
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func initialState(now time.Time) HMIState {
	return HMIState{
		Revision:  1,
		UpdatedAt: now,
		Running:   true,
		Mode:      "auto",
		Target:    30, Output: 30, Cycle: 30, OEE: 92, Inspected: 30, Passed: 30,
		Pending: 30, Blank: 30, Finished: 30, ToolLimit: 100, InspectInterval: 30,
		Bins: []BinState{
			{Type: "full", Label: "满料"},
			{Type: "warning", Label: "需换料"},
			{Type: "fault", Label: "异常"},
		},
		Alarms: []Alarm{
			{ID: 3, Level: "danger", Code: "0003", Text: "库位3定位异常", OccurredAt: now.Add(-3 * time.Minute)},
			{ID: 2, Level: "warning", Code: "0002", Text: "库位2余量不足", OccurredAt: now.Add(-5 * time.Minute)},
			{ID: 1, Level: "info", Code: "0001", Text: "系统自检完成", OccurredAt: now.Add(-10 * time.Minute)},
		},
		History: []HistoryEntry{
			{ID: 3, Level: "danger", Code: "0003", Text: "库位3定位异常", Timestamp: now.Add(-3 * time.Minute)},
			{ID: 2, Level: "warning", Code: "0002", Text: "急停解除", Timestamp: now.Add(-20 * time.Minute)},
			{ID: 1, Level: "info", Code: "0001", Text: "完成抽检", Timestamp: now.Add(-30 * time.Minute)},
		},
	}
}

func (c *memoryController) State(context.Context) (HMIState, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneState(c.state), nil
}

func (c *memoryController) UpdateSettings(_ context.Context, next Parameters, meta MutationMeta, expected *uint64) (HMIState, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := checkRevision(c.state.Revision, expected); err != nil {
		return HMIState{}, "", err
	}

	candidate := cloneState(c.state)
	before := Parameters{Target: candidate.Target, ToolLimit: candidate.ToolLimit, InspectInterval: candidate.InspectInterval}
	candidate.Target = next.Target
	candidate.ToolLimit = next.ToolLimit
	candidate.InspectInterval = next.InspectInterval
	message := "维护参数已保存"
	details := map[string]interface{}{"before": before, "after": next}
	return c.commitLocked(candidate, meta, "settings.update", message, details)
}

func (c *memoryController) ExecuteCommand(_ context.Context, command DeviceCommand, meta MutationMeta, expected *uint64) (HMIState, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := checkRevision(c.state.Revision, expected); err != nil {
		return HMIState{}, "", err
	}

	candidate := cloneState(c.state)
	message := ""
	details := map[string]interface{}{"command": command.Name}
	switch command.Name {
	case "start":
		candidate.Running = true
		message = "设备已启动"
	case "pause":
		candidate.Running = false
		message = "设备已暂停"
	case "reset":
		candidate.Running = true
		candidate.SinglePaused = false
		candidate.FramePaused = false
		candidate.Bins = []BinState{
			{Type: "full", Label: "满料"},
			{Type: "warning", Label: "需换料"},
			{Type: "full", Label: "已复位"},
		}
		for i := range candidate.Alarms {
			if candidate.Alarms[i].Level == "danger" {
				candidate.Alarms[i].Acknowledged = true
			}
		}
		message = "设备复位完成"
	case "clear_bins":
		for i := range candidate.Bins {
			candidate.Bins[i] = BinState{Type: "empty", Label: "料仓已清空"}
		}
		candidate.Blank = 0
		message = "清空料仓命令已执行"
	case "inspect":
		candidate.Inspected++
		candidate.Passed++
		if candidate.Pending > 0 {
			candidate.Pending--
		}
		message = "抽检完成：结果合格"
	case "set_mode":
		if command.Mode != "auto" && command.Mode != "manual" {
			return HMIState{}, "", fmt.Errorf("%w: set_mode requires auto or manual", errUnknownCommand)
		}
		candidate.Mode = command.Mode
		details["mode"] = command.Mode
		message = map[string]string{"auto": "已切换至自动模式", "manual": "已切换至手动模式"}[command.Mode]
	case "set_single_paused":
		if command.Paused == nil {
			return HMIState{}, "", fmt.Errorf("%w: paused is required", errUnknownCommand)
		}
		candidate.SinglePaused = *command.Paused
		details["paused"] = *command.Paused
		message = pauseMessage("单件循环", *command.Paused)
	case "set_frame_paused":
		if command.Paused == nil {
			return HMIState{}, "", fmt.Errorf("%w: paused is required", errUnknownCommand)
		}
		candidate.FramePaused = *command.Paused
		details["paused"] = *command.Paused
		message = pauseMessage("单框循环", *command.Paused)
	default:
		return HMIState{}, "", errUnknownCommand
	}
	return c.commitLocked(candidate, meta, "command."+command.Name, message, details)
}

func pauseMessage(scope string, paused bool) string {
	if paused {
		return scope + "已暂停"
	}
	return scope + "已恢复"
}

func (c *memoryController) AcknowledgeAlarm(_ context.Context, alarmID uint64, meta MutationMeta, expected *uint64) (HMIState, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := checkRevision(c.state.Revision, expected); err != nil {
		return HMIState{}, "", err
	}

	candidate := cloneState(c.state)
	found := false
	for i := range candidate.Alarms {
		if candidate.Alarms[i].ID == alarmID {
			candidate.Alarms[i].Acknowledged = true
			found = true
			break
		}
	}
	if !found {
		return HMIState{}, "", errAlarmNotFound
	}
	message := fmt.Sprintf("报警 %d 已确认", alarmID)
	return c.commitLocked(candidate, meta, "alarm.acknowledge", message, map[string]interface{}{"alarmId": alarmID})
}

func (c *memoryController) Audit(_ context.Context, limit int, beforeID *uint64) (AuditPage, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	items := make([]AuditEntry, 0, limit)
	remaining := false
	for i := len(c.audit) - 1; i >= 0; i-- {
		entry := c.audit[i]
		if beforeID != nil && entry.ID >= *beforeID {
			continue
		}
		if len(items) == limit {
			remaining = true
			break
		}
		items = append(items, cloneAuditEntry(entry))
	}
	page := AuditPage{Items: items}
	if remaining && len(items) > 0 {
		next := items[len(items)-1].ID
		page.NextBeforeID = &next
	}
	return page, nil
}

func (c *memoryController) commitLocked(candidate HMIState, meta MutationMeta, action, message string, details map[string]interface{}) (HMIState, string, error) {
	candidate.Revision = c.state.Revision + 1
	candidate.UpdatedAt = c.now().UTC()
	level, code := historyClassification(action, details)
	candidate.History = append([]HistoryEntry{{
		ID: c.nextAuditID, Level: level, Code: code, Text: message, Timestamp: candidate.UpdatedAt,
	}}, candidate.History...)
	if len(candidate.History) > 100 {
		candidate.History = candidate.History[:100]
	}
	entry := AuditEntry{
		ID: c.nextAuditID, Timestamp: candidate.UpdatedAt, Operator: meta.Operator,
		Action: action, Message: message, Revision: candidate.Revision,
		RequestID: meta.RequestID, Details: details,
	}
	candidateAudit := append(cloneAudit(c.audit), entry)
	if len(candidateAudit) > maxAuditEntries {
		candidateAudit = append([]AuditEntry(nil), candidateAudit[len(candidateAudit)-maxAuditEntries:]...)
	}
	data := persistedController{
		SchemaVersion: persistenceSchemaVersion,
		State:         candidate, Audit: candidateAudit, NextAuditID: c.nextAuditID + 1,
	}
	if err := persistAtomically(c.dataFile, data); err != nil {
		return HMIState{}, "", fmt.Errorf("persist HMI state: %w", err)
	}
	c.state = candidate
	c.audit = candidateAudit
	c.nextAuditID++
	return cloneState(candidate), message, nil
}

func historyClassification(action string, details map[string]interface{}) (string, string) {
	if action == "command.pause" || action == "command.clear_bins" {
		return "warning", "0002"
	}
	if paused, ok := details["paused"].(bool); ok && paused {
		return "warning", "0002"
	}
	return "info", "0001"
}

func checkRevision(current uint64, expected *uint64) error {
	if expected != nil && *expected != current {
		return errRevisionConflict
	}
	return nil
}

func (c *memoryController) load() error {
	contents, err := os.ReadFile(c.dataFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", c.dataFile, err)
	}
	var data persistedController
	if err := json.Unmarshal(contents, &data); err != nil {
		return fmt.Errorf("decode %s: %w", c.dataFile, err)
	}
	if data.SchemaVersion != persistenceSchemaVersion {
		return fmt.Errorf("unsupported persistence schema %d", data.SchemaVersion)
	}
	if data.State.Revision == 0 || data.NextAuditID == 0 {
		return errors.New("persisted HMI data is incomplete")
	}
	c.state = cloneState(data.State)
	c.audit = cloneAudit(data.Audit)
	c.nextAuditID = data.NextAuditID
	return nil
}

func persistAtomically(path string, data persistedController) error {
	if path == "" {
		return nil
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".block-hmi-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	keepTemporary = false
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func cloneState(state HMIState) HMIState {
	state.Bins = append([]BinState(nil), state.Bins...)
	state.Alarms = append([]Alarm(nil), state.Alarms...)
	state.History = append([]HistoryEntry(nil), state.History...)
	return state
}

func cloneAudit(entries []AuditEntry) []AuditEntry {
	result := make([]AuditEntry, len(entries))
	for i := range entries {
		result[i] = cloneAuditEntry(entries[i])
	}
	return result
}

func cloneAuditEntry(entry AuditEntry) AuditEntry {
	if entry.Details != nil {
		original := entry.Details
		entry.Details = make(map[string]interface{}, len(original))
		keys := make([]string, 0, len(original))
		for key := range original {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			entry.Details[key] = original[key]
		}
	}
	return entry
}
