package assets

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type composeServiceForTest struct {
	Ports       []string                     `yaml:"ports"`
	Expose      []string                     `yaml:"expose"`
	Environment map[string]string            `yaml:"environment"`
	DependsOn   map[string]composeDepForTest `yaml:"depends_on"`
	Volumes     []string                     `yaml:"volumes"`
}

func TestPackagedComposeHasNoBackupService(t *testing.T) {
	core, raw := readComposeForTest(t, "core.yml")
	for _, retired := range []string{"OPENNEKO_BACKUP", "PGBACKREST", "pgbackrest", "archive_command"} {
		if strings.Contains(raw, retired) {
			t.Fatalf("packaged Compose still references retired backup setting %q", retired)
		}
	}
	for _, name := range []string{"records-db", "neko-backup", "neko-restore", "records-graphjin", "records-watch-graphjin"} {
		if _, exists := core.Services[name]; exists {
			t.Fatalf("packaged Compose still defines retired service %s", name)
		}
	}
}

type composeDepForTest struct {
	Condition string `yaml:"condition"`
}

type composeFileForTest struct {
	Services map[string]composeServiceForTest `yaml:"services"`
}

func readComposeForTest(t *testing.T, name string) (composeFileForTest, string) {
	t.Helper()
	raw, err := fs.ReadFile(ComposeFS, "compose/"+name)
	if err != nil {
		t.Fatal(err)
	}
	var doc composeFileForTest
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return doc, string(raw)
}

// repoRootForTest walks up from the test's working directory until it finds the
// directory holding the top-level compose.yml.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "compose.yml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root: no compose.yml found walking up from the test directory")
		}
		dir = parent
	}
}

// TestDevComposeExposesOnlyWebToHost guards the top-level (development) Compose
// files that run on the Linux host. web is the only service allowed to publish
// on all interfaces (0.0.0.0); every other host-published port must stay bound
// to the 127.0.0.1 loopback, so only web is reachable from the LAN. Globbing
// compose*.yml means any new overlay is covered automatically.
func TestDevComposeExposesOnlyWebToHost(t *testing.T) {
	const webPublish = "${OPENNEKO_WEB_BIND_ADDRESS:-0.0.0.0}:${OPENNEKO_PORT:-3000}:8080"

	files, err := filepath.Glob(filepath.Join(repoRootForTest(t), "compose*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no top-level compose*.yml files found")
	}

	sawWeb := false
	for _, path := range files {
		name := filepath.Base(path)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc composeFileForTest
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for service, spec := range doc.Services {
			for _, entry := range spec.Ports {
				if service == "web" {
					if entry != webPublish {
						t.Fatalf("%s: web publishes %q, want only %q", name, entry, webPublish)
					}
					sawWeb = true
					continue
				}
				if !strings.HasPrefix(entry, "127.0.0.1:") {
					t.Fatalf("%s: service %q publishes host port %q on a non-loopback interface; only web may bind the LAN", name, service, entry)
				}
			}
		}
	}
	if !sawWeb {
		t.Fatal("expected the web service to publish its host port in the top-level compose files")
	}
}

func TestPackagedComposeExposesOnlyWebToLAN(t *testing.T) {
	core, _ := readComposeForTest(t, "core.yml")
	for name, service := range core.Services {
		if name == "web" {
			want := "${OPENNEKO_WEB_BIND_ADDRESS:-0.0.0.0}:${OPENNEKO_PORT:-3000}:8080"
			if len(service.Ports) != 1 || service.Ports[0] != want {
				t.Fatalf("web ports = %v, want only %q", service.Ports, want)
			}
			continue
		}
		if len(service.Ports) != 0 {
			t.Fatalf("packaged service %s unexpectedly publishes host ports: %v", name, service.Ports)
		}
	}

	demo, _ := readComposeForTest(t, "demo.yml")
	for name, service := range demo.Services {
		if len(service.Ports) != 0 {
			t.Fatalf("demo.yml service %s unexpectedly publishes host ports: %v", name, service.Ports)
		}
	}
}

func TestPackagedComposeKeepsRuntimeTrafficOnDockerNetwork(t *testing.T) {
	core, _ := readComposeForTest(t, "core.yml")
	for service, port := range map[string]string{
		"neko-db":        "5432",
		"openneko-serve": "8080",
		"neko-graphjin":  "8089",
	} {
		got := core.Services[service].Expose
		if len(got) != 1 || got[0] != port {
			t.Fatalf("%s expose = %v, want internal port %s", service, got, port)
		}
	}

	migrate := core.Services["neko-migrate"]
	if migrate.Environment["NEKO_PG_HOST"] != "neko-db" {
		t.Fatalf("migration host = %q, want Docker service neko-db", migrate.Environment["NEKO_PG_HOST"])
	}
	if core.Services["web"].DependsOn["worker"].Condition != "service_healthy" {
		t.Fatal("web must start after the worker control plane is healthy")
	}
}

func TestServeHoldsNoControlZoneSecrets(t *testing.T) {
	root := repoRootForTest(t)
	type service struct {
		Environment map[string]any `yaml:"environment"`
		EnvFile     any            `yaml:"env_file"`
		Volumes     []string       `yaml:"volumes"`
	}
	forbidden := func(name string) bool {
		upper := strings.ToUpper(name)
		switch {
		case upper == "NEKO_PG_PASSWORD", upper == "NEKO_SECRET_KEY", upper == "NEKO_DATABASE_URL", upper == "DATABASE_URL":
			return true
		case strings.Contains(upper, "PASSWORD"), strings.HasSuffix(upper, "_API_KEY"), strings.Contains(upper, "SECRET"):
			return true
		}
		for _, provider := range []string{"OPENAI", "ANTHROPIC", "GEMINI", "GOOGLE", "VERTEX", "AWS", "BEDROCK", "AZURE", "MISTRAL", "GROQ", "COHERE", "DEEPSEEK", "XAI", "OPENROUTER", "TOGETHER"} {
			if strings.HasPrefix(upper, provider+"_") {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"compose.yml", "apps/openneko/assets/compose/core.yml", "recipes/sandbox/compose.yml"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Services map[string]service `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		serve, ok := doc.Services["openneko-serve"]
		if !ok {
			t.Fatalf("%s has no openneko-serve service", name)
		}
		if serve.EnvFile != nil {
			t.Fatalf("%s: openneko-serve reads an env_file", name)
		}
		for key, value := range serve.Environment {
			if forbidden(key) {
				t.Fatalf("%s: openneko-serve names %s", name, key)
			}
			if key == "NEKO_PG_USER" && value != nil && value != "neko_files" {
				t.Fatalf("%s: openneko-serve logs in as %v", name, value)
			}
		}
		for _, mount := range serve.Volumes {
			source := strings.SplitN(mount, ":", 2)[0]
			if strings.Contains(source, "openneko-config") || strings.Contains(source, "OPENNEKO_CONFIG_VOLUME") {
				t.Fatalf("%s: openneko-serve mounts the config volume (%s), which holds the secret-key and the database password", name, mount)
			}
		}
		if name != "recipes/sandbox/compose.yml" {
			if got := serve.Environment["OPENNEKO_SERVE_CREDENTIALS"]; got != "/credentials" {
				t.Fatalf("%s: openneko-serve OPENNEKO_SERVE_CREDENTIALS = %v", name, got)
			}
			if _, ok := doc.Services["openneko-serve-init"]; !ok {
				t.Fatalf("%s has no openneko-serve-init step", name)
			}
		}
	}
}
