package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"jarvisai/runtime/hostd/internal/protocol"
)

const (
	ComponentID           = "host.main"
	DefaultMaxReadBytes   = 8 * 1024 * 1024
	DefaultMaxOutputBytes = 16 * 1024
)

var DefaultExecTimeout = 30 * time.Second

var Methods = []string{
	"host.fs.stat",
	"host.fs.list",
	"host.fs.read",
	"host.fs.write",
	"host.fs.mkdir",
	"host.exec.run",
}

type Options struct {
	ComponentID        string
	RuntimeVersion     string
	MaxReadBytes       int
	MaxOutputBytes     int
	DefaultExecTimeout time.Duration
}

type Component struct {
	componentID        string
	runtimeVersion     string
	hostname           string
	maxReadBytes       int
	maxOutputBytes     int
	defaultExecTimeout time.Duration
}

type Error struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string {
	return e.Message
}

func NewComponent(options Options) (*Component, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	componentID := strings.TrimSpace(options.ComponentID)
	if componentID == "" {
		componentID = ComponentID
	}
	if options.MaxReadBytes <= 0 {
		options.MaxReadBytes = DefaultMaxReadBytes
	}
	if options.MaxOutputBytes <= 0 {
		options.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if options.DefaultExecTimeout <= 0 {
		options.DefaultExecTimeout = DefaultExecTimeout
	}
	return &Component{
		componentID:        componentID,
		runtimeVersion:     strings.TrimSpace(options.RuntimeVersion),
		hostname:           hostname,
		maxReadBytes:       options.MaxReadBytes,
		maxOutputBytes:     options.MaxOutputBytes,
		defaultExecTimeout: options.DefaultExecTimeout,
	}, nil
}

func (c *Component) Definition() protocol.RuntimeComponent {
	return protocol.RuntimeComponent{
		ComponentID: c.componentID,
		Kind:        "host",
		Subtype:     "local",
		Methods:     append([]string(nil), Methods...),
		Health:      c.Health(),
		Metadata:    c.Metadata(),
	}
}

func (c *Component) Health() protocol.Health {
	return protocol.Health{
		Status:    "healthy",
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Detail:    "",
	}
}

func (c *Component) Metadata() map[string]any {
	return map[string]any{
		"os":              runtime.GOOS,
		"arch":            runtime.GOARCH,
		"hostname":        c.hostname,
		"runtime_version": c.runtimeVersion,
	}
}

func (c *Component) Dispatch(method string, params map[string]any) (map[string]any, *Error) {
	if err := c.validateComponent(params); err != nil {
		return nil, err
	}
	switch method {
	case "host.fs.stat":
		return c.fsStat(params)
	case "host.fs.list":
		return c.fsList(params)
	case "host.fs.read":
		return c.fsRead(params)
	case "host.fs.write":
		return c.fsWrite(params)
	case "host.fs.mkdir":
		return c.fsMkdir(params)
	case "host.exec.run":
		return c.execRun(params)
	default:
		return nil, &Error{
			Code:    "METHOD_NOT_SUPPORTED",
			Message: fmt.Sprintf("unsupported method: %s", method),
		}
	}
}

func (c *Component) validateComponent(params map[string]any) *Error {
	componentID := strings.TrimSpace(stringValue(params["componentId"]))
	if componentID == "" {
		return &Error{
			Code:    "TARGET_COMPONENT_NOT_FOUND",
			Message: "componentId is required",
		}
	}
	if componentID != c.componentID {
		return &Error{
			Code:    "TARGET_COMPONENT_NOT_FOUND",
			Message: fmt.Sprintf("component %s is not registered on hostd", componentID),
		}
	}
	return nil
}

func (c *Component) fsStat(params map[string]any) (map[string]any, *Error) {
	path, err := resolvePath(stringValue(params["path"]))
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return map[string]any{
				"path":   path,
				"exists": false,
				"type":   nil,
				"size":   nil,
				"mtime":  nil,
			}, nil
		}
		return nil, fromPathError(path, statErr)
	}
	return map[string]any{
		"path":   path,
		"exists": true,
		"type":   fileType(info),
		"size":   info.Size(),
		"mtime":  info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

func (c *Component) fsList(params map[string]any) (map[string]any, *Error) {
	path, err := resolvePath(stringValue(params["path"]))
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return nil, fromPathError(path, statErr)
	}
	if !info.IsDir() {
		return nil, &Error{
			Code:    "NOT_A_DIRECTORY",
			Message: fmt.Sprintf("path is not a directory: %s", path),
		}
	}
	entries, readErr := os.ReadDir(path)
	if readErr != nil {
		return nil, fromPathError(path, readErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		entryPath := filepath.Join(path, entry.Name())
		result = append(result, map[string]any{
			"name":  entry.Name(),
			"path":  entryPath,
			"type":  fileType(info),
			"size":  info.Size(),
			"mtime": info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return map[string]any{
		"path":    path,
		"entries": result,
	}, nil
}

func (c *Component) fsRead(params map[string]any) (map[string]any, *Error) {
	path, err := resolvePath(stringValue(params["path"]))
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return nil, fromPathError(path, statErr)
	}
	if info.IsDir() {
		return nil, &Error{
			Code:    "NOT_A_FILE",
			Message: fmt.Sprintf("path is not a file: %s", path),
		}
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, fromPathError(path, readErr)
	}
	encoding := strings.TrimSpace(strings.ToLower(stringValue(params["encoding"])))
	if encoding == "" {
		encoding = "utf-8"
	}
	if encoding != "utf-8" && encoding != "base64" {
		return nil, &Error{Code: "INVALID_ENCODING", Message: fmt.Sprintf("unsupported encoding: %s", encoding)}
	}
	limit := intValue(params["maxBytes"])
	if limit <= 0 || limit > c.maxReadBytes {
		limit = c.maxReadBytes
	}
	clipped, truncated := truncateBytes(content, limit)
	var encoded string
	if encoding == "base64" {
		encoded = base64.StdEncoding.EncodeToString(clipped)
	} else {
		encoded = string(clipped)
	}
	return map[string]any{
		"path":      path,
		"encoding":  encoding,
		"content":   encoded,
		"size":      len(content),
		"mtime":     info.ModTime().UTC().Format(time.RFC3339),
		"truncated": truncated,
	}, nil
}

func (c *Component) fsWrite(params map[string]any) (map[string]any, *Error) {
	path, err := resolvePath(stringValue(params["path"]))
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	parentInfo, statErr := os.Stat(parent)
	if statErr != nil {
		return nil, &Error{
			Code:    "PARENT_NOT_FOUND",
			Message: fmt.Sprintf("parent directory not found: %s", parent),
		}
	}
	if !parentInfo.IsDir() {
		return nil, &Error{
			Code:    "NOT_A_DIRECTORY",
			Message: fmt.Sprintf("parent path is not a directory: %s", parent),
		}
	}
	if info, fileErr := os.Stat(path); fileErr == nil && info.IsDir() {
		return nil, &Error{
			Code:    "IS_A_DIRECTORY",
			Message: fmt.Sprintf("path is a directory: %s", path),
		}
	}
	encoding := strings.TrimSpace(strings.ToLower(stringValue(params["encoding"])))
	if encoding == "" {
		encoding = "utf-8"
	}
	var payload []byte
	switch encoding {
	case "utf-8":
		payload = []byte(stringValue(params["content"]))
	case "base64":
		decoded, decodeErr := base64.StdEncoding.DecodeString(stringValue(params["content"]))
		if decodeErr != nil {
			return nil, &Error{Code: "INVALID_BASE64", Message: "content is not valid base64"}
		}
		payload = decoded
	default:
		return nil, &Error{Code: "INVALID_ENCODING", Message: fmt.Sprintf("unsupported encoding: %s", encoding)}
	}
	if writeErr := os.WriteFile(path, payload, 0o644); writeErr != nil {
		return nil, &Error{
			Code:    "WRITE_FAILED",
			Message: writeErr.Error(),
		}
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return nil, &Error{
			Code:    "WRITE_FAILED",
			Message: statErr.Error(),
		}
	}
	return map[string]any{
		"path":         path,
		"bytesWritten": len(payload),
		"mtime":        info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

func (c *Component) fsMkdir(params map[string]any) (map[string]any, *Error) {
	path, err := resolvePath(stringValue(params["path"]))
	if err != nil {
		return nil, err
	}
	parents := boolValue(params["parents"], true)
	if info, statErr := os.Stat(path); statErr == nil {
		if !info.IsDir() {
			return nil, &Error{
				Code:    "PATH_EXISTS_AS_FILE",
				Message: fmt.Sprintf("path exists and is not a directory: %s", path),
			}
		}
		return map[string]any{
			"path":    path,
			"created": false,
			"mtime":   info.ModTime().UTC().Format(time.RFC3339),
		}, nil
	}
	var mkdirErr error
	if parents {
		mkdirErr = os.MkdirAll(path, 0o755)
	} else {
		mkdirErr = os.Mkdir(path, 0o755)
	}
	if mkdirErr != nil {
		return nil, &Error{
			Code:    "MKDIR_FAILED",
			Message: mkdirErr.Error(),
		}
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return nil, &Error{
			Code:    "MKDIR_FAILED",
			Message: statErr.Error(),
		}
	}
	return map[string]any{
		"path":    path,
		"created": true,
		"mtime":   info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

func (c *Component) execRun(params map[string]any) (map[string]any, *Error) {
	rawArgv, ok := params["argv"].([]any)
	if !ok || len(rawArgv) == 0 {
		if argv, castOK := params["argv"].([]string); castOK {
			rawArgv = make([]any, 0, len(argv))
			for _, item := range argv {
				rawArgv = append(rawArgv, item)
			}
		}
	}
	argv := make([]string, 0, len(rawArgv))
	for _, item := range rawArgv {
		value := strings.TrimSpace(stringValue(item))
		if value != "" {
			argv = append(argv, value)
		}
	}
	if len(argv) == 0 {
		return nil, &Error{Code: "INVALID_ARGV", Message: "argv is required"}
	}
	timeout := time.Duration(intValue(params["timeoutSeconds"])) * time.Second
	if timeout <= 0 {
		timeout = c.defaultExecTimeout
	}
	cwd := strings.TrimSpace(stringValue(params["cwd"]))
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, &Error{Code: "EXEC_FAILED", Message: err.Error()}
		}
		cwd = wd
	}
	cwd, err := resolvePath(cwd)
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(cwd)
	if statErr != nil {
		return nil, fromPathError(cwd, statErr)
	}
	if !info.IsDir() {
		return nil, &Error{Code: "NOT_A_DIRECTORY", Message: fmt.Sprintf("cwd is not a directory: %s", cwd)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = cwd
	command.Env = append(os.Environ(), flattenEnvMap(params["env"])...)
	var stdoutBuffer bytes.Buffer
	var stderrBuffer bytes.Buffer
	command.Stdout = &stdoutBuffer
	command.Stderr = &stderrBuffer
	execErr := command.Run()
	exitCode := 0
	if execErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &Error{Code: "EXEC_TIMEOUT", Message: fmt.Sprintf("command timed out after %ds", int(timeout.Seconds()))}
		}
		if errors.Is(execErr, exec.ErrNotFound) {
			return nil, &Error{Code: "COMMAND_NOT_FOUND", Message: execErr.Error()}
		}
		var notFound *exec.Error
		if errors.As(execErr, &notFound) {
			return nil, &Error{Code: "COMMAND_NOT_FOUND", Message: execErr.Error()}
		}
		var exitErr *exec.ExitError
		if errors.As(execErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, &Error{Code: "EXEC_FAILED", Message: execErr.Error()}
		}
	}
	stdoutText, stdoutTruncated := truncateText(stdoutBuffer.Bytes(), c.maxOutputBytes)
	stderrText, stderrTruncated := truncateText(stderrBuffer.Bytes(), c.maxOutputBytes)
	return map[string]any{
		"ok":        exitCode == 0,
		"exitCode":  exitCode,
		"stdout":    stdoutText,
		"stderr":    stderrText,
		"truncated": stdoutTruncated || stderrTruncated,
	}, nil
}

func resolvePath(value string) (string, *Error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", &Error{Code: "INVALID_PATH", Message: "path is required"}
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", &Error{Code: "INVALID_PATH", Message: err.Error()}
	}
	return filepath.Clean(absolute), nil
}

func fileType(info os.FileInfo) string {
	switch {
	case info.IsDir():
		return "dir"
	case info.Mode().IsRegular():
		return "file"
	default:
		return "other"
	}
}

func fromPathError(path string, err error) *Error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &Error{Code: "PATH_NOT_FOUND", Message: fmt.Sprintf("path not found: %s", path)}
	case errors.Is(err, os.ErrPermission):
		return &Error{Code: "PERMISSION_DENIED", Message: fmt.Sprintf("permission denied: %s", path)}
	default:
		return &Error{Code: "PATH_ERROR", Message: err.Error()}
	}
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	switch item := value.(type) {
	case string:
		return item
	case []byte:
		return string(item)
	default:
		return fmt.Sprintf("%v", item)
	}
}

func intValue(value any) int {
	switch item := value.(type) {
	case int:
		return item
	case int64:
		return int(item)
	case float64:
		return int(item)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(item))
		return parsed
	default:
		return 0
	}
}

func boolValue(value any, defaultValue bool) bool {
	switch item := value.(type) {
	case bool:
		return item
	case string:
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return defaultValue
}

func flattenEnvMap(value any) []string {
	raw, ok := value.(map[string]any)
	if !ok {
		if typed, typedOK := value.(map[string]string); typedOK {
			output := make([]string, 0, len(typed))
			for key, item := range typed {
				output = append(output, fmt.Sprintf("%s=%s", key, item))
			}
			return output
		}
		return nil
	}
	output := make([]string, 0, len(raw))
	for key, item := range raw {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		output = append(output, fmt.Sprintf("%s=%s", key, stringValue(item)))
	}
	sort.Strings(output)
	return output
}

func truncateBytes(value []byte, limit int) ([]byte, bool) {
	if limit <= 0 || len(value) <= limit {
		return value, false
	}
	return value[:limit], true
}

func truncateText(value []byte, limit int) (string, bool) {
	clipped, truncated := truncateBytes(value, limit)
	return string(clipped), truncated
}
