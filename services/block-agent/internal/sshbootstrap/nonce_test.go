package sshbootstrap

import (
	"context"
	"path/filepath"
	"testing"
)

func TestNonceStorePersistsAndKeepsBoundarySecond(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonces.db")
	store, err := OpenNonceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := store.Register(context.Background(), "admin-1", "nonce-1", 1000, 1000)
	if err != nil || !inserted {
		t.Fatalf("first register: inserted=%t err=%v", inserted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenNonceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	inserted, err = store.Register(context.Background(), "admin-1", "nonce-1", 1000, 1060)
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("nonce replay at expiresAt boundary was accepted")
	}
	inserted, err = store.Register(context.Background(), "admin-1", "nonce-1", 1061, 1061)
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatal("expired nonce was not cleaned after the boundary")
	}
}

func TestNonceStoreScopesUniquenessByKID(t *testing.T) {
	store, err := OpenNonceStore(filepath.Join(t.TempDir(), "nonces.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, kid := range []string{"admin-1", "admin-2"} {
		inserted, err := store.Register(context.Background(), kid, "same-nonce", 1000, 1000)
		if err != nil || !inserted {
			t.Fatalf("register %s: inserted=%t err=%v", kid, inserted, err)
		}
	}
}
