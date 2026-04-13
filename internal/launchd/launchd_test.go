package launchd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeCommandRunner struct {
	calls    [][]string
	stdout   map[string]string
	stderr   map[string]string
	failures map[string]error
}

func (f *fakeCommandRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	_ = ctx
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	key := strings.Join(call, " ")
	if err, exists := f.failures[key]; exists {
		return f.stdout[key], f.stderr[key], err
	}
	return f.stdout[key], f.stderr[key], nil
}

func TestRenderPlistIncludesRuntimeCommand(t *testing.T) {
	plist, err := RenderPlist(InstallOptions{
		Label:      DefaultLabel,
		BinaryPath: "/Applications/JARVIS.app/Contents/MacOS/hostd",
		ConfigPath: "/Users/dev/Library/Application Support/hostd/config.json",
		StatePath:  "/Users/dev/Library/Application Support/hostd/state.json",
		WorkingDir: "/Users/dev/Library/Application Support/hostd",
		StdoutPath: "/Users/dev/Library/Logs/hostd/stdout.log",
		StderrPath: "/Users/dev/Library/Logs/hostd/stderr.log",
	})
	if err != nil {
		t.Fatalf("render plist: %v", err)
	}
	for _, expected := range []string{
		"<string>run</string>",
		"<string>--config</string>",
		"<string>/Applications/JARVIS.app/Contents/MacOS/hostd</string>",
		"<string>/Users/dev/Library/Application Support/hostd/config.json</string>",
		"<string>/Users/dev/Library/Application Support/hostd/state.json</string>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, expected) {
			t.Fatalf("plist missing %q:\n%s", expected, plist)
		}
	}
}

func TestStatusOfReturnsUnsupportedOutsideDarwin(t *testing.T) {
	previousGOOS := currentGOOS
	currentGOOS = "linux"
	t.Cleanup(func() {
		currentGOOS = previousGOOS
	})
	status, err := StatusOf(context.Background(), DefaultLabel, "", nil)
	if err != nil {
		t.Fatalf("status of unsupported platform: %v", err)
	}
	if status.Supported {
		t.Fatalf("expected unsupported status: %#v", status)
	}
}

func TestInstallWritesPlistAndBootstrapsLaunchAgent(t *testing.T) {
	previousGOOS := currentGOOS
	previousUID := currentUID
	currentGOOS = "darwin"
	currentUID = func() int { return 501 }
	t.Cleanup(func() {
		currentGOOS = previousGOOS
		currentUID = previousUID
	})

	tempDir := t.TempDir()
	options := InstallOptions{
		Label:      DefaultLabel,
		PlistPath:  filepath.Join(tempDir, DefaultLabel+".plist"),
		BinaryPath: "/Applications/JARVIS.app/Contents/MacOS/hostd",
		ConfigPath: filepath.Join(tempDir, "config.json"),
		StatePath:  filepath.Join(tempDir, "state.json"),
		WorkingDir: tempDir,
		StdoutPath: filepath.Join(tempDir, "logs", "stdout.log"),
		StderrPath: filepath.Join(tempDir, "logs", "stderr.log"),
	}
	runner := &fakeCommandRunner{
		stdout: map[string]string{
			"launchctl print gui/501/ai.jarvis.hostd": "pid = 4242\n",
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}

	status, err := Install(context.Background(), options, runner)
	if err != nil {
		t.Fatalf("install launchd service: %v", err)
	}
	if !status.Installed || !status.Loaded || status.PID != 4242 {
		t.Fatalf("unexpected install status: %#v", status)
	}
	content, readErr := os.ReadFile(options.PlistPath)
	if readErr != nil {
		t.Fatalf("read plist: %v", readErr)
	}
	if !strings.Contains(string(content), "<string>run</string>") {
		t.Fatalf("unexpected plist contents:\n%s", string(content))
	}
	if len(runner.calls) < 3 {
		t.Fatalf("expected launchctl commands to run, got %#v", runner.calls)
	}
}

func TestUninstallRemovesPlist(t *testing.T) {
	previousGOOS := currentGOOS
	previousUID := currentUID
	currentGOOS = "darwin"
	currentUID = func() int { return 501 }
	t.Cleanup(func() {
		currentGOOS = previousGOOS
		currentUID = previousUID
	})

	tempDir := t.TempDir()
	plistPath := filepath.Join(tempDir, DefaultLabel+".plist")
	if err := os.WriteFile(plistPath, []byte("plist"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	runner := &fakeCommandRunner{
		stdout:   map[string]string{},
		stderr:   map[string]string{},
		failures: map[string]error{"launchctl print gui/501/ai.jarvis.hostd": context.Canceled},
	}

	status, err := Uninstall(context.Background(), DefaultLabel, plistPath, runner)
	if err != nil {
		t.Fatalf("uninstall launchd service: %v", err)
	}
	if status.Installed || status.Loaded {
		t.Fatalf("unexpected uninstall status: %#v", status)
	}
	if _, statErr := os.Stat(plistPath); !os.IsNotExist(statErr) {
		t.Fatalf("plist should be removed, stat err=%v", statErr)
	}
}
