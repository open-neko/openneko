package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LocalPg mirrors the `pg` block of ~/.config/openneko/config.json, written
// by the web /setup wizard (see packages/db/src/local-config.ts). On a fresh
// install the file doesn't exist and every consumer falls back to its env-
// var defaults; once /setup runs the rotated password lands here and every
// reader picks it up from this single source.
type LocalPg struct {
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Database string `json:"database,omitempty"`
	SSLMode  string `json:"sslmode,omitempty"`
}

type Local struct {
	Pg *LocalPg `json:"pg,omitempty"`
}

// ReadLocal returns the local config, plus the path it was read from (empty
// if no file was found). A missing or malformed file is not an error — the
// caller layers what it finds over its own defaults.
func ReadLocal(override string) (Local, string) {
	candidates := []string{filepath.Join(Dir(override), "config.json")}
	// Pre-rebrand fallback path; matches the TS reader.
	if override == "" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				base = filepath.Join(home, ".config")
			}
		}
		if base != "" {
			candidates = append(candidates, filepath.Join(base, "neko", "config.json"))
		}
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var lc Local
		if err := json.Unmarshal(data, &lc); err != nil {
			continue
		}
		return lc, path
	}
	return Local{}, ""
}
