package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/plugin/manifest"
)

func TestExitCodeFor(t *testing.T) {
	if code := ExitCodeFor(WithExit(3, errors.New("x"))); code != 3 {
		t.Fatalf("expected 3, got %d", code)
	}
	if code := ExitCodeFor(errors.New("plain")); code != 0 {
		t.Fatalf("expected 0 for non-wrapped err, got %d", code)
	}
}

func TestRootHasAllCommands(t *testing.T) {
	root := NewRoot()
	want := []string{"init", "install", "remove", "list", "doctor", "marketplace", "secrets", "version", "start", "stop", "status", "logs", "migrate", "seed", "reset"}
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
	if strings.TrimSpace(out.String()) == "" {
		t.Fatalf("expected version output, got empty")
	}
}

func TestListJSONOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir("/") })
	// Pre-populate a manifest with one entry.
	t.Setenv(manifest.PathEnv, "")
	m := manifest.Empty()
	m = manifest.Upsert(m, manifest.Entry{
		Name:        "plug",
		Version:     "1.0.0",
		Integrity:   "sha512-x",
		Permissions: manifest.Permissions{Network: []string{"api.example"}, Env: []manifest.EnvRequirement{}},
	})
	if err := manifest.Write(dir, m); err != nil {
		t.Fatal(err)
	}

	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--local", "list", "--output", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed []manifest.Entry
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("expected JSON: %v\n%s", err, out.String())
	}
	if len(parsed) != 1 || parsed[0].Name != "plug" {
		t.Fatalf("unexpected: %+v", parsed)
	}
}

func TestInitCreates(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir("/") })
	t.Setenv(manifest.PathEnv, "")

	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--local", "init"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "created") {
		t.Fatalf("expected 'created' in output: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, manifest.Filename)); err != nil {
		t.Fatalf("manifest not created: %v", err)
	}
}

func TestRemoveMissingPluginIsHarmless(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir("/") })
	t.Setenv(manifest.PathEnv, "")
	if err := manifest.Write(dir, manifest.Empty()); err != nil {
		t.Fatal(err)
	}
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--local", "remove", "no-such"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "was not in the manifest") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}
