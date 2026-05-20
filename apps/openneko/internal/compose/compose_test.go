package compose

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func sampleFS() fstest.MapFS {
	return fstest.MapFS{
		"compose/core.yml":          {Data: []byte("services: { web: {} }\n")},
		"compose/dev.yml":           {Data: []byte("services: { dev: {} }\n")},
		"compose/demo.yml":          {Data: []byte("services: { demo: {} }\n")},
		"compose/plugins.linux.yml": {Data: []byte("services: { sandbox: {} }\n")},
	}
}

func TestMaterializeProd(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: dir, HasKVM: func() bool { return false }, GOOS: "darwin"}
	files, err := s.Materialize(ModeProd)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "core.yml" {
		t.Fatalf("unexpected: %v", files)
	}
}

func TestMaterializeDemo(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: dir, HasKVM: func() bool { return false }, GOOS: "darwin"}
	files, err := s.Materialize(ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected core + demo, got %v", files)
	}
	if filepath.Base(files[0]) != "core.yml" || filepath.Base(files[1]) != "demo.yml" {
		t.Fatalf("unexpected order: %v", files)
	}
}

func TestMaterializeLinuxOverlaysPlugins(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: dir, HasKVM: func() bool { return true }, GOOS: "linux"}
	files, err := s.Materialize(ModeProd)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, f := range files {
		got = append(got, filepath.Base(f))
	}
	want := []string{"core.yml", "plugins.linux.yml"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMaterializeLinuxNoKVMSkipsPlugins(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: dir, HasKVM: func() bool { return false }, GOOS: "linux"}
	files, err := s.Materialize(ModeProd)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(filepath.Base(f), "plugins") {
			t.Fatalf("plugins overlay should not be included without KVM: %v", files)
		}
	}
}

func TestMaterializeUnknownMode(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: dir, HasKVM: func() bool { return false }, GOOS: "linux"}
	if _, err := s.Materialize(Mode("bogus")); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}
