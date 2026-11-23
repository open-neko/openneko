package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// On Windows the runner starts inside its AppContainer, where the config folder is out of reach.
func TestAgentRunnerSkipsTheInstallSettings(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("APPDATA", cfg)
	t.Setenv("OPENNEKO_PROXIED", "")
	if err := os.MkdirAll(filepath.Join(cfg, "openneko", "installation.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := NewRoot()
	other, _, err := root.Find([]string{"version"})
	if err != nil {
		t.Fatal(err)
	}
	if root.PersistentPreRunE(other, nil) == nil {
		t.Fatal("control: a normal command read an unreadable installation.json without an error")
	}
	runner, _, err := root.Find([]string{agentRunnerCmd})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.PersistentPreRunE(runner, nil); err != nil {
		t.Fatalf("the agent runner read the install settings: %v", err)
	}
}
