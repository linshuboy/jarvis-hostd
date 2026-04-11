package state

import (
	"path/filepath"
	"testing"
)

func TestEnsureRuntimeIDPersists(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	current, err := store.Update(func(value *State) error {
		_, err := EnsureRuntimeID(value)
		return err
	})
	if err != nil {
		t.Fatalf("ensure runtime id: %v", err)
	}
	if current.RuntimeID == "" {
		t.Fatalf("runtime_id should not be empty")
	}
	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if reloaded.RuntimeID != current.RuntimeID {
		t.Fatalf("runtime_id mismatch: %s != %s", reloaded.RuntimeID, current.RuntimeID)
	}
}
