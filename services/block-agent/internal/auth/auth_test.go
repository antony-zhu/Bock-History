package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFirstSetupAndLoginReturnStatelessIdentity(t *testing.T) {
	store := newMemoryStore()
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.FirstSetup(context.Background(), "admin", "one", "different"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("mismatched setup error = %v", err)
	}
	created, err := service.FirstSetup(context.Background(), "admin", "one", "one")
	if err != nil {
		t.Fatal(err)
	}
	if created != (Identity{Username: "admin", Role: RoleAdmin}) {
		t.Fatalf("first setup identity = %+v", created)
	}
	if permissions := created.Permissions(); !permissions.Operate || !permissions.Maintenance {
		t.Fatalf("admin permissions = %+v", permissions)
	}
	if _, err := service.FirstSetup(context.Background(), "other", "one", "one"); !errors.Is(err, ErrSetupCompleted) {
		t.Fatalf("repeat setup error = %v", err)
	}
	if _, err := service.Login(context.Background(), "admin", "bad"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("bad login error = %v", err)
	}
	identity, err := service.Login(context.Background(), "admin", "one")
	if err != nil {
		t.Fatal(err)
	}
	if identity != created {
		t.Fatalf("login identity = %+v, want %+v", identity, created)
	}
}

func TestPasswordChangeRequiresCurrentPasswordAndIdleTimeoutPersists(t *testing.T) {
	store := newMemoryStore()
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FirstSetup(context.Background(), "admin", "one", "one"); err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(context.Background(), "admin", "wrong", "two"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong current password error = %v", err)
	}
	if err := service.ChangePassword(context.Background(), "admin", "one", "two"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(context.Background(), "admin", "one"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password login error = %v", err)
	}
	if _, err := service.Login(context.Background(), "admin", "two"); err != nil {
		t.Fatal(err)
	}

	if err := service.SetIdleTimeout(context.Background(), 59*time.Second); !errors.Is(err, ErrInvalidIdleTimeout) {
		t.Fatalf("short timeout error = %v", err)
	}
	if err := service.SetIdleTimeout(context.Background(), 120*time.Second); err != nil {
		t.Fatal(err)
	}
	timeout, err := service.IdleTimeout(context.Background())
	if err != nil || timeout != 120*time.Second {
		t.Fatalf("idle timeout = %s, error = %v", timeout, err)
	}
}

func TestRolePermissions(t *testing.T) {
	if got := (Identity{Role: RoleViewer}).Permissions(); got.Operate || got.Maintenance {
		t.Fatalf("viewer permissions = %+v", got)
	}
	if got := (Identity{Role: RoleOperator}).Permissions(); !got.Operate || got.Maintenance {
		t.Fatalf("operator permissions = %+v", got)
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
