package launchd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const (
	DefaultLabel   = "ai.jarvis.hostd"
	ServiceManager = "launchd"
)

var currentGOOS = runtime.GOOS
var currentUID = os.Getuid
var pidPattern = regexp.MustCompile(`(?m)\bpid\s*=\s*(\d+)`)

type Status struct {
	Platform       string `json:"platform"`
	ServiceManager string `json:"service_manager"`
	Supported      bool   `json:"supported"`
	Label          string `json:"label"`
	PlistPath      string `json:"plist_path"`
	Installed      bool   `json:"installed"`
	Loaded         bool   `json:"loaded"`
	PID            int    `json:"pid,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

type InstallOptions struct {
	Label      string
	PlistPath  string
	BinaryPath string
	ConfigPath string
	StatePath  string
	WorkingDir string
	StdoutPath string
	StderrPath string
}

type CommandRunner interface {
	Run(context.Context, string, ...string) (string, string, error)
}

type DefaultCommandRunner struct{}

func (DefaultCommandRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

func DefaultInstallOptions(binaryPath string, configPath string, statePath string) (InstallOptions, error) {
	plistPath, stdoutPath, stderrPath, err := DefaultPaths(DefaultLabel)
	if err != nil {
		return InstallOptions{}, err
	}
	workingDir := filepath.Dir(statePath)
	return InstallOptions{
		Label:      DefaultLabel,
		PlistPath:  plistPath,
		BinaryPath: binaryPath,
		ConfigPath: configPath,
		StatePath:  statePath,
		WorkingDir: workingDir,
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
	}, nil
}

func DefaultPaths(label string) (string, string, string, error) {
	trimmedLabel := strings.TrimSpace(label)
	if trimmedLabel == "" {
		trimmedLabel = DefaultLabel
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", err
	}
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	logDir := filepath.Join(home, "Library", "Logs", "hostd")
	return filepath.Join(launchAgentsDir, trimmedLabel+".plist"), filepath.Join(logDir, "stdout.log"), filepath.Join(logDir, "stderr.log"), nil
}

func RenderPlist(options InstallOptions) (string, error) {
	label := strings.TrimSpace(options.Label)
	if label == "" {
		return "", fmt.Errorf("launchd label is required")
	}
	binaryPath := strings.TrimSpace(options.BinaryPath)
	configPath := strings.TrimSpace(options.ConfigPath)
	statePath := strings.TrimSpace(options.StatePath)
	if binaryPath == "" {
		return "", fmt.Errorf("hostd binary path is required")
	}
	if configPath == "" {
		return "", fmt.Errorf("hostd config path is required")
	}
	if statePath == "" {
		return "", fmt.Errorf("hostd state path is required")
	}
	workingDir := strings.TrimSpace(options.WorkingDir)
	if workingDir == "" {
		workingDir = filepath.Dir(statePath)
	}
	stdoutPath := strings.TrimSpace(options.StdoutPath)
	if stdoutPath == "" {
		_, defaultStdoutPath, defaultStderrPath, err := DefaultPaths(label)
		if err != nil {
			return "", err
		}
		stdoutPath = defaultStdoutPath
		if strings.TrimSpace(options.StderrPath) == "" {
			options.StderrPath = defaultStderrPath
		}
	}
	stderrPath := strings.TrimSpace(options.StderrPath)
	if stderrPath == "" {
		_, _, defaultStderrPath, err := DefaultPaths(label)
		if err != nil {
			return "", err
		}
		stderrPath = defaultStderrPath
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>run</string>
    <string>--config</string>
    <string>%s</string>
    <string>--state</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>WorkingDirectory</key>
  <string>%s</string>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
  <key>ProcessType</key>
  <string>Background</string>
</dict>
</plist>
`, xmlEscape(label), xmlEscape(binaryPath), xmlEscape(configPath), xmlEscape(statePath), xmlEscape(workingDir), xmlEscape(stdoutPath), xmlEscape(stderrPath)), nil
}

func Install(ctx context.Context, options InstallOptions, runner CommandRunner) (Status, error) {
	if !isSupported() {
		return unsupportedStatus(options), fmt.Errorf("launchd is only supported on darwin")
	}
	if runner == nil {
		runner = DefaultCommandRunner{}
	}
	normalized, err := normalizeInstallOptions(options)
	if err != nil {
		return Status{}, err
	}
	plist, err := RenderPlist(normalized)
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(normalized.PlistPath), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(normalized.StdoutPath), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(normalized.StderrPath), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(normalized.PlistPath, []byte(plist), 0o644); err != nil {
		return Status{}, err
	}
	_, _, _ = runner.Run(ctx, "launchctl", "bootout", domainTarget(), normalized.PlistPath)
	if _, stderr, err := runner.Run(ctx, "launchctl", "bootstrap", domainTarget(), normalized.PlistPath); err != nil {
		return Status{}, fmt.Errorf("launchctl bootstrap failed: %s", firstNonEmpty(stderr, err.Error()))
	}
	if _, stderr, err := runner.Run(ctx, "launchctl", "kickstart", "-k", serviceTarget(normalized.Label)); err != nil {
		return Status{}, fmt.Errorf("launchctl kickstart failed: %s", firstNonEmpty(stderr, err.Error()))
	}
	return StatusOf(ctx, normalized.Label, normalized.PlistPath, runner)
}

func Uninstall(ctx context.Context, label string, plistPath string, runner CommandRunner) (Status, error) {
	if !isSupported() {
		return unsupportedStatus(InstallOptions{Label: label, PlistPath: plistPath}), fmt.Errorf("launchd is only supported on darwin")
	}
	if runner == nil {
		runner = DefaultCommandRunner{}
	}
	normalized, err := normalizeInstallOptions(InstallOptions{Label: label, PlistPath: plistPath})
	if err != nil {
		return Status{}, err
	}
	_, _, _ = runner.Run(ctx, "launchctl", "bootout", domainTarget(), normalized.PlistPath)
	if err := os.Remove(normalized.PlistPath); err != nil && !os.IsNotExist(err) {
		return Status{}, err
	}
	status, statusErr := StatusOf(ctx, normalized.Label, normalized.PlistPath, runner)
	if statusErr != nil {
		return Status{}, statusErr
	}
	status.Installed = false
	status.Loaded = false
	status.PID = 0
	status.LastError = ""
	return status, nil
}

func StatusOf(ctx context.Context, label string, plistPath string, runner CommandRunner) (Status, error) {
	if runner == nil {
		runner = DefaultCommandRunner{}
	}
	normalized, err := normalizeInstallOptions(InstallOptions{Label: label, PlistPath: plistPath})
	if err != nil {
		return Status{}, err
	}
	if !isSupported() {
		return unsupportedStatus(normalized), nil
	}
	status := Status{
		Platform:       currentGOOS,
		ServiceManager: ServiceManager,
		Supported:      true,
		Label:          normalized.Label,
		PlistPath:      normalized.PlistPath,
	}
	if _, err := os.Stat(normalized.PlistPath); err == nil {
		status.Installed = true
	}
	stdout, stderr, err := runner.Run(ctx, "launchctl", "print", serviceTarget(normalized.Label))
	if err != nil {
		status.LastError = strings.TrimSpace(firstNonEmpty(stderr, stdout, err.Error()))
		return status, nil
	}
	status.Loaded = true
	if matches := pidPattern.FindStringSubmatch(stdout); len(matches) == 2 {
		if pidValue, parseErr := strconv.Atoi(matches[1]); parseErr == nil {
			status.PID = pidValue
		}
	}
	return status, nil
}

func normalizeInstallOptions(options InstallOptions) (InstallOptions, error) {
	label := strings.TrimSpace(options.Label)
	if label == "" {
		label = DefaultLabel
	}
	plistPath := strings.TrimSpace(options.PlistPath)
	stdoutPath := strings.TrimSpace(options.StdoutPath)
	stderrPath := strings.TrimSpace(options.StderrPath)
	if plistPath == "" || stdoutPath == "" || stderrPath == "" {
		defaultPlistPath, defaultStdoutPath, defaultStderrPath, err := DefaultPaths(label)
		if err != nil {
			return InstallOptions{}, err
		}
		if plistPath == "" {
			plistPath = defaultPlistPath
		}
		if stdoutPath == "" {
			stdoutPath = defaultStdoutPath
		}
		if stderrPath == "" {
			stderrPath = defaultStderrPath
		}
	}
	return InstallOptions{
		Label:      label,
		PlistPath:  plistPath,
		BinaryPath: strings.TrimSpace(options.BinaryPath),
		ConfigPath: strings.TrimSpace(options.ConfigPath),
		StatePath:  strings.TrimSpace(options.StatePath),
		WorkingDir: strings.TrimSpace(options.WorkingDir),
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
	}, nil
}

func unsupportedStatus(options InstallOptions) Status {
	normalized, _ := normalizeInstallOptions(options)
	return Status{
		Platform:       currentGOOS,
		ServiceManager: ServiceManager,
		Supported:      false,
		Label:          normalized.Label,
		PlistPath:      normalized.PlistPath,
		LastError:      "launchd is only supported on darwin",
	}
}

func isSupported() bool {
	return currentGOOS == "darwin"
}

func domainTarget() string {
	return fmt.Sprintf("gui/%d", currentUID())
}

func serviceTarget(label string) string {
	return fmt.Sprintf("%s/%s", domainTarget(), strings.TrimSpace(label))
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
