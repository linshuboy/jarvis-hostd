package state

import (
	"os"
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

func TestLoadRepairsTrailingStateGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	content := `{
  "runtime_id": "runtime-1",
  "runtime_token": "runtime-token-1",
  "pairing_state": "paired",
  "last_gateway_url": "wss://example.test/ws/node",
  "last_connected_at": "2026-06-01T08:21:19Z"
}
 "last_error": "EOF"
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	store := NewStore(path)
	current, err := store.Load()
	if err != nil {
		t.Fatalf("load state with trailing garbage: %v", err)
	}
	if current.RuntimeID != "runtime-1" || current.RuntimeToken != "runtime-token-1" || current.PairingState != PairingStatePaired {
		t.Fatalf("unexpected repaired state: %#v", current)
	}
	reloaded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repaired state: %v", err)
	}
	if _, _, err := decodeState(reloaded); err != nil {
		t.Fatalf("repaired state should decode cleanly: %v", err)
	}
	if string(reloaded) == content {
		t.Fatalf("state file was not rewritten")
	}
}
