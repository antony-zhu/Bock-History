package mqttv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"time"

	"block.local/block-agent/internal/alarmhistory"
)

const (
	SchemaVersion          = "2.0"
	DefaultPublishInterval = 60 * time.Second
	maximumJSONPayload     = 64 << 10
)

var (
	ErrDisconnected   = errors.New("mqtt v2 is disconnected")
	ErrSenderMissing  = errors.New("mqtt v2 sender is unavailable")
	ErrInboundPolicy  = errors.New("mqtt v2 accepts only non-retained QoS 0 input")
	ErrInvalidRequest = errors.New("invalid mqtt v2 request")
	requestIDPattern  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type Quality string

const (
	QualityGood  Quality = "good"
	QualityStale Quality = "stale"
	QualityError Quality = "error"
)

type PointValue struct {
	Value       any       `json:"value"`
	Quality     Quality   `json:"quality"`
	UpdatedAt   time.Time `json:"updatedAt"`
	AlarmActive *bool     `json:"alarmActive"`
}

type Snapshot struct {
	Values  map[string]PointValue
	StateAt time.Time
}

type Source struct {
	SiteID   string `json:"siteId"`
	BlockID  string `json:"blockId"`
	DeviceID string `json:"deviceId"`
}

type Topics struct {
	StateLatest       string
	StateGet          string
	AlarmNotify       string
	AlarmHistoryQuery string
	AlarmHistoryPage  string
}

func NewTopics(siteID, blockID string) Topics {
	base := fmt.Sprintf("bdm/v2/sites/%s/blocks/%s", siteID, blockID)
	return Topics{
		StateLatest:       base + "/up/state/latest",
		StateGet:          base + "/down/state/get",
		AlarmNotify:       base + "/up/alarm/notify",
		AlarmHistoryQuery: base + "/down/alarm-history/query",
		AlarmHistoryPage:  base + "/up/alarm-history/page",
	}
}

type Publication struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}

type Inbound Publication

type Sender interface {
	Publish(context.Context, Publication) error
}

type HistoryReader interface {
	List(context.Context, alarmhistory.Query) (alarmhistory.Page, error)
}

type StateCurrent struct {
	Type          string                `json:"type"`
	SchemaVersion string                `json:"schemaVersion"`
	Source        Source                `json:"source"`
	RequestID     string                `json:"requestId,omitempty"`
	StateAt       time.Time             `json:"stateAt"`
	SentAt        time.Time             `json:"sentAt"`
	State         map[string]PointValue `json:"state"`
}

type StateGet struct {
	Type          string `json:"type"`
	SchemaVersion string `json:"schemaVersion"`
	RequestID     string `json:"requestId"`
	Target        Source `json:"target"`
}

type AlarmNotification struct {
	Type          string              `json:"type"`
	SchemaVersion string              `json:"schemaVersion"`
	Source        Source              `json:"source"`
	SentAt        time.Time           `json:"sentAt"`
	Alarm         alarmhistory.Record `json:"alarm"`
}

type AlarmHistoryQuery struct {
	Type           string  `json:"type"`
	SchemaVersion  string  `json:"schemaVersion"`
	RequestID      string  `json:"requestId"`
	Target         Source  `json:"target"`
	FromOccurredAt string  `json:"fromOccurredAt"`
	ToOccurredAt   string  `json:"toOccurredAt"`
	AfterCursor    *uint64 `json:"afterCursor,omitempty"`
	Limit          int     `json:"limit"`
}

type AlarmHistoryPage struct {
	Type          string                `json:"type"`
	SchemaVersion string                `json:"schemaVersion"`
	RequestID     string                `json:"requestId"`
	Source        Source                `json:"source"`
	Records       []alarmhistory.Record `json:"records"`
	NextCursor    *uint64               `json:"nextCursor,omitempty"`
	HasMore       bool                  `json:"hasMore"`
}

func decodeStrict(payload []byte, target any) error {
	if len(payload) == 0 || len(payload) > maximumJSONPayload {
		return ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("mqtt v2 payload contains multiple JSON values")
		}
		return err
	}
	return nil
}

func validRequestID(value string) bool {
	return requestIDPattern.MatchString(value)
}

func validSource(value Source) bool {
	return value.SiteID != "" && value.BlockID != "" && value.DeviceID != ""
}

func goodPoints(snapshot Snapshot) (map[string]PointValue, time.Time) {
	points := make(map[string]PointValue)
	stateAt := snapshot.StateAt
	for pointID, point := range snapshot.Values {
		if point.Quality != QualityGood {
			continue
		}
		points[pointID] = point
		if point.UpdatedAt.After(stateAt) {
			stateAt = point.UpdatedAt
		}
	}
	return points, stateAt
}

func stateFingerprint(points map[string]PointValue) string {
	keys := make([]string, 0, len(points))
	for pointID := range points {
		keys = append(keys, pointID)
	}
	sort.Strings(keys)
	type fingerprintValue struct {
		Value       any
		Quality     Quality
		AlarmActive *bool
	}
	fingerprint := make(map[string]fingerprintValue, len(keys))
	for _, pointID := range keys {
		point := points[pointID]
		fingerprint[pointID] = fingerprintValue{
			Value: point.Value, Quality: point.Quality, AlarmActive: point.AlarmActive,
		}
	}
	payload, _ := json.Marshal(fingerprint)
	return string(payload)
}
