package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFirstSetupLoginAndPasswordConfirmation(t *testing.T) {
	store := newMemoryStore()
	service, err := NewService(store, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	if _, err := service.FirstSetup(context.Background(), "admin", "one", "different"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("mismatched setup error = %v", err)
	}
	created, err := service.FirstSetup(context.Background(), "admin", "one", "one")
	if err != nil {
		t.Fatal(err)
	}
	if created.Role != RoleAdmin || created.Token == "" {
		t.Fatalf("first setup result = %+v", created)
	}
	if _, err := service.FirstSetup(context.Background(), "other", "one", "one"); !errors.Is(err, ErrSetupCompleted) {
		t.Fatalf("repeat setup error = %v", err)
	}
	if _, err := service.Login(context.Background(), "admin", "bad"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("bad login error = %v", err)
	}
	if _, err := service.Login(context.Background(), "admin", "one"); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyExplicitActivityExtendsSessionAndExpiryCallsBack(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	var expired []Session
	service, err := NewService(store, func() time.Time { return now }, func(session Session) { expired = append(expired, session) })
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	result, err := service.FirstSetup(context.Background(), "admin", "one", "one")
	if err != nil {
		t.Fatal(err)
	}
	originalExpiry := result.ExpiresAt
	now = now.Add(299 * time.Second)
	lookup, err := service.Session(result.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.ExpiresAt.Equal(originalExpiry) {
		t.Fatalf("non-activity lookup extended session to %s", lookup.ExpiresAt)
	}
	active, err := service.Activity(result.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !active.ExpiresAt.Equal(now.Add(DefaultIdleTimeout)) {
		t.Fatalf("activity expiry = %s", active.ExpiresAt)
	}
	now = active.ExpiresAt
	service.ExpireSessions()
	if _, err := service.Session(result.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired session error = %v", err)
	}
	if len(expired) != 1 || expired[0].Username != "admin" {
		t.Fatalf("expiry callback = %#v", expired)
	}
}

func TestAdminCanPersistIdleTimeoutAndManageAccountPasswords(t *testing.T) {
	store := newMemoryStore()
	service, err := NewService(store, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	admin, err := service.FirstSetup(context.Background(), "admin", "one", "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CreateAccount(context.Background(), admin.Token, "viewer", "two", "two", RoleViewer); err != nil {
		t.Fatal(err)
	}
	viewer, err := service.Login(context.Background(), "viewer", "two")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetIdleTimeout(context.Background(), viewer.Token, 120*time.Second); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer idle configuration error = %v", err)
	}
	if err := service.SetIdleTimeout(context.Background(), admin.Token, 120*time.Second); err != nil {
		t.Fatal(err)
	}
	if store.timeout != 120*time.Second || service.IdleTimeout() != 120*time.Second {
		t.Fatalf("idle timeout was not persisted: store=%s service=%s", store.timeout, service.IdleTimeout())
	}
	if err := service.SetAccountPassword(context.Background(), admin.Token, "viewer", "three", "different"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("admin confirmation error = %v", err)
	}
	if err := service.SetAccountPassword(context.Background(), admin.Token, "viewer", "three", "three"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(context.Background(), "viewer", "three"); err != nil {
		t.Fatal(err)
	}
}

type memoryStore struct {
	mu       sync.Mutex
	accounts map[string]Account
	timeout  time.Duration
}

func newMemoryStore() *memoryStore {
	return &memoryStore{accounts: make(map[string]Account), timeout: DefaultIdleTimeout}
}

func (s *memoryStore) HasAdmin(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, account := range s.accounts {
		if account.Role == RoleAdmin {
			return true, nil
		}
	}
	return false, nil
}

func (s *memoryStore) FindAccount(_ context.Context, username string) (Account, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, found := s.accounts[username]
	return account, found, nil
}

func (s *memoryStore) CreateAccount(_ context.Context, account Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.accounts[account.Username]; exists {
		return ErrAccountExists
	}
	s.accounts[account.Username] = account
	return nil
}

func (s *memoryStore) SetPassword(_ context.Context, username, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, found := s.accounts[username]
	if !found {
		return ErrAccountNotFound
	}
	account.PasswordHash = passwordHash
	s.accounts[username] = account
	return nil
}

func (s *memoryStore) IdleTimeout(context.Context) (time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timeout, nil
}

func (s *memoryStore) SetIdleTimeout(_ context.Context, timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeout = timeout
	return nil
}
