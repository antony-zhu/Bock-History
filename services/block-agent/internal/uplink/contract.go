package uplink

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/state"
)

const (
	SchemaVersion     = "1.0"
	MaxSafeSequence   = uint64(9007199254740991)
	MaxReplayMessages = 100
	MaxReplayBytes    = 240 * 1024
)

type Source struct {
	SiteID   string  `json:"siteId"`
	BlockID  string  `json:"blockId"`
	DeviceID string  `json:"deviceId"`
	LineID   *string `json:"lineId"`
}

type ReliableMessage struct {
	Type          string `json:"type"`
	SchemaVersion string `json:"schemaVersion"`
	MessageID     string `json:"messageId"`
	Source        Source `json:"source"`
	OccurredAt    string `json:"occurredAt"`
	SentAt        string `json:"sentAt"`
	BootID        string `json:"bootId"`
	StreamEpoch   string `json:"streamEpoch"`
	Sequence      string `json:"sequence"`
	CorrelationID any    `json:"correlationId"`
	Quality       string `json:"quality"`
	Replayed      bool   `json:"replayed"`
	Payload       any    `json:"payload"`
}

type ControlMessage struct {
	Type          string `json:"type"`
	SchemaVersion string `json:"schemaVersion"`
	MessageID     string `json:"messageId"`
	Source        Source `json:"source"`
	OccurredAt    string `json:"occurredAt"`
	SentAt        string `json:"sentAt"`
	BootID        string `json:"bootId"`
	Quality       string `json:"quality"`
	Payload       any    `json:"payload"`
}

type SnapshotPayload struct {
	Connectivity   string       `json:"connectivity"`
	OperatingState string       `json:"operatingState"`
	OperatingMode  string       `json:"operatingMode"`
	DisplayStatus  string       `json:"displayStatus"`
	Revision       string       `json:"revision"`
	Production     Production   `json:"production"`
	Cycle          Cycle        `json:"cycle"`
	AlarmSummary   AlarmSummary `json:"alarmSummary"`
	DataFreshness  uint64       `json:"dataFreshnessMs"`
}

type Production struct {
	Target         uint64 `json:"target"`
	Total          uint64 `json:"total"`
	Good           uint64 `json:"good"`
	Reject         uint64 `json:"reject"`
	BlankRemaining uint64 `json:"blankRemaining"`
	FinishedCount  uint64 `json:"finishedCount"`
}

type Cycle struct {
	CycleTimeSec uint64 `json:"cycleTimeSec"`
}

type AlarmSummary struct {
	ActiveCount     uint64  `json:"activeCount"`
	HighestSeverity *string `json:"highestSeverity"`
}

type StateChangedPayload struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

type CountChangedPayload struct {
	Good   uint64 `json:"good"`
	Reject uint64 `json:"reject"`
	Total  uint64 `json:"total"`
}

type AlarmRaisedPayload struct {
	AlarmID  string `json:"alarmId"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Text     string `json:"text"`
}

type AlarmClearedPayload struct {
	AlarmID string `json:"alarmId"`
	Code    string `json:"code"`
}

type Range struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type GapPayload struct {
	StreamEpoch string  `json:"streamEpoch"`
	Ranges      []Range `json:"ranges"`
	Reason      string  `json:"reason"`
}

type Sync struct {
	SchemaVersion             string     `json:"schemaVersion"`
	SyncID                    string     `json:"syncId"`
	SyncRevision              string     `json:"syncRevision"`
	Target                    SyncTarget `json:"target"`
	IssuedAt                  string     `json:"issuedAt"`
	Action                    string     `json:"action"`
	StreamEpoch               string     `json:"streamEpoch"`
	HighestContiguousSequence string     `json:"highestContiguousSequence"`
	Ranges                    []Range    `json:"ranges"`
	AcceptedGapRanges         []Range    `json:"acceptedGapRanges"`
}

type SyncTarget struct {
	SiteID   string `json:"siteId"`
	BlockID  string `json:"blockId"`
	DeviceID string `json:"deviceId"`
}

type ReplayBatch struct {
	SchemaVersion string       `json:"schemaVersion"`
	BatchID       string       `json:"batchId"`
	Source        Source       `json:"source"`
	BootID        string       `json:"bootId"`
	SentAt        string       `json:"sentAt"`
	StreamEpoch   string       `json:"streamEpoch"`
	Messages      []ReplayItem `json:"messages"`
}

type ReplayItem struct {
	Channel string          `json:"channel"`
	Message json.RawMessage `json:"message"`
}

type Presence struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	BootID string `json:"bootId"`
}

type HelloPayload struct {
	SoftwareVersion           string   `json:"softwareVersion"`
	OSVersion                 string   `json:"osVersion"`
	Architecture              string   `json:"architecture"`
	HardwareModel             string   `json:"hardwareModel"`
	Capabilities              []string `json:"capabilities"`
	SupportedProtocolVersions []string `json:"supportedProtocolVersions"`
	DesiredConfigRevision     string   `json:"desiredConfigRevision"`
	ReportedConfigRevision    string   `json:"reportedConfigRevision"`
	StreamEpoch               string   `json:"streamEpoch"`
	StreamGeneration          string   `json:"streamGeneration"`
	StreamEpochStartedAt      string   `json:"streamEpochStartedAt"`
	FirstAvailableSequence    string   `json:"firstAvailableSequence"`
	LastProducedSequence      string   `json:"lastProducedSequence"`
	LastAckedSequence         string   `json:"lastAckedSequence"`
}

type HeartbeatPayload struct {
	UptimeSec            uint64             `json:"uptimeSec"`
	CPUPercent           float64            `json:"cpuPercent"`
	MemoryPercent        float64            `json:"memoryPercent"`
	DiskFreeBytes        uint64             `json:"diskFreeBytes"`
	ClockOffsetMs        int64              `json:"clockOffsetMs"`
	DeviceConnections    []DeviceConnection `json:"deviceConnections"`
	OutboxPending        uint64             `json:"outboxPending"`
	LastProducedSequence string             `json:"lastProducedSequence"`
	LastAckedSequence    string             `json:"lastAckedSequence"`
}

type DeviceConnection struct {
	DeviceID     string  `json:"deviceId"`
	Connected    bool    `json:"connected"`
	LastSampleAt *string `json:"lastSampleAt"`
}

type SyncStatusPayload struct {
	StreamEpoch            string `json:"streamEpoch"`
	FirstAvailableSequence string `json:"firstAvailableSequence"`
	LastProducedSequence   string `json:"lastProducedSequence"`
	LastAckedSequence      string `json:"lastAckedSequence"`
}

type Draft struct {
	Type           string
	Channel        string
	OccurredAt     time.Time
	Quality        string
	Payload        any
	RetentionClass string
	Calibration    bool
}

type SnapshotView struct {
	State state.Model
	Meta  state.SourceMeta
	Stale bool
}

func NewUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(value[:])
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32], nil
}

func FormatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func FormatSequence(value uint64) string {
	return strconv.FormatUint(value, 10)
}

func ParseSequence(name, value string, positive bool) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed > MaxSafeSequence || (positive && parsed == 0) ||
		strconv.FormatUint(parsed, 10) != value {
		qualifier := "non-negative"
		if positive {
			qualifier = "positive"
		}
		return 0, fmt.Errorf("%s must be a canonical %s safe sequence", name, qualifier)
	}
	return parsed, nil
}

func NewReliable(source Source, bootID, streamEpoch string, sequence uint64, draft Draft, sentAt time.Time) (ReliableMessage, error) {
	messageID, err := NewUUID()
	if err != nil {
		return ReliableMessage{}, err
	}
	if sequence == 0 || sequence > MaxSafeSequence {
		return ReliableMessage{}, errors.New("reliable sequence is outside the contract range")
	}
	return ReliableMessage{
		Type: draft.Type, SchemaVersion: SchemaVersion, MessageID: messageID,
		Source: source, OccurredAt: FormatTime(draft.OccurredAt), SentAt: FormatTime(sentAt),
		BootID: bootID, StreamEpoch: streamEpoch, Sequence: FormatSequence(sequence),
		CorrelationID: nil, Quality: draft.Quality, Replayed: false, Payload: draft.Payload,
	}, nil
}

func NewControl(messageType string, source Source, bootID, quality string, payload any, now time.Time) (ControlMessage, error) {
	messageID, err := NewUUID()
	if err != nil {
		return ControlMessage{}, err
	}
	return ControlMessage{
		Type: messageType, SchemaVersion: SchemaVersion, MessageID: messageID,
		Source: source, OccurredAt: FormatTime(now), SentAt: FormatTime(now),
		BootID: bootID, Quality: quality, Payload: payload,
	}, nil
}

func PresenceJSON(status, reason, bootID string) ([]byte, error) {
	return json.Marshal(Presence{Status: status, Reason: reason, BootID: bootID})
}

func BuildSnapshot(view SnapshotView, now time.Time) SnapshotPayload {
	model := view.State
	highest := highestSeverity(model.Alarms)
	freshness := uint64(0)
	sourceAt := model.UpdatedAt
	if now.After(sourceAt) {
		freshness = safeUint64(now.Sub(sourceAt).Milliseconds())
	}
	return SnapshotPayload{
		Connectivity:   connectivity(view),
		OperatingState: operatingState(model, view),
		OperatingMode:  operatingMode(model.Mode),
		DisplayStatus:  displayStatus(model, view),
		Revision:       FormatSequence(minSafe(model.Revision)),
		Production: Production{
			Target: nonNegative(model.Target), Total: nonNegative(model.Output),
			Good: nonNegative(model.Passed), Reject: nonNegative(model.NG),
			BlankRemaining: nonNegative(model.Blank), FinishedCount: nonNegative(model.Finished),
		},
		Cycle:         Cycle{CycleTimeSec: nonNegative(model.Cycle)},
		AlarmSummary:  AlarmSummary{ActiveCount: uint64(len(model.Alarms)), HighestSeverity: highest},
		DataFreshness: freshness,
	}
}

func BuildDrafts(previous *SnapshotView, current SnapshotView, now time.Time, calibration bool) []Draft {
	currentPayload := BuildSnapshot(current, now)
	qualityValue := quality(current)
	if previous == nil {
		drafts := make([]Draft, 0, len(current.State.Alarms)+1)
		for _, alarm := range sortedAlarms(current.State.Alarms) {
			drafts = append(drafts, raisedAlarmDraft(alarm, current.State.UpdatedAt, qualityValue))
		}
		return append(drafts, Draft{
			Type: "device.snapshot", Channel: "snapshot", OccurredAt: current.State.UpdatedAt,
			Quality: qualityValue, Payload: currentPayload, RetentionClass: "snapshot",
		})
	}
	drafts := make([]Draft, 0, 5)
	previousPayload := BuildSnapshot(*previous, now)
	fromState, toState := previousPayload.OperatingState, currentPayload.OperatingState
	if fromState != toState {
		drafts = append(drafts, Draft{
			Type: "machine.state.changed", Channel: "event", OccurredAt: current.State.UpdatedAt,
			Quality: qualityValue, RetentionClass: "production",
			Payload: StateChangedPayload{From: fromState, To: toState, Reason: "device_sample"},
		})
	}
	if previousPayload.Production.Total != currentPayload.Production.Total ||
		previousPayload.Production.Good != currentPayload.Production.Good ||
		previousPayload.Production.Reject != currentPayload.Production.Reject {
		drafts = append(drafts, Draft{
			Type: "production.count.changed", Channel: "event", OccurredAt: current.State.UpdatedAt,
			Quality: qualityValue, RetentionClass: "production",
			Payload: CountChangedPayload{
				Good: currentPayload.Production.Good, Reject: currentPayload.Production.Reject,
				Total: currentPayload.Production.Total,
			},
		})
	}
	previousAlarms := make(map[uint64]state.Alarm, len(previous.State.Alarms))
	for _, alarm := range previous.State.Alarms {
		previousAlarms[alarm.ID] = alarm
	}
	currentAlarms := make(map[uint64]state.Alarm, len(current.State.Alarms))
	for _, alarm := range sortedAlarms(current.State.Alarms) {
		currentAlarms[alarm.ID] = alarm
		if _, existed := previousAlarms[alarm.ID]; !existed {
			drafts = append(drafts, raisedAlarmDraft(alarm, current.State.UpdatedAt, qualityValue))
		}
	}
	var clearedIDs []uint64
	for id := range previousAlarms {
		if _, active := currentAlarms[id]; !active {
			clearedIDs = append(clearedIDs, id)
		}
	}
	sort.Slice(clearedIDs, func(left, right int) bool { return clearedIDs[left] < clearedIDs[right] })
	for _, id := range clearedIDs {
		alarm := previousAlarms[id]
		drafts = append(drafts, Draft{
			Type: "alarm.cleared", Channel: "alarm", OccurredAt: current.State.UpdatedAt,
			Quality: qualityValue, RetentionClass: "alarm",
			Payload: AlarmClearedPayload{
				AlarmID: strconv.FormatUint(id, 10), Code: bounded(alarm.Code, 128),
			},
		})
	}
	if calibration || quality(*previous) != qualityValue || snapshotChanged(previousPayload, currentPayload) {
		drafts = append(drafts, Draft{
			Type: "device.snapshot", Channel: "snapshot", OccurredAt: current.State.UpdatedAt,
			Quality: qualityValue, Payload: currentPayload, RetentionClass: "snapshot", Calibration: calibration,
		})
	}
	return drafts
}

func sortedAlarms(alarms []state.Alarm) []state.Alarm {
	result := append([]state.Alarm{}, alarms...)
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].ID != result[right].ID {
			return result[left].ID < result[right].ID
		}
		return result[left].Code < result[right].Code
	})
	return result
}

func raisedAlarmDraft(alarm state.Alarm, fallback time.Time, qualityValue string) Draft {
	occurredAt := alarm.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = fallback
	}
	return Draft{
		Type: "alarm.raised", Channel: "alarm", OccurredAt: occurredAt,
		Quality: qualityValue, RetentionClass: "alarm",
		Payload: AlarmRaisedPayload{
			AlarmID: strconv.FormatUint(alarm.ID, 10), Code: bounded(alarm.Code, 128),
			Severity: severity(alarm.Level), Text: bounded(alarm.Text, 1024),
		},
	}
}

func snapshotChanged(previous, current SnapshotPayload) bool {
	previous.DataFreshness = 0
	current.DataFreshness = 0
	left, _ := json.Marshal(previous)
	right, _ := json.Marshal(current)
	return string(left) != string(right)
}

func SnapshotQuality(view SnapshotView) string {
	return quality(view)
}

func quality(view SnapshotView) string {
	if view.Stale {
		return "STALE"
	}
	switch view.Meta.Quality {
	case plccontract.QualityGood:
		return "GOOD"
	case plccontract.QualityUncertain:
		return "UNCERTAIN"
	default:
		return "BAD"
	}
}

func connectivity(view SnapshotView) string {
	if !view.Meta.PLCConnected {
		return "OFFLINE"
	}
	if view.Stale || view.Meta.Quality != plccontract.QualityGood {
		return "DEGRADED"
	}
	return "ONLINE"
}

func operatingState(model state.Model, view SnapshotView) string {
	if !view.Meta.PLCConnected {
		return "UNKNOWN"
	}
	if len(model.Alarms) > 0 {
		return "FAULT"
	}
	if model.Running {
		return "RUNNING"
	}
	return "STOPPED"
}

func operatingMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto":
		return "AUTO"
	case "manual", "single", "frame":
		return "MANUAL"
	case "mdi":
		return "MDI"
	case "setup":
		return "SETUP"
	default:
		return "UNKNOWN"
	}
}

func displayStatus(model state.Model, view SnapshotView) string {
	if !view.Meta.PLCConnected {
		return "OFFLINE"
	}
	if len(model.Alarms) > 0 {
		return "ALARM"
	}
	if model.Running {
		return "RUNNING"
	}
	return "STOPPED"
}

func highestSeverity(alarms []state.Alarm) *string {
	best := ""
	rank := 0
	for _, alarm := range alarms {
		candidate := severity(alarm.Level)
		candidateRank := map[string]int{"INFO": 1, "WARNING": 2, "CRITICAL": 3}[candidate]
		if candidateRank > rank {
			best, rank = candidate, candidateRank
		}
	}
	if best == "" {
		return nil
	}
	return &best
}

func severity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical", "severe", "fatal", "error":
		return "CRITICAL"
	case "warning", "warn":
		return "WARNING"
	default:
		return "INFO"
	}
}

func bounded(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "unknown"
	}
	runes := []rune(value)
	if len(runes) > maximum {
		value = string(runes[:maximum])
	}
	return value
}

func nonNegative(value int) uint64 {
	if value <= 0 {
		return 0
	}
	return minSafe(uint64(value))
}

func minSafe(value uint64) uint64 {
	if value > MaxSafeSequence {
		return MaxSafeSequence
	}
	return value
}

func safeUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return minSafe(uint64(value))
}
