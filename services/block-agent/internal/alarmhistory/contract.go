package alarmhistory

import (
	"context"
	"errors"
	"time"
)

const MaxPageSize = 200

var (
	ErrStoreUnavailable = errors.New("alarm history store is unavailable")
	ErrInvalidQuery     = errors.New("invalid alarm history query")
	ErrInvalidPage      = errors.New("invalid alarm history page")
)

type Record struct {
	AlarmRecordID string         `json:"alarmRecordId"`
	HistoryCursor uint64         `json:"historyCursor"`
	AlarmID       string         `json:"alarmId"`
	EventKind     string         `json:"eventKind"`
	Code          string         `json:"code"`
	Severity      string         `json:"severity"`
	Text          string         `json:"text"`
	OccurredAt    time.Time      `json:"occurredAt"`
	Details       map[string]any `json:"details,omitempty"`
}

type Query struct {
	FromOccurredAt time.Time
	ToOccurredAt   time.Time
	AfterCursor    uint64
	Limit          int
}

func (q Query) Validate() error {
	if q.FromOccurredAt.IsZero() || q.ToOccurredAt.IsZero() ||
		!q.ToOccurredAt.After(q.FromOccurredAt) ||
		q.Limit < 1 || q.Limit > MaxPageSize {
		return ErrInvalidQuery
	}
	return nil
}

type Page struct {
	Records    []Record `json:"records"`
	NextCursor *uint64  `json:"nextCursor,omitempty"`
	HasMore    bool     `json:"hasMore"`
}

func (p Page) Validate() error {
	if p.HasMore && (len(p.Records) == 0 || p.NextCursor == nil || *p.NextCursor == 0) {
		return ErrInvalidPage
	}
	if p.NextCursor != nil && *p.NextCursor == 0 {
		return ErrInvalidPage
	}
	return nil
}

type Store interface {
	Append(context.Context, Record) (Record, error)
	List(context.Context, Query) ([]Record, bool, error)
}

type Notifier interface {
	Notify(context.Context, Record) error
}
