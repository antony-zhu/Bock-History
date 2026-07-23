package state

import (
	"fmt"
	"strconv"
	"time"

	"block.local/block-agent/internal/plccontract"
)

type Bin struct {
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

// Model intentionally matches the existing HMI v1 response. Simulator and
// freshness metadata stay private to the Agent until Common freezes a Local API.
type Model struct {
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
	Bins            []Bin          `json:"bins"`
	Alarms          []Alarm        `json:"alarms"`
	History         []HistoryEntry `json:"history"`
}

type SourceMeta struct {
	SimulatorSessionID string
	SampleSequence     uint64
	Quality            plccontract.Quality
	PLCConnected       bool
	ReceivedAt         time.Time
}

func FromPLC(snapshot plccontract.Snapshot, previous Model) (Model, SourceMeta) {
	points := snapshot.Points
	next := Model{
		Revision:        points.ControlRevision,
		UpdatedAt:       snapshot.GeneratedAt.UTC(),
		Running:         points.Running,
		Mode:            points.Mode,
		SinglePaused:    points.SinglePaused,
		FramePaused:     points.FramePaused,
		Target:          points.Target,
		Output:          points.Output,
		Cycle:           points.CycleSeconds,
		Inspected:       points.Inspected,
		Passed:          points.Passed,
		NG:              points.NG,
		Pending:         points.Pending,
		Blank:           points.Blank,
		Finished:        points.Finished,
		ToolLimit:       points.ToolLimit,
		InspectInterval: points.InspectInterval,
		Bins:            make([]Bin, 0, len(points.Bins)),
		Alarms:          make([]Alarm, 0, len(points.Alarms)),
		History:         append([]HistoryEntry{}, previous.History...),
	}
	if points.Target > 0 {
		next.OEE = min(100, points.Output*100/points.Target)
	}
	for _, bin := range points.Bins {
		next.Bins = append(next.Bins, Bin{
			Type:  normalizeBinStatus(bin.Status),
			Label: fmt.Sprintf("%s · %d/%d", bin.ID, bin.Quantity, bin.Capacity),
		})
	}
	previousAlarms := make(map[uint64]Alarm, len(previous.Alarms))
	for _, item := range previous.Alarms {
		previousAlarms[item.ID] = item
	}
	active := make(map[uint64]bool)
	for _, raw := range points.Alarms {
		if !raw.Active {
			continue
		}
		id := stableAlarmID(raw.AlarmID)
		active[id] = true
		next.Alarms = append(next.Alarms, Alarm{
			ID: id, Level: raw.Level, Code: raw.Code, Text: raw.Text,
			OccurredAt: raw.OccurredAt.UTC(), Acknowledged: raw.Acknowledged,
		})
		if _, existed := previousAlarms[id]; !existed {
			next.prependHistory(HistoryEntry{ID: historyID(snapshot.SampleSequence, id), Level: raw.Level, Code: raw.Code, Text: raw.Text, Timestamp: raw.OccurredAt.UTC()})
		}
	}
	for id, old := range previousAlarms {
		if !active[id] {
			next.prependHistory(HistoryEntry{ID: historyID(snapshot.SampleSequence, id), Level: "info", Code: old.Code, Text: old.Text + " 已清除", Timestamp: snapshot.GeneratedAt.UTC()})
		}
	}
	return next, SourceMeta{
		SimulatorSessionID: snapshot.SimulatorSessionID,
		SampleSequence:     snapshot.SampleSequence,
		Quality:            snapshot.Quality,
		PLCConnected:       points.PLCConnected,
		ReceivedAt:         time.Now().UTC(),
	}
}

func (m *Model) AddOperation(level, code, text string, at time.Time) {
	m.prependHistory(HistoryEntry{ID: uint64(at.UnixNano()), Level: level, Code: code, Text: text, Timestamp: at.UTC()})
}

func (m *Model) prependHistory(entry HistoryEntry) {
	m.History = append([]HistoryEntry{entry}, m.History...)
	if len(m.History) > 100 {
		m.History = m.History[:100]
	}
}

func normalizeBinStatus(value string) string {
	switch value {
	case "empty", "normal", "warning", "full", "fault":
		return value
	default:
		return "fault"
	}
}

func stableAlarmID(value string) uint64 {
	if parsed, err := strconv.ParseUint(value, 10, 64); err == nil && parsed > 0 {
		return parsed
	}
	var hash uint64 = 1469598103934665603
	for index := 0; index < len(value); index++ {
		hash ^= uint64(value[index])
		hash *= 1099511628211
	}
	if hash == 0 {
		return 1
	}
	return hash
}

func historyID(sequence, alarmID uint64) uint64 {
	return (sequence << 20) ^ alarmID
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
