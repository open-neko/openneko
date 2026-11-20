package compose

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/assets"
)

const oldOverride = `x-sandbox: &sandbox
  OPENNEKO_SANDBOX_URL: unix:///run/openneko-sandbox/sandbox.sock
services:
  web:
    environment: *sandbox
    mem_limit: 1g
  worker:
    environment:
      NODE_OPTIONS: --max-old-space-size=2048
    mem_limit: 2g
  librarian:
    environment:
      LIBRARIAN_CONCURRENCY: "2"
  embedding:
    mem_limit: 1g
  neko-backup:
    environment:
      BACKUP_RETENTION_DAYS: "14"
  neko-db:
    command: !reset []
volumes:
  extra:
`

func writeOverride(t *testing.T, body string) string {
	t.Helper()
	isolateConfig(t)
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "openneko")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "compose.override.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOverrideOfRemovedServicesIsIgnoredWithOneLine(t *testing.T) {
	path := writeOverride(t, oldOverride)
	var notice bytes.Buffer
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: t.TempDir(), Notice: &notice}
	for range 2 {
		files, err := s.Materialize(ModeDemo)
		if err != nil {
			t.Fatal(err)
		}
		used := files[len(files)-1]
		if used == path {
			t.Fatalf("the stack uses the operator's file %s with entries for removed services", used)
		}
		raw, err := os.ReadFile(used)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			XSandbox map[string]string         `yaml:"x-sandbox"`
			Services map[string]map[string]any `yaml:"services"`
			Volumes  map[string]any            `yaml:"volumes"`
		}
		if err := yaml.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%v\n%s", err, raw)
		}
		for _, name := range []string{"worker", "librarian", "embedding", "neko-backup"} {
			if _, ok := got.Services[name]; ok {
				t.Fatalf("the copy keeps %s:\n%s", name, raw)
			}
		}
		web := got.Services["web"]
		if env, _ := web["environment"].(map[string]any); env["OPENNEKO_SANDBOX_URL"] == nil || web["mem_limit"] != "1g" {
			t.Fatalf("the copy lost the web entry: %v\n%s", web, raw)
		}
		if _, ok := got.Services["neko-db"]; !ok || !strings.Contains(string(raw), "!reset") {
			t.Fatalf("the copy lost the neko-db entry or its tag:\n%s", raw)
		}
		if _, ok := got.Volumes["extra"]; !ok {
			t.Fatalf("the copy lost the volumes:\n%s", raw)
		}
		if info, err := os.Stat(used); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("copy mode %v, %v", info.Mode().Perm(), err)
		}
	}
	if left, _ := os.ReadFile(path); string(left) != oldOverride {
		t.Fatalf("the operator's file changed:\n%s", left)
	}
	lines := strings.Split(strings.TrimSpace(notice.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], path) || !strings.Contains(lines[0], "(embedding, librarian, neko-backup, worker)") {
		t.Fatalf("want one line that names the file and the four services, got %q", notice.String())
	}
}

func TestOverrideWithoutRemovedServicesIsUsedAsItIs(t *testing.T) {
	path := writeOverride(t, "services:\n  web:\n    mem_limit: 1g\n")
	var notice bytes.Buffer
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: t.TempDir(), Notice: &notice}
	files, err := s.Materialize(ModeProd)
	if err != nil {
		t.Fatal(err)
	}
	if files[len(files)-1] != path || notice.Len() != 0 {
		t.Fatalf("files %v notice %q", files, notice.String())
	}
}

func TestOverrideAnchorInARemovedServiceStopsWithItsNames(t *testing.T) {
	writeOverride(t, "services:\n  worker:\n    environment: &shared\n      A: b\n  web:\n    environment: *shared\n")
	s := &Supervisor{AssetsFS: sampleFS(), RuntimeDir: t.TempDir(), Notice: &bytes.Buffer{}}
	_, err := s.Materialize(ModeProd)
	if err == nil || !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), "compose.override.yml") {
		t.Fatalf("want an error that names worker and the file, got %v", err)
	}
}

func TestRemovedServicesAreNotInThisRelease(t *testing.T) {
	files, err := fs.Glob(assets.ComposeFS, "compose/*.yml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no compose files: %v", err)
	}
	for _, name := range files {
		raw, err := assets.ComposeFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		top := doc.Content[0]
		for i := 0; i+1 < len(top.Content); i += 2 {
			if top.Content[i].Value != "services" {
				continue
			}
			for j := 0; j < len(top.Content[i+1].Content); j += 2 {
				svc := top.Content[i+1].Content[j].Value
				for _, removed := range RemovedServices {
					if svc == removed {
						t.Fatalf("%s runs %s, which RemovedServices lists", name, svc)
					}
				}
			}
		}
	}
}
