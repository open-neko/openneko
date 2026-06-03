package cli

import "testing"

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
