package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/instance"
)

func TestExitCodeFor(t *testing.T) {
	if code := ExitCodeFor(WithExit(3, errors.New("x"))); code != 3 {
		t.Fatalf("expected 3, got %d", code)
	}
	if code := ExitCodeFor(errors.New("plain")); code != 0 {
		t.Fatalf("expected 0 for non-wrapped err, got %d", code)
	}
	if err := WithExit(0, nil); err != nil {
		t.Fatalf("a proxied command that exits 0 returns %v; main then exits 1", err)
	}
	if code := ExitCodeFor(WithExit(2, nil)); code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}

func TestRootHasAllCommands(t *testing.T) {
	root := NewRoot()
	want := []string{"setup", "instances", "install", "doctor", "secrets", "pack", "version", "start", "upgrade", "stop", "status", "backup", "restore", "storage", "logs", "migrate", "seed", "reset", "eval", "jobs"}
	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("expected subcommand %q to be registered", w)
		}
	}
}

func TestVersionSubcommand(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"OpenNeko ", "Commit SHA-1", "Go version", "Licensed under the Apache License 2.0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in version output:\n%s", want, got)
		}
	}
}

func TestVersionSubcommandShort(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version", "--short"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(out.String())
	if got == "" || strings.Contains(got, "\n") || strings.Contains(got, "OpenNeko") {
		t.Fatalf("expected short version output, got %q", out.String())
	}
}

func TestPersistentInstanceFlagWorksAfterSubcommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(instance.EnvName, "")
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version", "--short", "--instance", "acme"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := instance.Current(); got != "acme" {
		t.Fatalf("selected instance = %q", got)
	}
}

func TestNamedInstanceFromWebContainer(t *testing.T) {
	name, ok := namedInstanceFromContainer("openneko-acme-west-prod-web-1", "web")
	if !ok || name != "acme-west" {
		t.Fatalf("named web = %q, ok=%v", name, ok)
	}
	for _, container := range []string{
		"openneko-prod-web-1",
		"openneko-acme-prod-openneko-serve-1",
		"unrelated-web-1",
	} {
		if name, ok := namedInstanceFromContainer(container, "web"); ok || name != "" {
			t.Fatalf("%q parsed as named instance %q", container, name)
		}
	}
}

func TestRootDropsRemovedPluginCommands(t *testing.T) {
	root := NewRoot()
	for _, c := range root.Commands() {
		switch c.Name() {
		case "init", "list", "remove", "marketplace":
			t.Errorf("plugin command %q should be gone; integrations come from solution packs", c.Name())
		}
	}
}

func TestInstallRejectsNonGitSpec(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--local", "install", "@open-neko/plugin-slack"})
	err := root.Execute()
	if err == nil || ExitCodeFor(err) != 2 || !strings.Contains(err.Error(), "openneko pack install") {
		t.Fatalf("expected a usage error pointing at packs, got %v", err)
	}
}
