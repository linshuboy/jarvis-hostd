package appctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerRoundTrip(t *testing.T) {
	tempDir := workspaceTempDir(t)
	socketPath := filepath.Join(tempDir, "control.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := Snapshot{
		RuntimeID:       "runtime-1",
		PairingState:    "pending",
		HasRuntimeToken: false,
		Online:          false,
		ConnectionState: "waiting_for_pairing",
		HelperPID:       1234,
	}
	server, err := Start(ctx, ServerOptions{
		SocketPath: socketPath,
		Snapshot: func() (Snapshot, error) {
			return current, nil
		},
		SetRuntimeToken: func(token string) (Snapshot, error) {
			current.PairingState = "paired"
			current.HasRuntimeToken = token != ""
			current.Online = true
			current.ConnectionState = "connected"
			return current, nil
		},
		ClearRuntimeToken: func() (Snapshot, error) {
			current.PairingState = "unpaired"
			current.HasRuntimeToken = false
			current.Online = false
			current.ConnectionState = "offline"
			return current, nil
		},
		RequestReconnect: func() (Snapshot, error) {
			current.ConnectionState = "connecting"
			return current, nil
		},
	})
	if err != nil {
		if errors.Is(err, ErrUnavailable) || stringsContains(err.Error(), "operation not permitted") {
			t.Skipf("unix socket unavailable in this environment: %v", err)
		}
		t.Fatalf("start app control server: %v", err)
	}
	defer func() {
		_ = server.Close()
	}()

	snapshot, err := SnapshotStatus(socketPath)
	if err != nil {
		t.Fatalf("snapshot status: %v", err)
	}
	if snapshot.PairingState != "pending" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}

	snapshot, err = SetRuntimeToken(socketPath, "runtime-token-1")
	if err != nil {
		t.Fatalf("set runtime token: %v", err)
	}
	if !snapshot.HasRuntimeToken || snapshot.PairingState != "paired" {
		t.Fatalf("unexpected snapshot after set-token: %#v", snapshot)
	}

	snapshot, err = RequestReconnect(socketPath)
	if err != nil {
		t.Fatalf("request reconnect: %v", err)
	}
	if snapshot.ConnectionState != "connecting" {
		t.Fatalf("unexpected reconnect snapshot: %#v", snapshot)
	}

	snapshot, err = ClearRuntimeToken(socketPath)
	if err != nil {
		t.Fatalf("clear runtime token: %v", err)
	}
	if snapshot.HasRuntimeToken || snapshot.PairingState != "unpaired" {
		t.Fatalf("unexpected snapshot after clear-token: %#v", snapshot)
	}
}

func TestSnapshotStatusReturnsUnavailableWhenSocketMissing(t *testing.T) {
	tempDir := workspaceTempDir(t)
	_, err := SnapshotStatus(filepath.Join(tempDir, "missing.sock"))
	if err == nil {
		t.Fatalf("expected unavailable error")
	}
	if err != ErrUnavailable {
		t.Fatalf("unexpected error: %v", err)
	}
}

func workspaceTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".appctl-test-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}

func stringsContains(value string, needle string) bool {
	return strings.Contains(value, needle)
}
