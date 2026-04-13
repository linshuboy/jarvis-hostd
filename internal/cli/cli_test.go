package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jarvisai/runtime/hostd/internal/buildinfo"
	"jarvisai/runtime/hostd/internal/config"
	"jarvisai/runtime/hostd/internal/state"
)

type fakeRunner struct {
	called bool
}

func (f *fakeRunner) Run(ctx context.Context) error {
	f.called = true
	return nil
}

func TestPairSetTokenWritesState(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")
	var stdout bytes.Buffer
	err := Execute(context.Background(), []string{
		"pair",
		"set-token",
		"--state", statePath,
		"--token", "runtime-token-1",
	}, Dependencies{
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute pair set-token: %v", err)
	}
	store := state.NewStore(statePath)
	current, loadErr := store.Load()
	if loadErr != nil {
		t.Fatalf("load state: %v", loadErr)
	}
	if current.RuntimeID == "" {
		t.Fatalf("runtime_id should be generated")
	}
	if current.RuntimeToken != "runtime-token-1" {
		t.Fatalf("unexpected runtime token: %s", current.RuntimeToken)
	}
	if current.PairingState != state.PairingStatePaired {
		t.Fatalf("unexpected pairing state: %s", current.PairingState)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["has_runtime_token"] != true {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestPairStatusMasksToken(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")
	store := state.NewStore(statePath)
	if _, err := store.Update(func(value *state.State) error {
		_, ensureErr := state.EnsureRuntimeID(value)
		if ensureErr != nil {
			return ensureErr
		}
		value.RuntimeToken = "runtime-token-1"
		value.PairingState = state.PairingStatePaired
		return nil
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	var stdout bytes.Buffer
	err := Execute(context.Background(), []string{
		"pair",
		"status",
		"--state", statePath,
	}, Dependencies{
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute pair status: %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["has_runtime_token"] != true {
		t.Fatalf("unexpected output: %#v", output)
	}
	if _, exists := output["runtime_token"]; exists {
		t.Fatalf("runtime token should not be printed")
	}
}

func TestRunCommandUsesRunnerFactory(t *testing.T) {
	runner := &fakeRunner{}
	err := Execute(context.Background(), []string{"run", "--gateway-ws-url", "ws://gateway/ws/node"}, Dependencies{
		Stdout: io.Discard,
		Stderr: io.Discard,
		RunnerFactory: func(cfg config.Config, store *state.Store, logger *log.Logger) (Runner, error) {
			_ = store
			_ = logger
			if cfg.Gateway.WSURL != "ws://gateway/ws/node" {
				t.Fatalf("unexpected ws url: %s", cfg.Gateway.WSURL)
			}
			return runner, nil
		},
	})
	if err != nil {
		t.Fatalf("execute run: %v", err)
	}
	if !runner.called {
		t.Fatalf("runner should be called")
	}
}

func TestConfigValidatePrintsEffectiveConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")
	statePath := filepath.Join(tempDir, "state.json")
	content := `{
	  "gateway": {
	    "ws_url": "ws://gateway.example/ws/node",
	    "tls_mode": "system"
	  },
	  "display_name": "build-host-a"
	}`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var stdout bytes.Buffer
	err := Execute(context.Background(), []string{
		"config",
		"validate",
		"--config", configPath,
		"--state", statePath,
	}, Dependencies{
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute config validate: %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["valid"] != true {
		t.Fatalf("unexpected output: %#v", output)
	}
	if output["config_path"] != configPath {
		t.Fatalf("unexpected config path: %#v", output["config_path"])
	}
	if output["state_path"] != statePath {
		t.Fatalf("unexpected state path: %#v", output["state_path"])
	}
}

func TestVersionCommandPrintsBuildInfo(t *testing.T) {
	var stdout bytes.Buffer
	err := Execute(context.Background(), []string{"version"}, Dependencies{
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute version: %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["version"] != buildinfo.RuntimeVersion() {
		t.Fatalf("unexpected version: %#v", output["version"])
	}
	if output["commit"] != buildinfo.BuildCommit() {
		t.Fatalf("unexpected commit: %#v", output["commit"])
	}
	if output["go_version"] == "" {
		t.Fatalf("go_version should not be empty")
	}
}

func TestAppSnapshotFallsBackWithoutControlServer(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")
	store := state.NewStore(statePath)
	if _, err := store.Update(func(value *state.State) error {
		_, ensureErr := state.EnsureRuntimeID(value)
		if ensureErr != nil {
			return ensureErr
		}
		value.PairingState = state.PairingStatePending
		value.LastError = "PAIRING_REQUIRED: runtime pairing required"
		return nil
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	var stdout bytes.Buffer
	err := Execute(context.Background(), []string{
		"app",
		"snapshot",
		"--state", statePath,
	}, Dependencies{
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute app snapshot: %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["bridge_mode"] != "state-fallback" {
		t.Fatalf("unexpected bridge mode: %#v", output["bridge_mode"])
	}
	if output["helper_available"] != false {
		t.Fatalf("unexpected helper availability: %#v", output["helper_available"])
	}
	if output["pairing_state"] != state.PairingStatePending {
		t.Fatalf("unexpected pairing state: %#v", output["pairing_state"])
	}
}

func TestAppSetTokenFallsBackToPersistedState(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")
	var stdout bytes.Buffer
	err := Execute(context.Background(), []string{
		"app",
		"set-token",
		"--state", statePath,
		"--token", "runtime-token-3",
	}, Dependencies{
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute app set-token: %v", err)
	}
	store := state.NewStore(statePath)
	current, loadErr := store.Load()
	if loadErr != nil {
		t.Fatalf("load state: %v", loadErr)
	}
	if current.RuntimeToken != "runtime-token-3" {
		t.Fatalf("unexpected runtime token: %s", current.RuntimeToken)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["bridge_mode"] != "state-fallback" {
		t.Fatalf("unexpected bridge mode: %#v", output["bridge_mode"])
	}
	if output["has_runtime_token"] != true {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestAppShutdownFallsBackWithoutControlServer(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")
	var stdout bytes.Buffer
	err := Execute(context.Background(), []string{
		"app",
		"shutdown",
		"--state", statePath,
	}, Dependencies{
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute app shutdown: %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["shutdown_requested"] != false {
		t.Fatalf("unexpected shutdown result: %#v", output)
	}
	if output["helper_available"] != false {
		t.Fatalf("unexpected helper availability: %#v", output)
	}
}

func TestServiceStatusLaunchdPrintsUnsupportedStatusOnNonDarwin(t *testing.T) {
	var stdout bytes.Buffer
	err := Execute(context.Background(), []string{
		"service",
		"status-launchd",
	}, Dependencies{
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute service status-launchd: %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	status, ok := output["status"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected status payload: %#v", output["status"])
	}
	if status["service_manager"] != "launchd" {
		t.Fatalf("unexpected service manager: %#v", status["service_manager"])
	}
	if status["supported"] == true && runtime.GOOS != "darwin" {
		t.Fatalf("launchd should not be supported on %s: %#v", runtime.GOOS, status)
	}
}

func TestServiceRunIsRejectedOnNonWindows(t *testing.T) {
	err := Execute(context.Background(), []string{
		"service",
		"run",
		"--gateway-ws-url", "ws://gateway.example/ws/node",
	}, Dependencies{
		Stdout: io.Discard,
		Stderr: io.Discard,
		RunnerFactory: func(cfg config.Config, store *state.Store, logger *log.Logger) (Runner, error) {
			_ = cfg
			_ = store
			_ = logger
			return &fakeRunner{}, nil
		},
	})
	if err == nil {
		t.Fatalf("expected windows-only service error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "windows service mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunCommandRejectsInvalidConfigBeforeRunnerFactory(t *testing.T) {
	called := false
	err := Execute(context.Background(), []string{
		"run",
		"--gateway-ws-url", "wss://gateway.example/ws/node",
		"--gateway-tls-mode", "pinned",
	}, Dependencies{
		Stdout: io.Discard,
		Stderr: io.Discard,
		RunnerFactory: func(cfg config.Config, store *state.Store, logger *log.Logger) (Runner, error) {
			_ = store
			_ = logger
			_ = cfg
			called = true
			return &fakeRunner{}, nil
		},
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "gateway.tls_fingerprint is required") {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatalf("runner factory should not be called")
	}
}

func TestPairClearTokenRemovesPersistedToken(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")
	store := state.NewStore(statePath)
	if _, err := store.Update(func(value *state.State) error {
		_, ensureErr := state.EnsureRuntimeID(value)
		if ensureErr != nil {
			return ensureErr
		}
		value.RuntimeToken = "runtime-token-1"
		value.PairingState = state.PairingStatePaired
		return nil
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	err := Execute(context.Background(), []string{
		"pair",
		"clear-token",
		"--state", statePath,
	}, Dependencies{
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute pair clear-token: %v", err)
	}
	current, loadErr := store.Load()
	if loadErr != nil {
		t.Fatalf("load state: %v", loadErr)
	}
	if current.RuntimeToken != "" {
		t.Fatalf("runtime token should be cleared")
	}
	if current.PairingState != state.PairingStateUnpaired {
		t.Fatalf("unexpected pairing state: %s", current.PairingState)
	}
}

func TestPairRevokeMarksStateRevoked(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")
	store := state.NewStore(statePath)
	if _, err := store.Update(func(value *state.State) error {
		_, ensureErr := state.EnsureRuntimeID(value)
		if ensureErr != nil {
			return ensureErr
		}
		value.RuntimeToken = "runtime-token-1"
		value.PairingState = state.PairingStatePaired
		return nil
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	err := Execute(context.Background(), []string{
		"pair",
		"revoke",
		"--state", statePath,
	}, Dependencies{
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("execute pair revoke: %v", err)
	}
	current, loadErr := store.Load()
	if loadErr != nil {
		t.Fatalf("load state: %v", loadErr)
	}
	if current.RuntimeToken != "" {
		t.Fatalf("runtime token should be cleared")
	}
	if current.PairingState != state.PairingStateRevoked {
		t.Fatalf("unexpected pairing state: %s", current.PairingState)
	}
}
