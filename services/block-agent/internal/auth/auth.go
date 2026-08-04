// Package auth owns local HMI accounts and the in-memory login session.
package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Role string

const (
	RoleViewer   Role = "VIEWER"
	RoleOperator Role = "OPERATOR"
	RoleAdmin    Role = "ADMIN"

	DefaultIdleTimeout = 300 * time.Second
	MinIdleTimeout     = 60 * time.Second
	MaxIdleTimeout     = 3600 * time.Second
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
	ErrUnauthenticated    = errors.New("login session is missing or expired")
	ErrForbidden          = errors.New("permission denied")
	ErrInvalidIdleTimeout = errors.New("idle timeout must be between 60 and 3600 seconds")
)

type Account struct {
	Username     string
	PasswordHash string
	Role         Role
}

// Store persists accounts and the one system-wide idle timeout. Login session
// data intentionally does not belong here.
type Store interface {
	HasAdmin(context.Context) (bool, error)
	FindAccount(context.Context, string) (Account, bool, error)
	CreateAccount(context.Context, Account) error
	SetPassword(context.Context, string, string) error
	IdleTimeout(context.Context) (time.Duration, error)
	SetIdleTimeout(context.Context, time.Duration) error
}

type Session struct {
	Username     string
	Role         Role
	LastActivity time.Time
	ExpiresAt    time.Time
}

type LoginResult struct {
	Token string
	Session
}

type Service struct {
	store    Store
	now      func() time.Time
	sessions *sessions
	setupMu  sync.Mutex
}

func NewService(store Store, now func() time.Time, onExpired func(Session)) (*Service, error) {
	if now == nil {
		now = time.Now
	}
	timeout, err := store.IdleTimeout(context.Background())
	if err != nil {
		return nil, err
	}
	if !validIdleTimeout(timeout) {
		return nil, ErrInvalidIdleTimeout
	}
	return &Service{store: store, now: now, sessions: newSessions(timeout, now, onExpired)}, nil
}

func (s *Service) Close() { s.sessions.close() }

func (s *Service) FirstSetup(ctx context.Context, username, password, confirmPassword string) (LoginResult, error) {
	if err := validateCredentials(username, password, confirmPassword); err != nil {
		return LoginResult{}, err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	hasAdmin, err := s.store.HasAdmin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	if hasAdmin {
		return LoginResult{}, ErrSetupCompleted
	}
	hash, err := HashPassword(password)
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.store.CreateAccount(ctx, Account{Username: username, PasswordHash: hash, Role: RoleAdmin}); err != nil {
		return LoginResult{}, err
	}
	return s.newLogin(username, RoleAdmin)
}

func (s *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	if username == "" || password == "" {
		return LoginResult{}, ErrInvalidCredentials
	}
	account, found, err := s.store.FindAccount(ctx, username)
	if err != nil {
		return LoginResult{}, err
	}
	if !found || !VerifyPassword(account.PasswordHash, password) {
		return LoginResult{}, ErrInvalidCredentials
	}
	return s.newLogin(account.Username, account.Role)
}

// Session only reads session state. It never extends the idle deadline.
func (s *Service) Session(token string) (Session, error) {
	session, ok := s.sessions.lookup(token, false)
	if !ok {
		return Session{}, ErrUnauthenticated
	}
	return session, nil
}

// Activity is called only for an explicit user action. Transport keepalives,
// polling and server pushes must use Session instead.
func (s *Service) Activity(token string) (Session, error) {
	session, ok := s.sessions.lookup(token, true)
	if !ok {
		return Session{}, ErrUnauthenticated
	}
	return session, nil
}

func (s *Service) Logout(token string) { s.sessions.delete(token) }

func (s *Service) ChangePassword(ctx context.Context, token, currentPassword, newPassword, confirmPassword string) error {
	session, err := s.Activity(token)
	if err != nil {
		return err
	}
	if err := validateCredentials(session.Username, newPassword, confirmPassword); err != nil {
		return err
	}
	account, found, err := s.store.FindAccount(ctx, session.Username)
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
	return s.store.SetPassword(ctx, session.Username, hash)
}

func (s *Service) SetAccountPassword(ctx context.Context, token, username, newPassword, confirmPassword string) error {
	if _, err := s.requireAdmin(token); err != nil {
		return err
	}
	if err := validateCredentials(username, newPassword, confirmPassword); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.store.SetPassword(ctx, username, hash)
}

func (s *Service) CreateAccount(ctx context.Context, token, username, password, confirmPassword string, role Role) error {
	if _, err := s.requireAdmin(token); err != nil {
		return err
	}
	if err := validateCredentials(username, password, confirmPassword); err != nil {
		return err
	}
	if !role.Valid() {
		return ErrInvalidRole
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.store.CreateAccount(ctx, Account{Username: username, PasswordHash: hash, Role: role})
}

func (s *Service) IdleTimeout() time.Duration { return s.sessions.idleTimeout() }

func (s *Service) SetIdleTimeout(ctx context.Context, token string, timeout time.Duration) error {
	if _, err := s.requireAdmin(token); err != nil {
		return err
	}
	if !validIdleTimeout(timeout) {
		return ErrInvalidIdleTimeout
	}
	if err := s.store.SetIdleTimeout(ctx, timeout); err != nil {
		return err
	}
	s.sessions.setIdleTimeout(timeout)
	return nil
}

func (s *Service) ExpireSessions() { s.sessions.expireDue() }

func (s *Service) newLogin(username string, role Role) (LoginResult, error) {
	token, session, err := s.sessions.create(username, role)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, Session: session}, nil
}

func (s *Service) requireAdmin(token string) (Session, error) {
	session, err := s.Activity(token)
	if err != nil {
		return Session{}, err
	}
	if session.Role != RoleAdmin {
		return Session{}, ErrForbidden
	}
	return session, nil
}

func (r Role) Valid() bool {
	return r == RoleViewer || r == RoleOperator || r == RoleAdmin
}

func validIdleTimeout(timeout time.Duration) bool {
	return timeout >= MinIdleTimeout && timeout <= MaxIdleTimeout && timeout%time.Second == 0
}

func validateCredentials(username, password, confirmPassword string) error {
	if username == "" {
		return ErrInvalidUsername
	}
	if password == "" {
		return ErrInvalidPassword
	}
	if password != confirmPassword {
		return ErrPasswordMismatch
	}
	return nil
}
