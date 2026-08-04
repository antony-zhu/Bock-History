package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"
)

type sessionEntry struct {
	session Session
	timer   *time.Timer
}

type sessions struct {
	mu        sync.Mutex
	now       func() time.Time
	timeout   time.Duration
	entries   map[[sha256.Size]byte]*sessionEntry
	onExpired func(Session)
}

func newSessions(timeout time.Duration, now func() time.Time, onExpired func(Session)) *sessions {
	return &sessions{now: now, timeout: timeout, entries: make(map[[sha256.Size]byte]*sessionEntry), onExpired: onExpired}
}

func (s *sessions) create(username string, role Role) (string, Session, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", Session{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	key := sha256.Sum256([]byte(token))
	now := s.now().UTC()
	entry := &sessionEntry{session: Session{Username: username, Role: role, LastActivity: now, ExpiresAt: now.Add(s.idleTimeout())}}
	s.mu.Lock()
	entry.timer = time.AfterFunc(s.timeout, func() { s.expire(key) })
	s.entries[key] = entry
	s.mu.Unlock()
	return token, entry.session, nil
}

func (s *sessions) lookup(token string, renew bool) (Session, bool) {
	key := sha256.Sum256([]byte(token))
	now := s.now().UTC()
	var expired *Session
	s.mu.Lock()
	entry := s.entries[key]
	if entry == nil {
		s.mu.Unlock()
		return Session{}, false
	}
	if !now.Before(entry.session.ExpiresAt) {
		delete(s.entries, key)
		entry.timer.Stop()
		copy := entry.session
		expired = &copy
		s.mu.Unlock()
		s.notifyExpired(*expired)
		return Session{}, false
	}
	if renew {
		entry.session.LastActivity = now
		entry.session.ExpiresAt = now.Add(s.timeout)
		entry.timer.Stop()
		entry.timer.Reset(s.timeout)
	}
	copy := entry.session
	s.mu.Unlock()
	return copy, true
}

func (s *sessions) delete(token string) {
	key := sha256.Sum256([]byte(token))
	s.mu.Lock()
	if entry := s.entries[key]; entry != nil {
		delete(s.entries, key)
		entry.timer.Stop()
	}
	s.mu.Unlock()
}

func (s *sessions) idleTimeout() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timeout
}

func (s *sessions) setIdleTimeout(timeout time.Duration) {
	now := s.now().UTC()
	var expired []Session
	s.mu.Lock()
	s.timeout = timeout
	for key, entry := range s.entries {
		entry.session.ExpiresAt = entry.session.LastActivity.Add(timeout)
		if !now.Before(entry.session.ExpiresAt) {
			delete(s.entries, key)
			entry.timer.Stop()
			expired = append(expired, entry.session)
			continue
		}
		entry.timer.Stop()
		entry.timer.Reset(entry.session.ExpiresAt.Sub(now))
	}
	s.mu.Unlock()
	for _, session := range expired {
		s.notifyExpired(session)
	}
}

func (s *sessions) expireDue() {
	now := s.now().UTC()
	var expired []Session
	s.mu.Lock()
	for key, entry := range s.entries {
		if now.Before(entry.session.ExpiresAt) {
			continue
		}
		delete(s.entries, key)
		entry.timer.Stop()
		expired = append(expired, entry.session)
	}
	s.mu.Unlock()
	for _, session := range expired {
		s.notifyExpired(session)
	}
}

func (s *sessions) expire(key [sha256.Size]byte) {
	now := s.now().UTC()
	var expired *Session
	s.mu.Lock()
	entry := s.entries[key]
	if entry == nil {
		s.mu.Unlock()
		return
	}
	if now.Before(entry.session.ExpiresAt) {
		entry.timer.Reset(entry.session.ExpiresAt.Sub(now))
		s.mu.Unlock()
		return
	}
	delete(s.entries, key)
	copy := entry.session
	expired = &copy
	s.mu.Unlock()
	s.notifyExpired(*expired)
}

func (s *sessions) close() {
	s.mu.Lock()
	for _, entry := range s.entries {
		entry.timer.Stop()
	}
	s.entries = make(map[[sha256.Size]byte]*sessionEntry)
	s.mu.Unlock()
}

func (s *sessions) notifyExpired(session Session) {
	if s.onExpired != nil {
		s.onExpired(session)
	}
}
