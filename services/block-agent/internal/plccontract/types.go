package plccontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

const SchemaVersion = "block-plc-private/v1"

type Quality string

const (
	QualityGood      Quality = "GOOD"
	QualityUncertain Quality = "UNCERTAIN"
	QualityBad       Quality = "BAD"
)

type Bin struct {
	ID       string `json:"id"`
	Quantity int    `json:"quantity"`
	Capacity int    `json:"capacity"`
	Status   string `json:"status"`
}

type Alarm struct {
	AlarmID      string     `json:"alarmId"`
	Code         string     `json:"code"`
	Level        string     `json:"level"`
	Text         string     `json:"text"`
	Active       bool       `json:"active"`
	OccurredAt   time.Time  `json:"occurredAt"`
	ClearedAt    *time.Time `json:"clearedAt,omitempty"`
	Acknowledged bool       `json:"acknowledged"`
}

type Points struct {
	ControlRevision uint64  `json:"controlRevision"`
	Running         bool    `json:"running"`
	Mode            string  `json:"mode"`
	SinglePaused    bool    `json:"singlePaused"`
	FramePaused     bool    `json:"framePaused"`
	SafetyReady     bool    `json:"safetyReady"`
	EmergencyStop   bool    `json:"emergencyStop"`
	GuardDoorClosed bool    `json:"guardDoorClosed"`
	PLCConnected    bool    `json:"plcConnected"`
	Target          int     `json:"target"`
	Output          int     `json:"output"`
	CycleSeconds    int     `json:"cycleSeconds"`
	Inspected       int     `json:"inspected"`
	Passed          int     `json:"passed"`
	NG              int     `json:"ng"`
	Pending         int     `json:"pending"`
	Blank           int     `json:"blank"`
	Finished        int     `json:"finished"`
	ToolUsed        int     `json:"toolUsed"`
	ToolLimit       int     `json:"toolLimit"`
	InspectInterval int     `json:"inspectInterval"`
	Bins            []Bin   `json:"bins"`
	Alarms          []Alarm `json:"alarms"`
}

type Snapshot struct {
	SchemaVersion      string    `json:"schemaVersion"`
	SimulatorSessionID string    `json:"simulatorSessionId"`
	SampleSequence     uint64    `json:"sampleSequence"`
	GeneratedAt        time.Time `json:"generatedAt"`
	Quality            Quality   `json:"quality"`
	Points             Points    `json:"points"`
}

type Settings struct {
	Target          int `json:"target"`
	ToolLimit       int `json:"toolLimit"`
	InspectInterval int `json:"inspectInterval"`
}

type Command struct {
	CommandID               string    `json:"commandId"`
	Name                    string    `json:"name"`
	ExpectedControlRevision *uint64   `json:"expectedControlRevision,omitempty"`
	Mode                    string    `json:"mode,omitempty"`
	Paused                  *bool     `json:"paused,omitempty"`
	Settings                *Settings `json:"settings,omitempty"`
	AlarmID                 string    `json:"alarmId,omitempty"`
}

type CommandStatus string

const (
	CommandExecuted CommandStatus = "EXECUTED"
	CommandRejected CommandStatus = "REJECTED"
	CommandFailed   CommandStatus = "FAILED"
	CommandUnknown  CommandStatus = "UNKNOWN"
	CommandPending  CommandStatus = "PENDING"
)

const (
	ResultCodeRevisionConflict    = "revision_conflict"
	ResultCodeIdempotencyConflict = "idempotency_conflict"
	ResultCodeSafetyInterlock     = "safety_interlock"
	ResultCodeAlarmNotFound       = "alarm_not_found"
	ResultCodeDeviceUnavailable   = "device_unavailable"
	ResultCodeCommandRejected     = "command_rejected"
	ResultCodeCommandFailed       = "command_failed"
	ResultCodeOutcomeUnknown      = "command_outcome_unknown"
	ResultCodeReadbackFailed      = "readback_failed"
)

type CommandResult struct {
	CommandID       string        `json:"commandId"`
	Name            string        `json:"name"`
	Status          CommandStatus `json:"status"`
	Code            string        `json:"code,omitempty"`
	Reason          string        `json:"reason,omitempty"`
	SimulationOnly  bool          `json:"simulationOnly,omitempty"`
	ControlRevision uint64        `json:"controlRevision"`
	Snapshot        *Snapshot     `json:"snapshot,omitempty"`
}

// NormalizeCommand defines the private idempotency identity shared by Agent
// storage and the simulator. CommandID is deliberately excluded from the
// fingerprint; every other request semantic, including an expected revision,
// participates in it.
func NormalizeCommand(command Command) Command {
	command.CommandID = strings.TrimSpace(command.CommandID)
	command.Name = strings.TrimSpace(command.Name)
	command.Mode = strings.TrimSpace(command.Mode)
	command.AlarmID = strings.TrimSpace(command.AlarmID)
	return command
}

func CommandFingerprint(command Command) string {
	command = NormalizeCommand(command)
	canonical := struct {
		Name                    string    `json:"name"`
		ExpectedControlRevision *uint64   `json:"expectedControlRevision"`
		Mode                    string    `json:"mode"`
		Paused                  *bool     `json:"paused"`
		Settings                *Settings `json:"settings"`
		AlarmID                 string    `json:"alarmId"`
	}{
		Name: command.Name, ExpectedControlRevision: command.ExpectedControlRevision,
		Mode: command.Mode, Paused: command.Paused, Settings: command.Settings,
		AlarmID: command.AlarmID,
	}
	contents, _ := json.Marshal(canonical)
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

type Faults struct {
	PLCDisconnected        bool    `json:"plcDisconnected"`
	ResponseDelayMillis    int     `json:"responseDelayMillis"`
	FreezeData             bool    `json:"freezeData"`
	ForcedQuality          Quality `json:"forcedQuality,omitempty"`
	EmergencyStop          bool    `json:"emergencyStop"`
	GuardDoorOpen          bool    `json:"guardDoorOpen"`
	SevereAlarm            bool    `json:"severeAlarm"`
	RejectCommands         bool    `json:"rejectCommands"`
	FailCommands           bool    `json:"failCommands"`
	AppliedResponseTimeout bool    `json:"appliedResponseTimeout"`
}

type FaultRequest struct {
	Fault       string  `json:"fault"`
	Enabled     *bool   `json:"enabled,omitempty"`
	DelayMillis *int    `json:"delayMillis,omitempty"`
	Quality     Quality `json:"quality,omitempty"`
}

type ErrorEnvelope struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
