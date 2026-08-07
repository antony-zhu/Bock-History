// Package auth owns local HMI accounts and their persisted idle setting.
package auth

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"
)

type Role string

const (
	RoleViewer   Role = "VIEWER"
	RoleOperator Role = "OPERATOR"
	RoleAdmin    Role = "ADMIN"

	DefaultIdleTimeout = 300 * time.Second
	MinIdleTimeout     = 60 * time.Second
	MaxIdleTimeout     = 3600 * time.Second
	MaxUsernameLength  = 128
	MaxPasswordLength  = 4096
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrSetupCompleted     = errors.New("initial setup is already complete")
	ErrPasswordMismatch   = errors.New("password confirmation does not match")
	ErrInvalidUsername    = errors.New("username is required")
	ErrInvalidPassword    = errors.New("password is required")
	ErrInvalidRole        = errors.New("invalid account role")
	ErrAccountExists      = errors.New("account already exists")
	ErrAccountNotFound    = errors.New("account does not exist")
	ErrInvalidIdleTimeout = errors.New("idle timeout must be between 60 and 3600 seconds")
)

type Account struct {
	Username     string
	PasswordHash string
	Role         Role
}

// Store persists accounts and the one system-wide idle timeout.
type Store interface {
	HasAdmin(context.Context) (bool, error)
	FindAccount(context.Context, string) (Account, bool, error)
	CreateAccount(context.Context, Account) error
	SetPassword(context.Context, string, string) error
	IdleTimeout(context.Context) (time.Duration, error)
	SetIdleTimeout(context.Context, time.Duration) error
}

// Identity is returned after credentials have been validated. It deliberately
// contains no token or backend session data.
type Identity struct {
	Username string
	Role     Role
}

type Permissions struct {
	Operate     bool `json:"operate"`
	Maintenance bool `json:"maintenance"`
}

type Service struct {
	store   Store
	setupMu sync.Mutex
}

func NewService(store Store) (*Service, error) {
	timeout, err := store.IdleTimeout(context.Background())
	if err != nil {
		return nil, err
	}
	if !validIdleTimeout(timeout) {
		return nil, ErrInvalidIdleTimeout
	}
	return &Service{store: store}, nil
}

// HasAdmin reports whether initial administrator setup is complete.
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	return s.store.HasAdmin(ctx)
}

func (s *Service) FirstSetup(ctx context.Context, username, password, confirmPassword string) (Identity, error) {
	if err := validateCredentials(username, password, confirmPassword); err != nil {
		return Identity{}, err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	hasAdmin, err := s.store.HasAdmin(ctx)
	if err != nil {
		return Identity{}, err
	}
	if hasAdmin {
		return Identity{}, ErrSetupCompleted
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Identity{}, err
	}
	if err := s.store.CreateAccount(ctx, Account{Username: username, PasswordHash: hash, Role: RoleAdmin}); err != nil {
		return Identity{}, err
	}
	return Identity{Username: username, Role: RoleAdmin}, nil
}

func (s *Service) Login(ctx context.Context, username, password string) (Identity, error) {
	if err := validateUsername(username); err != nil {
		return Identity{}, err
	}
	if err := validatePassword(password); err != nil {
		return Identity{}, err
	}
	account, found, err := s.store.FindAccount(ctx, username)
	if err != nil {
		return Identity{}, err
	}
	if !found || !VerifyPassword(account.PasswordHash, password) {
		return Identity{}, ErrInvalidCredentials
	}
	return Identity{Username: account.Username, Role: account.Role}, nil
}

func (s *Service) ChangePassword(ctx context.Context, username, currentPassword, newPassword string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	if err := validatePassword(currentPassword); err != nil {
		return err
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	account, found, err := s.store.FindAccount(ctx, username)
	if err != nil {
		return err
	}
	if !found || !VerifyPassword(account.PasswordHash, currentPassword) {
		return ErrInvalidCredentials
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.store.SetPassword(ctx, account.Username, hash)
}

func (s *Service) IdleTimeout(ctx context.Context) (time.Duration, error) {
	return s.store.IdleTimeout(ctx)
}

func (s *Service) SetIdleTimeout(ctx context.Context, timeout time.Duration) error {
	if !validIdleTimeout(timeout) {
		return ErrInvalidIdleTimeout
	}
	return s.store.SetIdleTimeout(ctx, timeout)
}

func (r Role) Valid() bool {
	return r == RoleViewer || r == RoleOperator || r == RoleAdmin
}

func (i Identity) Permissions() Permissions {
	switch i.Role {
	case RoleAdmin:
		return Permissions{Operate: true, Maintenance: true}
	case RoleOperator:
		return Permissions{Operate: true}
	default:
		return Permissions{}
	}
}

func validIdleTimeout(timeout time.Duration) bool {
	return timeout >= MinIdleTimeout && timeout <= MaxIdleTimeout && timeout%time.Second == 0
}

func validateCredentials(username, password, confirmPassword string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	if err := validatePassword(confirmPassword); err != nil {
		return err
	}
	if password != confirmPassword {
		return ErrPasswordMismatch
	}
	return nil
}

func validateUsername(username string) error {
	if username == "" || utf8.RuneCountInString(username) > MaxUsernameLength {
		return ErrInvalidUsername
	}
	return nil
}

func validatePassword(password string) error {
	if password == "" || utf8.RuneCountInString(password) > MaxPasswordLength {
		return ErrInvalidPassword
	}
	return nil
}
