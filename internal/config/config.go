package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var hostMethods = []string{
	"host.fs.stat",
	"host.fs.list",
	"host.fs.read",
	"host.fs.write",
	"host.fs.mkdir",
	"host.exec.run",
}

var allowedLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

type GatewayConfig struct {
	WSURL          string `json:"ws_url"`
	TLSMode        string `json:"tls_mode"`
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`
}

type HostComponentConfig struct {
	Enabled bool     `json:"enabled"`
	Methods []string `json:"methods"`
}

type ComponentsConfig struct {
	Host HostComponentConfig `json:"host"`
}

type LoggingConfig struct {
	Level string `json:"level"`
}

type Config struct {
	Gateway          GatewayConfig    `json:"gateway"`
	DisplayName      string           `json:"display_name"`
	HeartbeatSeconds int              `json:"heartbeat_seconds"`
	Components       ComponentsConfig `json:"components"`
	Logging          LoggingConfig    `json:"logging"`
}

type fileConfig struct {
	Gateway          *GatewayConfig    `json:"gateway,omitempty"`
	DisplayName      string            `json:"display_name,omitempty"`
	HeartbeatSeconds int               `json:"heartbeat_seconds,omitempty"`
	Components       *ComponentsConfig `json:"components,omitempty"`
	Logging          *LoggingConfig    `json:"logging,omitempty"`
}

type Options struct {
	ConfigPath            string
	StatePath             string
	ControlSocketPath     string
	GatewayWSURL          *string
	GatewayTLSMode        *string
	GatewayTLSFingerprint *string
	DisplayName           *string
	LoggingLevel          *string
	HeartbeatSeconds      *int
	HostEnabled           *bool
}

type Loaded struct {
	Config     Config
	ConfigPath string
	StatePath  string
}

func DefaultPaths() (string, string, error) {
	switch runtime.GOOS {
	case "windows":
		base := strings.TrimSpace(os.Getenv("ProgramData"))
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "hostd", "config.json"), filepath.Join(base, "hostd", "state.json"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
		base := filepath.Join(home, "Library", "Application Support", "hostd")
		return filepath.Join(base, "config.json"), filepath.Join(base, "state.json"), nil
	default:
		return "/etc/hostd/config.json", "/var/lib/hostd/state.json", nil
	}
}

func Load(options Options) (Loaded, error) {
	configPath, statePath, err := ResolvePaths(options)
	if err != nil {
		return Loaded{}, err
	}
	cfg := defaultConfig()
	if err := applyFile(configPath, &cfg); err != nil {
		return Loaded{}, err
	}
	if err := applyEnv(&cfg); err != nil {
		return Loaded{}, err
	}
	applyOptions(options, &cfg)
	if err := normalize(&cfg); err != nil {
		return Loaded{}, err
	}
	return Loaded{
		Config:     cfg,
		ConfigPath: configPath,
		StatePath:  statePath,
	}, nil
}

func Save(path string, cfg Config) error {
	if err := normalize(&cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
}

func ResolvePaths(options Options) (string, string, error) {
	defaultConfigPath, defaultStatePath, err := DefaultPaths()
	if err != nil {
		return "", "", err
	}
	configPath := strings.TrimSpace(options.ConfigPath)
	if configPath == "" {
		configPath = strings.TrimSpace(os.Getenv("HOSTD_CONFIG_PATH"))
	}
	if configPath == "" {
		configPath = defaultConfigPath
	}
	statePath := strings.TrimSpace(options.StatePath)
	if statePath == "" {
		statePath = strings.TrimSpace(os.Getenv("HOSTD_STATE_PATH"))
	}
	if statePath == "" {
		statePath = defaultStatePath
	}
	return configPath, statePath, nil
}

func ResolveControlSocketPath(options Options) (string, error) {
	controlSocketPath := strings.TrimSpace(options.ControlSocketPath)
	if controlSocketPath != "" {
		return controlSocketPath, nil
	}
	if raw := strings.TrimSpace(os.Getenv("HOSTD_CONTROL_SOCKET_PATH")); raw != "" {
		return raw, nil
	}
	_, statePath, err := ResolvePaths(options)
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "windows":
		return `\\.\pipe\hostd-control`, nil
	default:
		return filepath.Join(filepath.Dir(statePath), "control.sock"), nil
	}
}

func ParseInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

func ParseBool(value string) (bool, error) {
	raw := strings.TrimSpace(strings.ToLower(value))
	switch raw {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool: %s", value)
	}
}

func defaultConfig() Config {
	displayName, _ := os.Hostname()
	return Config{
		Gateway: GatewayConfig{
			TLSMode: "system",
		},
		DisplayName:      displayName,
		HeartbeatSeconds: 20,
		Components: ComponentsConfig{
			Host: HostComponentConfig{
				Enabled: true,
				Methods: append([]string(nil), hostMethods...),
			},
		},
		Logging: LoggingConfig{
			Level: "info",
		},
	}
}

func applyFile(path string, cfg *Config) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var fileCfg fileConfig
	if err := json.Unmarshal(content, &fileCfg); err != nil {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}
	if fileCfg.Gateway != nil {
		if fileCfg.Gateway.WSURL != "" {
			cfg.Gateway.WSURL = fileCfg.Gateway.WSURL
		}
		if fileCfg.Gateway.TLSMode != "" {
			cfg.Gateway.TLSMode = fileCfg.Gateway.TLSMode
		}
		if fileCfg.Gateway.TLSFingerprint != "" {
			cfg.Gateway.TLSFingerprint = fileCfg.Gateway.TLSFingerprint
		}
	}
	if fileCfg.DisplayName != "" {
		cfg.DisplayName = fileCfg.DisplayName
	}
	if fileCfg.HeartbeatSeconds > 0 {
		cfg.HeartbeatSeconds = fileCfg.HeartbeatSeconds
	}
	if fileCfg.Components != nil {
		cfg.Components.Host.Enabled = fileCfg.Components.Host.Enabled
		if len(fileCfg.Components.Host.Methods) > 0 {
			cfg.Components.Host.Methods = append([]string(nil), fileCfg.Components.Host.Methods...)
		}
	}
	if fileCfg.Logging != nil && fileCfg.Logging.Level != "" {
		cfg.Logging.Level = fileCfg.Logging.Level
	}
	return nil
}

func applyEnv(cfg *Config) error {
	if raw := strings.TrimSpace(os.Getenv("HOSTD_GATEWAY_WS_URL")); raw != "" {
		cfg.Gateway.WSURL = raw
	}
	if raw := strings.TrimSpace(os.Getenv("HOSTD_GATEWAY_TLS_MODE")); raw != "" {
		cfg.Gateway.TLSMode = raw
	}
	if raw := strings.TrimSpace(os.Getenv("HOSTD_GATEWAY_TLS_FINGERPRINT")); raw != "" {
		cfg.Gateway.TLSFingerprint = raw
	}
	if raw := strings.TrimSpace(os.Getenv("HOSTD_DISPLAY_NAME")); raw != "" {
		cfg.DisplayName = raw
	}
	if raw := strings.TrimSpace(os.Getenv("HOSTD_LOG_LEVEL")); raw != "" {
		cfg.Logging.Level = raw
	}
	if raw := strings.TrimSpace(os.Getenv("HOSTD_HEARTBEAT_SECONDS")); raw != "" {
		parsed, err := ParseInt(raw)
		if err != nil {
			return err
		}
		cfg.HeartbeatSeconds = parsed
	}
	if raw := strings.TrimSpace(os.Getenv("HOSTD_COMPONENTS_HOST_ENABLED")); raw != "" {
		parsed, err := ParseBool(raw)
		if err != nil {
			return err
		}
		cfg.Components.Host.Enabled = parsed
	}
	return nil
}

func applyOptions(options Options, cfg *Config) {
	if options.GatewayWSURL != nil {
		cfg.Gateway.WSURL = strings.TrimSpace(*options.GatewayWSURL)
	}
	if options.GatewayTLSMode != nil {
		cfg.Gateway.TLSMode = strings.TrimSpace(*options.GatewayTLSMode)
	}
	if options.GatewayTLSFingerprint != nil {
		cfg.Gateway.TLSFingerprint = strings.TrimSpace(*options.GatewayTLSFingerprint)
	}
	if options.DisplayName != nil {
		cfg.DisplayName = strings.TrimSpace(*options.DisplayName)
	}
	if options.LoggingLevel != nil {
		cfg.Logging.Level = strings.TrimSpace(*options.LoggingLevel)
	}
	if options.HeartbeatSeconds != nil {
		cfg.HeartbeatSeconds = *options.HeartbeatSeconds
	}
	if options.HostEnabled != nil {
		cfg.Components.Host.Enabled = *options.HostEnabled
	}
}

func normalize(cfg *Config) error {
	cfg.DisplayName = strings.TrimSpace(cfg.DisplayName)
	if cfg.DisplayName == "" {
		hostname, _ := os.Hostname()
		cfg.DisplayName = hostname
	}
	if cfg.HeartbeatSeconds <= 0 {
		cfg.HeartbeatSeconds = 20
	}
	mode := strings.TrimSpace(strings.ToLower(cfg.Gateway.TLSMode))
	if mode == "" {
		mode = "system"
	}
	switch mode {
	case "off", "system", "pinned":
	default:
		return fmt.Errorf("unsupported gateway.tls_mode: %s", cfg.Gateway.TLSMode)
	}
	cfg.Gateway.TLSMode = mode
	cfg.Logging.Level = strings.TrimSpace(strings.ToLower(cfg.Logging.Level))
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Components.Host.Enabled {
		cfg.Components.Host.Methods = append([]string(nil), hostMethods...)
	} else {
		cfg.Components.Host.Methods = nil
	}
	return nil
}

func ValidateForRun(cfg Config) error {
	wsURL := strings.TrimSpace(cfg.Gateway.WSURL)
	if wsURL == "" {
		return fmt.Errorf("gateway.ws_url is required")
	}
	parsed, err := url.Parse(wsURL)
	if err != nil {
		return fmt.Errorf("invalid gateway.ws_url: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
	case "ws", "wss":
	default:
		return fmt.Errorf("gateway.ws_url must use ws or wss scheme")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return fmt.Errorf("gateway.ws_url host is required")
	}
	if strings.TrimSpace(cfg.Gateway.TLSMode) == "pinned" && strings.TrimSpace(cfg.Gateway.TLSFingerprint) == "" {
		return fmt.Errorf("gateway.tls_fingerprint is required when gateway.tls_mode=pinned")
	}
	if !allowedLogLevels[cfg.Logging.Level] {
		return fmt.Errorf("unsupported logging.level: %s", cfg.Logging.Level)
	}
	return nil
}
