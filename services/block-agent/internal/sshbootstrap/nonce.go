package sshbootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type NonceStore struct {
	db *sql.DB
}

func OpenNonceStore(path string) (*NonceStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("nonce database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &NonceStore{db: db}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *NonceStore) initialize(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = FULL",
		"PRAGMA journal_mode = WAL",
		`CREATE TABLE IF NOT EXISTS used_nonce (
			kid TEXT NOT NULL,
			nonce TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			PRIMARY KEY (kid, nonce)
		) WITHOUT ROWID`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize nonce database: %w", err)
		}
	}
	return nil
}

func (s *NonceStore) Register(
	ctx context.Context,
	kid string,
	nonce string,
	tokenTimestamp int64,
	serverSeconds int64,
) (bool, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer transaction.Rollback()

	if _, err := transaction.ExecContext(
		ctx,
		"DELETE FROM used_nonce WHERE expires_at < ?",
		serverSeconds,
	); err != nil {
		return false, err
	}
	result, err := transaction.ExecContext(
		ctx,
		"INSERT OR IGNORE INTO used_nonce (kid, nonce, expires_at) VALUES (?, ?, ?)",
		kid,
		nonce,
		tokenTimestamp+60,
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := transaction.Commit(); err != nil {
		return false, err
	}
	return rows == 1, nil
}

func (s *NonceStore) Close() error {
	return s.db.Close()
}
