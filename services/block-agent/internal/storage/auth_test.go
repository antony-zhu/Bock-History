package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"block.local/block-agent/internal/auth"
)

func TestLocalAccountsAndIdleTimeoutPersistInSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "block.db")
	store, err := Open(path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	timeout, err := store.IdleTimeout(ctx)
	if err != nil || timeout != auth.DefaultIdleTimeout {
		t.Fatalf("default timeout = %s, %v", timeout, err)
	}
	if err := store.CreateAccount(ctx, auth.Account{Username: "admin", PasswordHash: "hash", Role: auth.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetIdleTimeout(ctx, 120*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	hasAdmin, err := reopened.HasAdmin(ctx)
	if err != nil || !hasAdmin {
		t.Fatalf("has admin = %t, %v", hasAdmin, err)
	}
	account, found, err := reopened.FindAccount(ctx, "admin")
	if err != nil || !found || account.Role != auth.RoleAdmin || account.PasswordHash != "hash" {
		t.Fatalf("persisted account = %+v, found=%t, err=%v", account, found, err)
	}
	timeout, err = reopened.IdleTimeout(ctx)
	if err != nil || timeout != 120*time.Second {
		t.Fatalf("persisted timeout = %s, %v", timeout, err)
	}
}
