package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmpty(t *testing.T) {
	m := Empty()
	if m.Schema != SchemaURL {
		t.Fatalf("bad schema: %s", m.Schema)
	}
	if len(m.Plugins) != 0 {
		t.Fatal("expected no plugins")
	}
}

func TestPathFor(t *testing.T) {
	t.Setenv(PathEnv, "")
	got := PathFor("/repo")
	want := filepath.Join("/repo", Filename)
	if got != want {
		t.Fatalf("want %s got %s", want, got)
	}
	t.Setenv(PathEnv, "/override/plugins.json")
	if PathFor("/repo") != "/override/plugins.json" {
		t.Fatal("env override should win")
	}
}

func TestReadMissing(t *testing.T) {
	dir := t.TempDir()
	m, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatal("expected nil manifest for missing file")
	}
}

func TestWriteRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(PathEnv, "")
	m := Empty()
	m = Upsert(m, Entry{Name: "p", Version: "1.0.0", Integrity: "sha512-x"})
	if err := Write(dir, m); err != nil {
		t.Fatal(err)
	}
	back, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back == nil || len(back.Plugins) != 1 || back.Plugins[0].Name != "p" {
		t.Fatalf("round-trip lost data: %+v", back)
	}
}

func TestUpsertReplaces(t *testing.T) {
	m := Empty()
	m = Upsert(m, Entry{Name: "p", Version: "1.0.0", Integrity: "x"})
	m = Upsert(m, Entry{Name: "p", Version: "2.0.0", Integrity: "y"})
	if len(m.Plugins) != 1 {
		t.Fatal("upsert should not duplicate")
	}
	if m.Plugins[0].Version != "2.0.0" {
		t.Fatalf("upsert should replace, got %s", m.Plugins[0].Version)
	}
}

func TestRemoveByName(t *testing.T) {
	m := Empty()
	m = Upsert(m, Entry{Name: "a", Version: "1.0.0", Integrity: "x"})
	m = Upsert(m, Entry{Name: "b", Version: "1.0.0", Integrity: "y"})
	m = RemoveByName(m, "a")
	if len(m.Plugins) != 1 || m.Plugins[0].Name != "b" {
		t.Fatalf("unexpected plugins: %+v", m.Plugins)
	}
}

func TestWriteCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	t.Setenv(PathEnv, "")
	if err := Write(dir, Empty()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, Filename)); err != nil {
		t.Fatal(err)
	}
}
