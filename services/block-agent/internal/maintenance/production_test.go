package maintenance

import (
	"path/filepath"
	"testing"
)

func TestProductionStorePersistsValidatedPatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maintenance.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	target := 800
	box := 24
	updated, err := store.Patch(ProductionPatch{TargetProduction: &target, PiecesPerBox: &box})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TargetProduction != target || updated.PiecesPerBox != box || updated.ToolChangePieces != 100 {
		t.Fatalf("updated production = %#v", updated)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(); got != updated {
		t.Fatalf("persisted production = %#v, want %#v", got, updated)
	}
	invalid := 0
	if _, err := store.Patch(ProductionPatch{PiecesPerBox: &invalid}); err == nil {
		t.Fatal("invalid piecesPerBox was accepted")
	}
	maximum := 60000
	if _, err := store.Patch(ProductionPatch{TargetProduction: &maximum}); err != nil {
		t.Fatalf("maximum targetProduction was rejected: %v", err)
	}
	overMaximum := 60001
	if _, err := store.Patch(ProductionPatch{TargetProduction: &overMaximum}); err == nil {
		t.Fatal("targetProduction over 60000 was accepted")
	}
}
