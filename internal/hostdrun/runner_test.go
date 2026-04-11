package hostdrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"jarvisai/runtime/hostd/internal/config"
	"jarvisai/runtime/hostd/internal/host"
	"jarvisai/runtime/hostd/internal/protocol"
	"jarvisai/runtime/hostd/internal/state"
	"jarvisai/runtime/hostd/internal/wsclient"
)

type fakeConn struct {
	readFrames []any
	writes     []any
	index      int
}

func (f *fakeConn) ReadJSON(ctx context.Context, value any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if f.index >= len(f.readFrames) {
		return io.EOF
	}
	payload, err := json.Marshal(f.readFrames[f.index])
	if err != nil {
		return err
	}
	f.index++
	return json.Unmarshal(payload, value)
}

func (f *fakeConn) WriteJSON(ctx context.Context, value any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	f.writes = append(f.writes, value)
	return nil
}

func (f *fakeConn) Close() error {
	return nil
}

type fakeDialer struct {
	conn wsclient.Conn
	err  error
}

func (f fakeDialer) Dial(ctx context.Context, options wsclient.Options) (wsclient.Conn, error) {
	_ = ctx
	_ = options
	if f.err != nil {
		return nil, f.err
	}
	return f.conn, nil
}

type sequenceDialer struct {
	mu    sync.Mutex
	conns []wsclient.Conn
	calls int
}

func (s *sequenceDialer) Dial(ctx context.Context, options wsclient.Options) (wsclient.Conn, error) {
	_ = ctx
	_ = options
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	index := s.calls - 1
	if index >= len(s.conns) {
		return nil, io.EOF
	}
	return s.conns[index], nil
}

func (s *sequenceDialer) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestRunOncePairingRequiredPersistsPendingState(t *testing.T) {
	store := state.NewStore(filepath.Join(t.TempDir(), "state.json"))
	component, err := host.NewComponent(host.Options{ComponentID: host.ComponentID, RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	conn := &fakeConn{
		readFrames: []any{
			map[string]any{
				"type": "res",
				"id":   "connect-1",
				"ok":   false,
				"error": map[string]any{
					"code":    "PAIRING_REQUIRED",
					"message": "runtime pairing required",
				},
			},
		},
	}
	runner := New(Options{
		Config: config.Config{
			Gateway: config.GatewayConfig{
				WSURL:   "ws://gateway.example/ws/node",
				TLSMode: "off",
			},
			DisplayName:      "Host A",
			HeartbeatSeconds: 20,
			Components: config.ComponentsConfig{
				Host: config.HostComponentConfig{
					Enabled: true,
					Methods: append([]string(nil), host.Methods...),
				},
			},
		},
		StateStore:    store,
		HostComponent: component,
		Dialer:        fakeDialer{conn: conn},
		Logger:        log.New(io.Discard, "", 0),
	})

	err = runner.runOnce(context.Background())
	if !errors.Is(err, errPairingRequired) {
		t.Fatalf("unexpected error: %v", err)
	}
	current, loadErr := store.Load()
	if loadErr != nil {
		t.Fatalf("load state: %v", loadErr)
	}
	if current.RuntimeID == "" {
		t.Fatalf("runtime_id should be persisted")
	}
	if current.PairingState != state.PairingStatePending {
		t.Fatalf("unexpected pairing state: %s", current.PairingState)
	}
	if len(conn.writes) != 1 {
		t.Fatalf("unexpected write count: %d", len(conn.writes))
	}
}

func TestBuildConnectRequestIncludesRuntimeAndHostComponent(t *testing.T) {
	component, err := host.NewComponent(host.Options{ComponentID: host.ComponentID, RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	runner := New(Options{
		Config: config.Config{
			Gateway: config.GatewayConfig{
				WSURL:   "ws://gateway.example/ws/node",
				TLSMode: "off",
			},
			DisplayName:      "Host A",
			HeartbeatSeconds: 20,
			Components: config.ComponentsConfig{
				Host: config.HostComponentConfig{
					Enabled: true,
					Methods: append([]string(nil), host.Methods...),
				},
			},
		},
		StateStore:    state.NewStore(filepath.Join(t.TempDir(), "state.json")),
		HostComponent: component,
		Logger:        log.New(io.Discard, "", 0),
	})
	frame := runner.buildConnectRequest(state.State{RuntimeID: "runtime-1", RuntimeToken: "token-1"}, "connect-1")
	if frame.Method != "connect" {
		t.Fatalf("unexpected method: %s", frame.Method)
	}
	if frame.Params.Runtime.ID != "runtime-1" {
		t.Fatalf("unexpected runtime id: %s", frame.Params.Runtime.ID)
	}
	if frame.Params.Auth.Token != "token-1" {
		t.Fatalf("unexpected token: %s", frame.Params.Auth.Token)
	}
	if len(frame.Params.Components) != 1 {
		t.Fatalf("unexpected components length: %d", len(frame.Params.Components))
	}
	if frame.Params.Components[0].ComponentID != host.ComponentID {
		t.Fatalf("unexpected component id: %s", frame.Params.Components[0].ComponentID)
	}
}

func TestRunOnceUsesPersistedTokenFromState(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")
	store := state.NewStore(statePath)
	if _, err := store.Update(func(value *state.State) error {
		_, ensureErr := state.EnsureRuntimeID(value)
		return ensureErr
	}); err != nil {
		t.Fatalf("seed runtime id: %v", err)
	}
	if _, err := store.Update(func(value *state.State) error {
		value.RuntimeToken = "runtime-token-2"
		value.PairingState = state.PairingStatePaired
		return nil
	}); err != nil {
		t.Fatalf("persist runtime token: %v", err)
	}
	component, err := host.NewComponent(host.Options{ComponentID: host.ComponentID, RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	conn := &fakeConn{
		readFrames: []any{
			map[string]any{
				"type": "res",
				"id":   "connect-1",
				"ok":   true,
				"payload": map[string]any{
					"type":     "hello-ok",
					"protocol": 1,
					"policy": map[string]any{
						"tickIntervalMs": 15000,
						"maxTtlSeconds":  60,
					},
					"runtime": map[string]any{
						"runtimeId":          "runtime-1",
						"pairingState":       "paired",
						"acceptedComponents": []string{host.ComponentID},
					},
				},
			},
		},
	}
	runner := New(Options{
		Config: config.Config{
			Gateway: config.GatewayConfig{
				WSURL:   "ws://gateway.example/ws/node",
				TLSMode: "off",
			},
			DisplayName:      "Host A",
			HeartbeatSeconds: 20,
			Components: config.ComponentsConfig{
				Host: config.HostComponentConfig{
					Enabled: true,
					Methods: append([]string(nil), host.Methods...),
				},
			},
		},
		StateStore:    store,
		HostComponent: component,
		Dialer:        fakeDialer{conn: conn},
		Logger:        log.New(io.Discard, "", 0),
	})

	err = runner.runOnce(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conn.writes) == 0 {
		t.Fatalf("expected connect request to be written")
	}
	connectFrame, ok := conn.writes[0].(protocol.ConnectRequest)
	if !ok {
		t.Fatalf("unexpected frame type: %#v", conn.writes[0])
	}
	if connectFrame.Params.Auth.Token != "runtime-token-2" {
		t.Fatalf("unexpected token on reconnect: %s", connectFrame.Params.Auth.Token)
	}
}

func TestSetRuntimeTokenTriggersImmediateReconnect(t *testing.T) {
	tempDir := workspaceTempDir(t)
	statePath := filepath.Join(tempDir, "state.json")
	store := state.NewStore(statePath)
	component, err := host.NewComponent(host.Options{ComponentID: host.ComponentID, RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	conn1 := &fakeConn{
		readFrames: []any{
			map[string]any{
				"type": "res",
				"id":   "connect-1",
				"ok":   false,
				"error": map[string]any{
					"code":    "PAIRING_REQUIRED",
					"message": "runtime pairing required",
				},
			},
		},
	}
	conn2 := &fakeConn{
		readFrames: []any{
			map[string]any{
				"type": "res",
				"id":   "connect-2",
				"ok":   true,
				"payload": map[string]any{
					"type":     "hello-ok",
					"protocol": 1,
					"policy": map[string]any{
						"tickIntervalMs": 15000,
						"maxTtlSeconds":  60,
					},
					"runtime": map[string]any{
						"runtimeId":          "runtime-1",
						"pairingState":       "paired",
						"acceptedComponents": []string{host.ComponentID},
					},
				},
			},
		},
	}
	dialer := &sequenceDialer{conns: []wsclient.Conn{conn1, conn2}}
	runner := New(Options{
		Config: config.Config{
			Gateway: config.GatewayConfig{
				WSURL:   "ws://gateway.example/ws/node",
				TLSMode: "off",
			},
			DisplayName:      "Host A",
			HeartbeatSeconds: 20,
			Components: config.ComponentsConfig{
				Host: config.HostComponentConfig{
					Enabled: true,
					Methods: append([]string(nil), host.Methods...),
				},
			},
		},
		StateStore:    store,
		HostComponent: component,
		Dialer:        dialer,
		Logger:        log.New(io.Discard, "", 0),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- runner.Run(ctx)
	}()

	_, err = runner.setRuntimeToken("runtime-token-9")
	if err != nil {
		t.Fatalf("set runtime token: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if dialer.CallCount() >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for reconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatalf("runner did not stop")
	}

	if len(conn2.writes) == 0 {
		t.Fatalf("expected reconnect connect request")
	}
	connectFrame, ok := conn2.writes[0].(protocol.ConnectRequest)
	if !ok {
		t.Fatalf("unexpected frame type: %#v", conn2.writes[0])
	}
	if connectFrame.Params.Auth.Token != "runtime-token-9" {
		t.Fatalf("unexpected reconnect token: %s", connectFrame.Params.Auth.Token)
	}
}

func workspaceTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".runner-test-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}
