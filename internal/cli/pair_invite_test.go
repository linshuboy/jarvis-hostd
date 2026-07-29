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

	"agi/runtime/hostd/internal/config"
	"agi/runtime/hostd/internal/state"
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

	store := state.NewStore(statePath)
	if _, err := store.Update(func(value *state.State) error {
		if _, ensureErr := state.EnsureRuntimeID(value); ensureErr != nil {
			return ensureErr
		}
		value.RuntimeToken = "current-runtime-token"
		value.LastGatewayURL = strings.Replace(server.URL, "http://", "ws://", 1) + "/ws/node"
		return nil
	}); err != nil {
		t.Fatalf("seed current runtime token: %v", err)
	}

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

	store = state.NewStore(statePath)
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
	if claimedBody["current_runtime_token"] != "current-runtime-token" {
		t.Fatalf("current runtime token proof missing: %#v", claimedBody)
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
	claimURL, gatewayWSURL, tlsMode, err := parseInviteURL("https://agi.example.com/api/host/runtime/invites/claim?code=invite-1")
	if err != nil {
		t.Fatalf("parse invite url: %v", err)
	}
	if claimURL != "https://agi.example.com/api/host/runtime/invites/claim?code=invite-1" {
		t.Fatalf("unexpected claim url: %s", claimURL)
	}
	if gatewayWSURL != "wss://agi.example.com/ws/node" {
		t.Fatalf("unexpected gateway ws url: %s", gatewayWSURL)
	}
	if tlsMode != "system" {
		t.Fatalf("unexpected tls mode: %s", tlsMode)
	}
}

func TestPairClaimInviteDoesNotSendCurrentTokenAcrossOrigins(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")
	statePath := filepath.Join(tempDir, "state.json")
	var claimedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&claimedBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"runtime_id":    "runtime-1",
			"pairing_state": "paired",
			"runtime_token": "new-runtime-token",
		})
	}))
	defer server.Close()

	store := state.NewStore(statePath)
	if _, err := store.Update(func(value *state.State) error {
		value.RuntimeID = "runtime-1"
		value.RuntimeToken = "old-runtime-token"
		value.LastGatewayURL = "wss://old.example.com/ws/node"
		return nil
	}); err != nil {
		t.Fatalf("seed current runtime state: %v", err)
	}

	err := Execute(context.Background(), []string{
		"pair",
		"claim-invite",
		"--config", configPath,
		"--state", statePath,
		"--invite-url", server.URL + "/api/host/runtime/invites/claim?code=invite-code-1",
	}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("execute pair claim-invite: %v", err)
	}
	if _, exists := claimedBody["current_runtime_token"]; exists {
		t.Fatalf("current runtime token leaked across origins: %#v", claimedBody)
	}
}

func TestClaimInviteRejectsCrossOriginRedirect(t *testing.T) {
	destinationCalled := false
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		destinationCalled = true
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL+"/claim", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	_, err := claimInvite(origin.URL+"/claim", map[string]any{
		"current_runtime_token": "current-runtime-token",
	})
	if err == nil || !strings.Contains(err.Error(), "redirect changed service origin") {
		t.Fatalf("expected cross-origin redirect rejection, got %v", err)
	}
	if destinationCalled {
		t.Fatal("cross-origin redirect received the claim request")
	}
}

func TestSameServiceOriginNormalizesGatewaySchemesAndDefaultPorts(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "secure gateway", left: "wss://AGI.example.com/ws/node", right: "https://agi.example.com:443/claim", want: true},
		{name: "plain gateway", left: "ws://agi.example.com:80/ws/node", right: "http://AGI.EXAMPLE.COM/claim", want: true},
		{name: "trailing dns dot", left: "wss://agi.example.com./ws/node", right: "https://agi.example.com/claim", want: true},
		{name: "scheme downgrade", left: "wss://agi.example.com/ws/node", right: "http://agi.example.com/claim", want: false},
		{name: "different port", left: "wss://agi.example.com:8443/ws/node", right: "https://agi.example.com/claim", want: false},
		{name: "different host", left: "wss://agi.example.com/ws/node", right: "https://evil.example.com/claim", want: false},
		{name: "missing proof", left: "", right: "https://agi.example.com/claim", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sameServiceOrigin(test.left, test.right); got != test.want {
				t.Fatalf("sameServiceOrigin(%q, %q) = %v, want %v", test.left, test.right, got, test.want)
			}
		})
	}
}
