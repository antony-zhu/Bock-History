package alarmhistory

import "context"

type Service struct {
	store    Store
	notifier Notifier
}

func New(store Store, notifier Notifier) *Service {
	return &Service{store: store, notifier: notifier}
}

// Append persists the alarm record before making one best-effort notification.
// Notification errors are intentionally not retried or queued.
func (s *Service) Append(ctx context.Context, record Record) (Record, error) {
	if s.store == nil {
		return Record{}, ErrStoreUnavailable
	}
	stored, err := s.store.Append(ctx, record)
	if err != nil {
		return Record{}, err
	}
	if s.notifier != nil {
		_ = s.notifier.Notify(ctx, stored)
	}
	return stored, nil
}

// List is read-only and derives a pagination cursor from the returned records.
func (s *Service) List(ctx context.Context, query Query) (Page, error) {
	if s.store == nil {
		return Page{}, ErrStoreUnavailable
	}
	if err := query.Validate(); err != nil {
		return Page{}, err
	}
	records, hasMore, err := s.store.List(ctx, query)
	if err != nil {
		return Page{}, err
	}
	page := Page{Records: append([]Record(nil), records...), HasMore: hasMore}
	if hasMore {
		if len(records) == 0 || records[len(records)-1].HistoryCursor == 0 {
			return Page{}, ErrInvalidPage
		}
		cursor := records[len(records)-1].HistoryCursor
		page.NextCursor = &cursor
	}
	if err := page.Validate(); err != nil {
		return Page{}, err
	}
	return page, nil
}
