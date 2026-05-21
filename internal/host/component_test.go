package host

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestComponentReadWriteAndMkdir(t *testing.T) {
	component, err := NewComponent(Options{ComponentID: ComponentID, RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "workspace")
	mkdirResult, mkdirErr := component.Dispatch("host.fs.mkdir", map[string]any{
		"componentId": ComponentID,
		"path":        targetDir,
		"parents":     true,
	})
	if mkdirErr != nil {
		t.Fatalf("mkdir: %v", mkdirErr)
	}
	if created := mkdirResult["created"]; created != true {
		t.Fatalf("unexpected created flag: %v", created)
	}
	targetFile := filepath.Join(targetDir, "demo.txt")
	writeResult, writeErr := component.Dispatch("host.fs.write", map[string]any{
		"componentId": ComponentID,
		"path":        targetFile,
		"encoding":    "utf-8",
		"content":     "hello hostd",
	})
	if writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	if bytesWritten := writeResult["bytesWritten"]; bytesWritten != 11 {
		t.Fatalf("unexpected bytesWritten: %v", bytesWritten)
	}
	readResult, readErr := component.Dispatch("host.fs.read", map[string]any{
		"componentId": ComponentID,
		"path":        targetFile,
		"encoding":    "utf-8",
	})
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if content := readResult["content"]; content != "hello hostd" {
		t.Fatalf("unexpected content: %v", content)
	}
	statResult, statErr := component.Dispatch("host.fs.stat", map[string]any{
		"componentId": ComponentID,
		"path":        targetFile,
	})
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if exists := statResult["exists"]; exists != true {
		t.Fatalf("unexpected exists flag: %v", exists)
	}
}

func TestComponentExecMissingCommandReturnsStructuredError(t *testing.T) {
	component, err := NewComponent(Options{ComponentID: ComponentID, RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	_, execErr := component.Dispatch("host.exec.run", map[string]any{
		"componentId": ComponentID,
		"argv":        []any{"hostd-command-does-not-exist"},
		"cwd":         t.TempDir(),
	})
	if execErr == nil {
		t.Fatalf("expected structured error")
	}
	if execErr.Code != "COMMAND_NOT_FOUND" {
		t.Fatalf("unexpected error code: %s", execErr.Code)
	}
}

func TestComponentWriteDoesNotCreateParentDirectory(t *testing.T) {
	component, err := NewComponent(Options{ComponentID: ComponentID, RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	targetFile := filepath.Join(t.TempDir(), "missing", "demo.txt")
	_, writeErr := component.Dispatch("host.fs.write", map[string]any{
		"componentId": ComponentID,
		"path":        targetFile,
		"encoding":    "utf-8",
		"content":     "demo",
	})
	if writeErr == nil {
		t.Fatalf("expected write error")
	}
	if writeErr.Code != "PARENT_NOT_FOUND" {
		t.Fatalf("unexpected error code: %s", writeErr.Code)
	}
	if _, err := os.Stat(filepath.Dir(targetFile)); !os.IsNotExist(err) {
		t.Fatalf("parent directory should not be created")
	}
}

func TestComponentDefinitionUsesDeclaredMethodsAndWorkspaceHints(t *testing.T) {
	component, err := NewComponent(Options{
		ComponentID:    ComponentID,
		RuntimeVersion: "0.1.0",
		Methods:        []string{"host.fs.read", "host.fs.list"},
		WorkspaceHints: []WorkspaceHint{
			{Name: "Repo", RootPath: "/workspace/repo"},
			{Name: "Home", RootPath: "/home/agi"},
		},
	})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	definition := component.Definition()
	if len(definition.Methods) != 2 || definition.Methods[0] != "host.fs.read" || definition.Methods[1] != "host.fs.list" {
		t.Fatalf("unexpected methods: %#v", definition.Methods)
	}
	rawHints, ok := definition.Metadata["workspace_hints"].([]map[string]string)
	if !ok {
		t.Fatalf("workspace hints should be metadata []map[string]string: %#v", definition.Metadata["workspace_hints"])
	}
	if len(rawHints) != 2 {
		t.Fatalf("unexpected workspace hints: %#v", rawHints)
	}
	if rawHints[0]["name"] != "Repo" || rawHints[0]["root_path"] != "/workspace/repo" {
		t.Fatalf("unexpected first workspace hint: %#v", rawHints[0])
	}
	if rawHints[1]["name"] != "Home" || rawHints[1]["root_path"] != "/home/agi" {
		t.Fatalf("unexpected second workspace hint: %#v", rawHints[1])
	}
}

func TestComponentOnlyDeclaresUpdateWhenConfigured(t *testing.T) {
	component, err := NewComponent(Options{ComponentID: ComponentID, RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	for _, method := range component.Definition().Methods {
		if method == HostUpdateMethod {
			t.Fatalf("update method should not be declared without update command")
		}
	}

	updateComponent, err := NewComponent(Options{
		ComponentID:    ComponentID,
		RuntimeVersion: "0.1.0",
		Update: UpdateOptions{
			Command: []string{"/bin/true"},
			Root:    t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("new update component: %v", err)
	}
	found := false
	for _, method := range updateComponent.Definition().Methods {
		if method == HostUpdateMethod {
			found = true
		}
	}
	if !found {
		t.Fatalf("update method should be declared when update command is configured")
	}
	if enabled := updateComponent.Definition().Metadata["update_enabled"]; enabled != true {
		t.Fatalf("expected update metadata: %#v", updateComponent.Definition().Metadata)
	}
}

func TestComponentApplyUpdateWritesManifestAndRunsCommand(t *testing.T) {
	if os.Getenv("HOSTD_UPDATE_TEST_HELPER") == "1" {
		os.Stdout.WriteString("tag=" + os.Getenv("AGI_UPDATE_RELEASE_TAG"))
		return
	}
	root := t.TempDir()
	t.Setenv("HOSTD_UPDATE_TEST_HELPER", "1")
	helperCommand := []string{os.Args[0], "-test.run", "TestComponentApplyUpdateWritesManifestAndRunsCommand"}
	component, err := NewComponent(Options{
		ComponentID:    ComponentID,
		RuntimeVersion: "0.1.0",
		Update: UpdateOptions{
			Command:        helperCommand,
			Root:           root,
			Download:       true,
			TimeoutSeconds: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	result, updateErr := component.Dispatch(HostUpdateMethod, map[string]any{
		"componentId":      ComponentID,
		"runtimeId":        "runtime-a",
		"release_tag":      "v1.2.3",
		"sourceRepository": "owner/repo",
		"sourceSha":        "abc123",
		"imageTags":        []any{"v1.2.3", "latest"},
		"services":         []any{"gateway"},
		"containerImages":  map[string]any{"gateway": "registry.example/gateway:v1.2.3"},
	})
	if updateErr != nil {
		t.Fatalf("apply update: %v", updateErr)
	}
	if result["ok"] != true {
		t.Fatalf("unexpected update result: %#v", result)
	}
	if !strings.Contains(stringValue(result["stdout"]), "tag=v1.2.3") {
		t.Fatalf("unexpected stdout: %#v", result["stdout"])
	}
	manifestPath := result["manifestPath"].(string)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest["runtime_id"] != "runtime-a" || manifest["release_tag"] != "v1.2.3" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
}

func TestComponentApplyUpdateRejectsPackageChecksumMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("package"))
	}))
	defer server.Close()

	component, err := NewComponent(Options{
		ComponentID:    ComponentID,
		RuntimeVersion: "0.1.0",
		Update: UpdateOptions{
			Command:        []string{"/bin/true"},
			Root:           t.TempDir(),
			Download:       true,
			TimeoutSeconds: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("new component: %v", err)
	}
	_, updateErr := component.Dispatch(HostUpdateMethod, map[string]any{
		"componentId":     ComponentID,
		"runtimeId":       "runtime-a",
		"packageUrl":      server.URL + "/hostd.tar.gz",
		"packageSha256":   "0000000000000000000000000000000000000000000000000000000000000000",
		"releaseTag":      "v1.2.3",
		"artifactBaseUrl": server.URL,
	})
	if updateErr == nil {
		t.Fatalf("expected checksum mismatch")
	}
	if updateErr.Code != "UPDATE_PACKAGE_CHECKSUM_MISMATCH" {
		t.Fatalf("unexpected error code: %s", updateErr.Code)
	}
}
