package buildinfo

import (
	"runtime"
	"strings"
)

var Version = "0.1.0"
var Commit = "dev"
var BuildDate = ""

func RuntimeVersion() string {
	version := strings.TrimSpace(Version)
	if version == "" {
		return "dev"
	}
	return version
}

func BuildCommit() string {
	commit := strings.TrimSpace(Commit)
	if commit == "" {
		return "dev"
	}
	return commit
}

func Info() map[string]any {
	return map[string]any{
		"version":    RuntimeVersion(),
		"commit":     BuildCommit(),
		"build_date": strings.TrimSpace(BuildDate),
		"go_version": runtime.Version(),
	}
}
