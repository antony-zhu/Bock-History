package alarmhistory

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	appendFn func(context.Context, Record) (Record, error)
	listFn   func(context.Context, Query) ([]Record, bool, error)
}

func (s fakeStore) Append(ctx context.Context, record Record) (Record, error) {
	return s.appendFn(ctx, record)
}

func (s fakeStore) List(ctx context.Context, query Query) ([]Record, bool, error) {
	return s.listFn(ctx, query)
}

type fakeNotifier struct {
	notifyFn func(context.Context, Record) error
}

func (n fakeNotifier) Notify(ctx context.Context, record Record) error {
	return n.notifyFn(ctx, record)
}

func TestAppendStoresBeforeBestEffortNotify(t *testing.T) {
	var calls []string
	service := New(fakeStore{
		appendFn: func(_ context.Context, record Record) (Record, error) {
			calls = append(calls, "store")
			record.HistoryCursor = 7
			return record, nil
		},
		listFn: func(context.Context, Query) ([]Record, bool, error) { return nil, false, nil },
	}, fakeNotifier{notifyFn: func(_ context.Context, record Record) error {
		calls = append(calls, "notify")
		if record.HistoryCursor != 7 {
			t.Fatal("notification received a record before persistence")
		}
		return errors.New("broker offline")
	}})

	stored, err := service.Append(context.Background(), Record{AlarmID: "alarm.emergencyStop"})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if stored.HistoryCursor != 7 || len(calls) != 2 || calls[0] != "store" || calls[1] != "notify" {
		t.Fatalf("Append() calls = %#v, record = %#v", calls, stored)
	}
}

func TestAppendDoesNotNotifyWhenStoreFails(t *testing.T) {
	notified := false
	service := New(fakeStore{
		appendFn: func(context.Context, Record) (Record, error) { return Record{}, errors.New("disk full") },
		listFn:   func(context.Context, Query) ([]Record, bool, error) { return nil, false, nil },
	}, fakeNotifier{notifyFn: func(context.Context, Record) error {
		notified = true
		return nil
	}})

	if _, err := service.Append(context.Background(), Record{}); err == nil {
		t.Fatal("Append() error = nil, want store error")
	}
	if notified {
		t.Fatal("notifier called after store failure")
	}
}

func TestListBuildsCursorOnlyWhenMore(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	query := Query{FromOccurredAt: now, ToOccurredAt: now.Add(time.Hour), Limit: 20}
	service := New(fakeStore{
		appendFn: func(context.Context, Record) (Record, error) { return Record{}, nil },
		listFn: func(context.Context, Query) ([]Record, bool, error) {
			return []Record{{HistoryCursor: 3}, {HistoryCursor: 4}}, true, nil
		},
	}, nil)

	page, err := service.List(context.Background(), query)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !page.HasMore || page.NextCursor == nil || *page.NextCursor != 4 {
		t.Fatalf("List() page = %#v", page)
	}
}

func TestListAllowsFinalPageWithoutCursor(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	service := New(fakeStore{
		appendFn: func(context.Context, Record) (Record, error) { return Record{}, nil },
		listFn: func(context.Context, Query) ([]Record, bool, error) {
			return []Record{{HistoryCursor: 4}}, false, nil
		},
	}, nil)

	page, err := service.List(context.Background(), Query{
		FromOccurredAt: now, ToOccurredAt: now.Add(time.Hour), Limit: 1,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if page.HasMore || page.NextCursor != nil {
		t.Fatalf("final page = %#v, want no cursor", page)
	}
}

func TestListRejectsMoreWithoutRecord(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	service := New(fakeStore{
		appendFn: func(context.Context, Record) (Record, error) { return Record{}, nil },
		listFn:   func(context.Context, Query) ([]Record, bool, error) { return nil, true, nil },
	}, nil)

	if _, err := service.List(context.Background(), Query{
		FromOccurredAt: now, ToOccurredAt: now.Add(time.Hour), Limit: 1,
	}); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("List() error = %v, want ErrInvalidPage", err)
	}
}
