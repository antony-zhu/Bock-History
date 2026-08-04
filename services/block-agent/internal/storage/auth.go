package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"block.local/block-agent/internal/auth"
)

func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM local_accounts WHERE role = ?)`, auth.RoleAdmin).Scan(&found)
	return found != 0, err
}

func (s *Store) FindAccount(ctx context.Context, username string) (auth.Account, bool, error) {
	var account auth.Account
	err := s.db.QueryRowContext(ctx, `
		SELECT username, password_hash, role FROM local_accounts WHERE username = ?`, username,
	).Scan(&account.Username, &account.PasswordHash, &account.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Account{}, false, nil
	}
	if err != nil {
		return auth.Account{}, false, err
	}
	return account, true, nil
}

func (s *Store) CreateAccount(ctx context.Context, account auth.Account) error {
	if !account.Role.Valid() {
		return auth.ErrInvalidRole
	}
	_, exists, err := s.FindAccount(ctx, account.Username)
	if err != nil {
		return err
	}
	if exists {
		return auth.ErrAccountExists
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO local_accounts (username, password_hash, role) VALUES (?, ?, ?)`,
		account.Username, account.PasswordHash, account.Role,
	)
	return err
}

func (s *Store) SetPassword(ctx context.Context, username, passwordHash string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE local_accounts SET password_hash = ? WHERE username = ?`, passwordHash, username)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return auth.ErrAccountNotFound
	}
	return nil
}

func (s *Store) IdleTimeout(ctx context.Context) (time.Duration, error) {
	var seconds int64
	err := s.db.QueryRowContext(ctx, `SELECT idle_timeout_seconds FROM local_system_settings WHERE singleton_id = 1`).Scan(&seconds)
	if err != nil {
		return 0, err
	}
	return time.Duration(seconds) * time.Second, nil
}

func (s *Store) SetIdleTimeout(ctx context.Context, timeout time.Duration) error {
	if timeout < auth.MinIdleTimeout || timeout > auth.MaxIdleTimeout || timeout%time.Second != 0 {
		return auth.ErrInvalidIdleTimeout
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE local_system_settings SET idle_timeout_seconds = ? WHERE singleton_id = 1`, int64(timeout/time.Second),
	)
	return err
}
