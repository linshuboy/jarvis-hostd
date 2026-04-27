package host

import (
	"os"
	"path/filepath"
	"testing"
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
