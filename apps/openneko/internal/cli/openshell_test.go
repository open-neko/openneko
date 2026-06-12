package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentImageRef(t *testing.T) {
	if got := agentImageRef("", "v1.2.3"); got != "ghcr.io/open-neko/agent:v1.2.3" {
		t.Fatalf("default: got %q", got)
	}
	if got := agentImageRef("my.registry/agent:custom", "v1.2.3"); got != "my.registry/agent:custom" {
		t.Fatalf("override should win: got %q", got)
	}
}

func TestOpenShellStateDirOverride(t *testing.T) {
	// macOS with nothing set → under $HOME (OrbStack maps only $HOME into its VM).
	if got := openShellStateDirOverride("darwin", "/Users/x", ""); got != "/Users/x/.openneko/openshell" {
		t.Fatalf("darwin: got %q", got)
	}
	// Linux → keep the compose default (empty = no override).
	if got := openShellStateDirOverride("linux", "/home/x", ""); got != "" {
		t.Fatalf("linux should not override: got %q", got)
	}
	// An explicit existing value is always respected (no override).
	if got := openShellStateDirOverride("darwin", "/Users/x", "/custom/state"); got != "" {
		t.Fatalf("existing value must win: got %q", got)
	}
}

func TestConfigureOpenShellDBURL(t *testing.T) {
	// Operator-set URL always wins.
	t.Setenv("OPENSHELL_DB_URL", "postgres://op:set@elsewhere:5432/db")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configureOpenShellDBURL()
	if got := os.Getenv("OPENSHELL_DB_URL"); got != "postgres://op:set@elsewhere:5432/db" {
		t.Fatalf("operator value must win: got %q", got)
	}

	// No local config -> leave unset so the compose default applies.
	t.Setenv("OPENSHELL_DB_URL", "")
	configureOpenShellDBURL()
	if got := os.Getenv("OPENSHELL_DB_URL"); got != "" {
		t.Fatalf("fresh install should not set the URL: got %q", got)
	}

	// Rotated password in config.json -> URL derived from it, host pinned to
	// the compose network, special characters escaped.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "openneko"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"pg":{"user":"neko","password":"p@ss:w/rd","database":"neko"}}`
	if err := os.WriteFile(filepath.Join(dir, "openneko", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENSHELL_DB_URL", "")
	configureOpenShellDBURL()
	if got := os.Getenv("OPENSHELL_DB_URL"); got != "postgres://neko:p%40ss%3Aw%2Frd@neko-db:5432/neko" {
		t.Fatalf("derived URL wrong: got %q", got)
	}
}
