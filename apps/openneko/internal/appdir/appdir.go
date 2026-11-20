// Package appdir names the folders where OpenNeko keeps its config, data
// and state on each system.
package appdir

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config is $XDG_CONFIG_HOME/openneko, %APPDATA%\OpenNeko on Windows, or
// ~/.config/openneko.
func Config() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "openneko")
	}
	if dir, ok := windows("APPDATA"); ok {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "openneko")
}

// Data is $XDG_DATA_HOME/openneko, %LOCALAPPDATA%\OpenNeko on Windows, or
// ~/.local/share/openneko.
func Data() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "openneko")
	}
	if dir, ok := windows("LOCALAPPDATA"); ok {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "openneko")
}

// State is $XDG_STATE_HOME/openneko, %LOCALAPPDATA%\OpenNeko\state on
// Windows, or ~/.local/state/openneko.
func State() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
		return filepath.Abs(filepath.Join(xdg, "openneko"))
	}
	if dir, ok := windows("LOCALAPPDATA"); ok {
		return filepath.Join(dir, "state"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "openneko"), nil
}

func windows(env string) (string, bool) {
	if runtime.GOOS != "windows" {
		return "", false
	}
	base := os.Getenv(env)
	if base == "" {
		return "", false
	}
	return filepath.Join(base, "OpenNeko"), true
}
