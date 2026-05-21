package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"runtime"
	"strings"
	"time"

	"agi/runtime/hostd/internal/appctl"
	"agi/runtime/hostd/internal/buildinfo"
	"agi/runtime/hostd/internal/config"
	"agi/runtime/hostd/internal/host"
	"agi/runtime/hostd/internal/state"
)

type inviteClaimResponse struct {
	RuntimeID    string `json:"runtime_id"`
	PairingState string `json:"pairing_state"`
	RuntimeToken string `json:"runtime_token"`
}

func pairClaimInvite(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("pair claim-invite", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var (
		configPath        string
		statePath         string
		controlSocketPath string
		inviteURL         string
	)
	flags.StringVar(&configPath, "config", "", "path to hostd config.json")
	flags.StringVar(&statePath, "state", "", "path to hostd state.json")
	flags.StringVar(&controlSocketPath, "control-socket", "", "path to hostd app control socket")
	flags.StringVar(&inviteURL, "invite-url", "", "binding invite URL issued by the web console")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(inviteURL) == "" {
		return fmt.Errorf("invite-url is required")
	}
	options := config.Options{
		ConfigPath:        configPath,
		StatePath:         statePath,
		ControlSocketPath: controlSocketPath,
	}
	configPath, statePath, controlSocketPath, err := resolveAppPaths(options)
	if err != nil {
		return err
	}
	loaded, err := config.Load(options)
	if err != nil {
		return err
	}
	claimURL, gatewayWSURL, gatewayTLSMode, err := parseInviteURL(strings.TrimSpace(inviteURL))
	if err != nil {
		return err
	}
	loaded.Config.Gateway.WSURL = gatewayWSURL
	switch gatewayTLSMode {
	case "off":
		loaded.Config.Gateway.TLSMode = "off"
		loaded.Config.Gateway.TLSFingerprint = ""
	default:
		if strings.TrimSpace(loaded.Config.Gateway.TLSMode) != "pinned" || strings.TrimSpace(loaded.Config.Gateway.TLSFingerprint) == "" {
			loaded.Config.Gateway.TLSMode = "system"
			loaded.Config.Gateway.TLSFingerprint = ""
		}
	}
	if err := config.Save(configPath, loaded.Config); err != nil {
		return err
	}
	store := state.NewStore(statePath)
	current, err := store.Update(func(value *state.State) error {
		_, ensureErr := state.EnsureRuntimeID(value)
		if ensureErr != nil {
			return ensureErr
		}
		value.LastGatewayURL = gatewayWSURL
		return nil
	})
	if err != nil {
		return err
	}
	hostComponent, err := host.NewComponent(host.Options{
		ComponentID:        host.ComponentID,
		RuntimeVersion:     buildinfo.RuntimeVersion(),
		Methods:            append([]string(nil), loaded.Config.Components.Host.Methods...),
		WorkspaceHints:     hostWorkspaceHints(loaded.Config.Components.Host.WorkspaceHints),
		MaxReadBytes:       host.DefaultMaxReadBytes,
		MaxOutputBytes:     host.DefaultMaxOutputBytes,
		DefaultExecTimeout: host.DefaultExecTimeout,
		Update:             hostUpdateOptions(loaded.Config.Components.Host.Update),
	})
	if err != nil {
		return err
	}
	components := buildInviteComponents(loaded.Config, hostComponent)
	if len(components) == 0 {
		return fmt.Errorf("host.main must be enabled to claim a binding invite")
	}
	runtimePayload := map[string]any{
		"runtime_id":      current.RuntimeID,
		"display_name":    loaded.Config.DisplayName,
		"hostname":        hostComponent.Metadata()["hostname"],
		"platform":        runtime.GOOS,
		"runtime_version": buildinfo.RuntimeVersion(),
		"metadata": map[string]any{
			"arch": runtime.GOARCH,
		},
	}
	response, err := claimInvite(claimURL, map[string]any{
		"runtime":    runtimePayload,
		"components": components,
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(response.RuntimeToken) == "" {
		return fmt.Errorf("binding invite response missing runtime_token")
	}
	bridgeMode := "state-fallback"
	helperAvailable := false
	if _, setErr := appctl.SetRuntimeToken(controlSocketPath, response.RuntimeToken); setErr == nil {
		_, _ = appctl.RequestReconnect(controlSocketPath)
		bridgeMode = "control-socket"
		helperAvailable = true
	} else if !errors.Is(setErr, appctl.ErrUnavailable) {
		return setErr
	} else {
		if _, persistErr := persistRuntimeToken(statePath, response.RuntimeToken); persistErr != nil {
			return persistErr
		}
	}
	current, err = store.Update(func(value *state.State) error {
		value.PairingState = response.PairingState
		value.LastGatewayURL = gatewayWSURL
		value.LastError = ""
		return nil
	})
	if err != nil {
		return err
	}
	return printJSON(stdout, map[string]any{
		"config_path":         configPath,
		"state_path":          statePath,
		"control_socket_path": controlSocketPath,
		"runtime_id":          current.RuntimeID,
		"pairing_state":       current.PairingState,
		"has_runtime_token":   strings.TrimSpace(current.RuntimeToken) != "",
		"last_gateway_url":    current.LastGatewayURL,
		"helper_available":    helperAvailable,
		"bridge_mode":         bridgeMode,
	})
}

func parseInviteURL(raw string) (string, string, string, error) {
	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", "", fmt.Errorf("invalid invite url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", "", fmt.Errorf("invite url must use http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", "", "", fmt.Errorf("invite url host is required")
	}
	values := parsed.Query()
	if strings.TrimSpace(values.Get("code")) == "" {
		return "", "", "", fmt.Errorf("invite url missing code query")
	}
	gatewayScheme := "ws"
	tlsMode := "off"
	if parsed.Scheme == "https" {
		gatewayScheme = "wss"
		tlsMode = "system"
	}
	gatewayURL := (&neturl.URL{
		Scheme: gatewayScheme,
		Host:   parsed.Host,
		Path:   "/ws/node",
	}).String()
	return parsed.String(), gatewayURL, tlsMode, nil
}

func buildInviteComponents(cfg config.Config, hostComponent *host.Component) []map[string]any {
	if !cfg.Components.Host.Enabled || hostComponent == nil {
		return nil
	}
	definition := hostComponent.Definition()
	return []map[string]any{
		{
			"component_id": definition.ComponentID,
			"kind":         definition.Kind,
			"subtype":      definition.Subtype,
			"methods":      append([]string(nil), definition.Methods...),
			"health": map[string]any{
				"status":     definition.Health.Status,
				"checked_at": definition.Health.CheckedAt,
				"detail":     definition.Health.Detail,
			},
			"metadata": definition.Metadata,
		},
	}
}

func claimInvite(claimURL string, payload map[string]any) (inviteClaimResponse, error) {
	var result inviteClaimResponse
	encoded, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		claimURL,
		bytes.NewReader(encoded),
	)
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		var errorPayload struct {
			Detail string `json:"detail"`
			Error  string `json:"error"`
		}
		if decodeErr := json.NewDecoder(response.Body).Decode(&errorPayload); decodeErr == nil {
			if strings.TrimSpace(errorPayload.Detail) != "" {
				return result, errors.New(strings.TrimSpace(errorPayload.Detail))
			}
			if strings.TrimSpace(errorPayload.Error) != "" {
				return result, errors.New(strings.TrimSpace(errorPayload.Error))
			}
		}
		return result, fmt.Errorf("binding invite claim failed with status %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return result, err
	}
	return result, nil
}
