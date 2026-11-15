package webfront

import (
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/assets"
)

var dockerEnvPair = regexp.MustCompile(`([A-Z_][A-Z0-9_]*)=("[^"]*"|\S+)`)

func webImageEnv(t *testing.T, root string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	stage := string(b)[strings.Index(string(b), " AS web\n"):]
	stage = stage[:strings.Index(stage, "\nFROM ")]
	env := map[string]string{}
	inEnv := false
	for _, line := range strings.Split(stage, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ENV ") {
			inEnv = true
			trimmed = strings.TrimPrefix(trimmed, "ENV ")
		}
		if !inEnv {
			continue
		}
		for _, m := range dockerEnvPair.FindAllStringSubmatch(trimmed, -1) {
			env[m[1]] = strings.Trim(m[2], `"`)
		}
		inEnv = strings.HasSuffix(trimmed, `\`)
	}
	return env
}

func composeWebEnv(t *testing.T, name string, raw []byte) map[string]string {
	t.Helper()
	var doc struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return doc.Services["web"].Environment
}

func TestTheWebImageRunsInProductionWithSecureCookies(t *testing.T) {
	root := "../../../.."
	envs := map[string]map[string]string{"Dockerfile web stage": webImageEnv(t, root)}
	if envs["Dockerfile web stage"]["OPENNEKO_ENV"] != "production" {
		t.Fatalf("the web image does not set OPENNEKO_ENV=production: %v", envs["Dockerfile web stage"])
	}
	for _, name := range []string{"compose.yml", "compose.dev.yml", "compose.adventureworks.yml", "compose.db.yml"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		envs[name] = composeWebEnv(t, name, raw)
	}
	packaged, err := fs.Glob(assets.ComposeFS, "compose/*.yml")
	if err != nil || len(packaged) == 0 {
		t.Fatalf("packaged compose files: %v %v", packaged, err)
	}
	for _, name := range packaged {
		raw, err := fs.ReadFile(assets.ComposeFS, name)
		if err != nil {
			t.Fatal(err)
		}
		envs[name] = composeWebEnv(t, name, raw)
	}
	for name, composeEnv := range envs {
		if name == "Dockerfile web stage" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENNEKO_ENV", "")
			t.Setenv("NODE_ENV", "")
			for k, v := range envs["Dockerfile web stage"] {
				t.Setenv(k, v)
			}
			for k, v := range composeEnv {
				t.Setenv(k, v)
			}
			t.Setenv("OPENNEKO_PUBLIC_URL", "https://openneko.example.com")
			if Development() {
				t.Fatalf("openneko web runs in development mode with the env of %s", name)
			}
			secure, _ := CookiePolicy()
			api := &API{Gate: &Gate{SessionSecret: func() (string, error) { return strings.Repeat("s", 32), nil }}, SecureCookies: secure}
			rec := httptest.NewRecorder()
			if err := api.writeSession(rec, "admin"); err != nil {
				t.Fatal(err)
			}
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != SessionCookie || !cookies[0].Secure {
				t.Fatalf("session cookie is not Secure: %v", rec.Header().Values("Set-Cookie"))
			}
		})
	}
}

func TestDevelopmentModeComesFromOpennekoEnvFirst(t *testing.T) {
	for _, c := range []struct {
		opennekoEnv, nodeEnv string
		development          bool
	}{
		{"", "", false},
		{"", "production", false},
		{"", "test", false},
		{"", "development", true},
		{"development", "", true},
		{"development", "production", true},
		{"production", "development", false},
		{"staging", "development", false},
	} {
		t.Setenv("OPENNEKO_ENV", c.opennekoEnv)
		t.Setenv("NODE_ENV", c.nodeEnv)
		if got := Development(); got != c.development {
			t.Errorf("OPENNEKO_ENV=%q NODE_ENV=%q: development %v, want %v", c.opennekoEnv, c.nodeEnv, got, c.development)
		}
	}
}

func TestTheDevLoopRunsWebInDevelopment(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("../../../..", "scripts", "dev-web.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\nexport OPENNEKO_ENV=development\n") {
		t.Fatal("scripts/dev-web.sh does not run openneko web with OPENNEKO_ENV=development")
	}
}
