package compose

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func sampleFS() fstest.MapFS {
	return fstest.MapFS{
		"compose/core.yml":      {Data: []byte("services: { web: {} }\n")},
		"compose/dev.yml":       {Data: []byte("services: { dev: {} }\n")},
		"compose/demo.yml":      {Data: []byte("services: { demo: {} }\n")},
		"compose/openshell.yml": {Data: []byte("services: { openshell-gateway: {} }\n")},
	}
}

func TestMaterializeProd(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: dir, GOOS: "darwin"}
	files, err := s.Materialize(ModeProd)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, f := range files {
		got = append(got, filepath.Base(f))
	}
	// SEC9: OpenShell is the only runtime — its overlay always applies.
	want := []string{"core.yml", "openshell.yml"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMaterializeDemo(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: dir, GOOS: "darwin"}
	files, err := s.Materialize(ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, f := range files {
		got = append(got, filepath.Base(f))
	}
	want := []string{"core.yml", "demo.yml", "openshell.yml"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected order: %v", got)
	}
}

func TestMaterializeOpenShellAlwaysOn(t *testing.T) {
	dir := t.TempDir()
	for _, goos := range []string{"darwin", "linux"} {
		s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: dir, GOOS: goos}
		files, err := s.Materialize(ModeProd)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range files {
			if filepath.Base(f) == "openshell.yml" {
				found = true
			}
		}
		if !found {
			t.Fatalf("openshell overlay must always apply (SEC9): %v", files)
		}
	}
}

func TestMaterializeUnknownMode(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: dir, GOOS: "linux"}
	if _, err := s.Materialize(Mode("bogus")); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}
