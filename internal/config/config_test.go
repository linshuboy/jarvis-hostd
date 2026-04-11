package config

import (
	"os"
	"path/filepath"
	"testing"
)

func stringPtr(value string) *string {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func TestLoadPrecedence(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")
	content := `{
	  "gateway": {
	    "ws_url": "ws://file.example/ws/node",
	    "tls_mode": "system"
	  },
	  "display_name": "file-name",
	  "heartbeat_seconds": 9,
	  "components": {
	    "host": {
	      "enabled": false
	    }
	  },
	  "logging": {
	    "level": "warn"
	  }
	}`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOSTD_GATEWAY_WS_URL", "ws://env.example/ws/node")
	t.Setenv("HOSTD_DISPLAY_NAME", "env-name")
	t.Setenv("HOSTD_HEARTBEAT_SECONDS", "15")
	loaded, err := Load(Options{
		ConfigPath:       configPath,
		StatePath:        filepath.Join(tempDir, "state.json"),
		GatewayWSURL:     stringPtr("ws://flag.example/ws/node"),
		DisplayName:      stringPtr("flag-name"),
		HeartbeatSeconds: intPtr(21),
		HostEnabled:      boolPtr(true),
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := loaded.Config.Gateway.WSURL; got != "ws://flag.example/ws/node" {
		t.Fatalf("unexpected gateway ws url: %s", got)
	}
	if got := loaded.Config.DisplayName; got != "flag-name" {
		t.Fatalf("unexpected display name: %s", got)
	}
	if got := loaded.Config.HeartbeatSeconds; got != 21 {
		t.Fatalf("unexpected heartbeat seconds: %d", got)
	}
	if !loaded.Config.Components.Host.Enabled {
		t.Fatalf("expected host component enabled")
	}
	if len(loaded.Config.Components.Host.Methods) != len(hostMethods) {
		t.Fatalf("unexpected host methods length: %d", len(loaded.Config.Components.Host.Methods))
	}
}

func TestValidateForRunRejectsMissingGatewayURL(t *testing.T) {
	cfg := defaultConfig()
	if err := ValidateForRun(cfg); err == nil {
		t.Fatalf("expected missing gateway ws url error")
	}
}

func TestValidateForRunRejectsPinnedWithoutFingerprint(t *testing.T) {
	cfg := defaultConfig()
	cfg.Gateway.WSURL = "wss://gateway.example/ws/node"
	cfg.Gateway.TLSMode = "pinned"
	cfg.Gateway.TLSFingerprint = ""
	if err := ValidateForRun(cfg); err == nil {
		t.Fatalf("expected missing tls fingerprint error")
	}
}

func TestValidateForRunAcceptsPinnedWithFingerprint(t *testing.T) {
	cfg := defaultConfig()
	cfg.Gateway.WSURL = "wss://gateway.example/ws/node"
	cfg.Gateway.TLSMode = "pinned"
	cfg.Gateway.TLSFingerprint = "sha256:abcdef"
	if err := ValidateForRun(cfg); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestResolveControlSocketPathDefaultsNextToState(t *testing.T) {
	tempDir := t.TempDir()
	controlSocketPath, err := ResolveControlSocketPath(Options{
		StatePath: filepath.Join(tempDir, "state.json"),
	})
	if err != nil {
		t.Fatalf("resolve control socket path: %v", err)
	}
	if got, want := controlSocketPath, filepath.Join(tempDir, "control.sock"); got != want {
		t.Fatalf("unexpected control socket path: got %s want %s", got, want)
	}
}
