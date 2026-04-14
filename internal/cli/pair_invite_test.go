package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"jarvisai/runtime/hostd/internal/config"
	"jarvisai/runtime/hostd/internal/state"
)

func TestPairClaimInvitePersistsConfigAndState(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")
	statePath := filepath.Join(tempDir, "state.json")
	var claimedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", request.Method)
		}
		if request.URL.Path != "/api/host/runtime/invites/claim" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		if request.URL.Query().Get("code") != "invite-code-1" {
			t.Fatalf("unexpected invite code query: %s", request.URL.RawQuery)
		}
		if err := json.NewDecoder(request.Body).Decode(&claimedBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"runtime_id":         "runtime-1",
			"pairing_state":      "paired",
			"runtime_token":      "runtime-token-1",
			"request_state":      "approved",
			"pairing_request_id": "pairing-1",
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Execute(context.Background(), []string{
		"pair",
		"claim-invite",
		"--config", configPath,
		"--state", statePath,
		"--invite-url", server.URL + "/api/host/runtime/invites/claim?code=invite-code-1",
	}, Dependencies{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("execute pair claim-invite: %v", err)
	}

	loaded, err := config.Load(config.Options{ConfigPath: configPath, StatePath: statePath})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	expectedGatewayWSURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/ws/node"
	if loaded.Config.Gateway.WSURL != expectedGatewayWSURL {
		t.Fatalf("unexpected gateway ws url: %s", loaded.Config.Gateway.WSURL)
	}
	if loaded.Config.Gateway.TLSMode != "off" {
		t.Fatalf("unexpected tls mode: %s", loaded.Config.Gateway.TLSMode)
	}

	store := state.NewStore(statePath)
	current, err := store.Load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if current.RuntimeToken != "runtime-token-1" {
		t.Fatalf("unexpected runtime token: %s", current.RuntimeToken)
	}
	if current.PairingState != state.PairingStatePaired {
		t.Fatalf("unexpected pairing state: %s", current.PairingState)
	}
	if current.LastGatewayURL == "" {
		t.Fatalf("last_gateway_url should be recorded")
	}

	runtimePayload, ok := claimedBody["runtime"].(map[string]any)
	if !ok || strings.TrimSpace(runtimePayload["runtime_id"].(string)) == "" {
		t.Fatalf("runtime payload missing runtime_id: %#v", claimedBody)
	}
	components, ok := claimedBody["components"].([]any)
	if !ok || len(components) == 0 {
		t.Fatalf("components should be sent: %#v", claimedBody)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["has_runtime_token"] != true {
		t.Fatalf("unexpected output: %#v", output)
	}
	if output["bridge_mode"] != "state-fallback" {
		t.Fatalf("unexpected bridge mode: %#v", output["bridge_mode"])
	}
}

func TestParseInviteURLDerivesGatewayWSURL(t *testing.T) {
	claimURL, gatewayWSURL, tlsMode, err := parseInviteURL("https://jarvis.example.com/api/host/runtime/invites/claim?code=invite-1")
	if err != nil {
		t.Fatalf("parse invite url: %v", err)
	}
	if claimURL != "https://jarvis.example.com/api/host/runtime/invites/claim?code=invite-1" {
		t.Fatalf("unexpected claim url: %s", claimURL)
	}
	if gatewayWSURL != "wss://jarvis.example.com/ws/node" {
		t.Fatalf("unexpected gateway ws url: %s", gatewayWSURL)
	}
	if tlsMode != "system" {
		t.Fatalf("unexpected tls mode: %s", tlsMode)
	}
}
