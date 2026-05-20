package config

import (
	"path/filepath"
	"testing"
)

func TestDirOverride(t *testing.T) {
	if got := Dir("/tmp/foo"); got != "/tmp/foo" {
		t.Fatalf("override should win, got %q", got)
	}
}

func TestDirXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/var/cfg")
	if got := Dir(""); got != filepath.Join("/var/cfg", "openneko") {
		t.Fatalf("expected XDG-derived path, got %q", got)
	}
}

func TestDirFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/test")
	if got := Dir(""); got != "/home/test/.config/openneko" {
		t.Fatalf("expected HOME-derived path, got %q", got)
	}
}

func TestFile(t *testing.T) {
	got := File("/etc/openneko", "secrets.json")
	if got != "/etc/openneko/secrets.json" {
		t.Fatalf("unexpected path: %q", got)
	}
}
