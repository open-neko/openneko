package config

import (
	"os"
	"path/filepath"
)

const AppDirName = "openneko"

func Dir(override string) string {
	if override != "" {
		return override
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, AppDirName)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", AppDirName)
}

func File(override, name string) string {
	return filepath.Join(Dir(override), name)
}
