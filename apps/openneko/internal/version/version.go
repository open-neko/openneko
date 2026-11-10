package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

var Version = "0.0.0-dev"
var Commit = "unknown"
var CommitTimestamp = "unknown"

const (
	DocumentationURL = "https://openneko.app"
	LicenseName      = "Apache License 2.0"
	Copyright        = "Copyright 2026 Amit Deshmukh and OpenNeko contributors"
)

func Detailed() string {
	return fmt.Sprintf(`OpenNeko %s
For documentation, visit %s

Commit SHA-1          : %s
Commit timestamp      : %s
Go version            : %s

Licensed under the %s
%s
`, Version, DocumentationURL, commit(), commitTimestamp(), runtime.Version(), LicenseName, Copyright)
}

func commit() string {
	if Commit != "" && Commit != "unknown" {
		return Commit
	}
	if value := buildSetting("vcs.revision"); value != "" {
		return value
	}
	return "unknown"
}

func commitTimestamp() string {
	if CommitTimestamp != "" && CommitTimestamp != "unknown" {
		return CommitTimestamp
	}
	if value := buildSetting("vcs.time"); value != "" {
		return value
	}
	return "unknown"
}

func buildSetting(key string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return ""
}
