package assets

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var agentZoneServices = []string{"openneko-serve", "openneko-sandbox", "openneko-egress"}

var plainVolume = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

var controlZoneValue = regexp.MustCompile(`(?i)secrets\.json|secret-key|/config/openneko|postgres(ql)?://|\$\{?(NEKO_SECRET_KEY|NEKO_PG_PASSWORD|NEKO_DATABASE_URL|DATABASE_URL|OPENNEKO_DATABASE_URL\w*|\w*_API_KEY|HF_TOKEN|\w*ACCESS_TOKEN)\b`)

func controlZoneName(name string) bool {
	upper := strings.ToUpper(name)
	switch {
	case upper == "HF_TOKEN", strings.HasSuffix(upper, "ACCESS_TOKEN"), strings.Contains(upper, "DATABASE_URL"):
		return true
	case strings.Contains(upper, "PASSWORD"), strings.HasSuffix(upper, "_API_KEY"), strings.Contains(upper, "SECRET"):
		return true
	}
	for _, provider := range []string{"OPENAI", "ANTHROPIC", "GEMINI", "GOOGLE", "VERTEX", "AWS", "BEDROCK", "AZURE", "MISTRAL", "GROQ", "COHERE", "DEEPSEEK", "XAI", "OPENROUTER", "TOGETHER", "HUGGINGFACE", "PERPLEXITY", "NEKO_EMBEDDING"} {
		if strings.HasPrefix(upper, provider+"_") {
			return true
		}
	}
	return false
}

type zoneService struct {
	Environment any   `yaml:"environment"`
	EnvFile     any   `yaml:"env_file"`
	Volumes     []any `yaml:"volumes"`
	Secrets     []any `yaml:"secrets"`
	Configs     []any `yaml:"configs"`
}

func (s zoneService) env(t *testing.T, file, name string) map[string]string {
	out := map[string]string{}
	switch env := s.Environment.(type) {
	case nil:
	case map[string]any:
		for k, v := range env {
			if v != nil {
				out[k] = fmt.Sprint(v)
			} else {
				out[k] = ""
			}
		}
	case []any:
		for _, entry := range env {
			k, v, _ := strings.Cut(fmt.Sprint(entry), "=")
			out[k] = v
		}
	default:
		t.Fatalf("%s: %s has an environment of type %T", file, name, env)
	}
	return out
}

func (s zoneService) mounts(t *testing.T, file, name string) [][2]string {
	var out [][2]string
	for _, v := range s.Volumes {
		switch m := v.(type) {
		case string:
			source, target, _ := strings.Cut(m, ":")
			target, _, _ = strings.Cut(target, ":")
			out = append(out, [2]string{source, target})
		case map[string]any:
			out = append(out, [2]string{fmt.Sprint(m["source"]), fmt.Sprint(m["target"])})
		default:
			t.Fatalf("%s: %s has a volume of type %T", file, name, v)
		}
	}
	return out
}

// The agent zone (serve, the sandbox and its egress proxy) must reach no
// file or variable that holds the secret key, secrets.json, the database
// URL or a model key.
func TestAgentZoneContainersHoldNoControlZoneSecret(t *testing.T) {
	root := repoRootForTest(t)
	files := map[string]map[string]zoneService{}
	controlVolumes := map[string]string{}
	for _, name := range []string{"compose.yml", "apps/openneko/assets/compose/core.yml", "apps/openneko/assets/compose/bundled-db.yml",
		"apps/openneko/assets/compose/demo.yml", "recipes/sandbox/compose.yml"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Services map[string]zoneService `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = doc.Services
		for service, s := range doc.Services {
			for _, m := range s.mounts(t, name, service) {
				if strings.HasPrefix(m[1], "/config/openneko") {
					controlVolumes[m[0]] = service + " in " + name
				}
			}
		}
	}
	if len(controlVolumes) == 0 {
		t.Fatal("no control-zone volume found: the check read nothing")
	}
	checked := 0
	for name, services := range files {
		for _, service := range agentZoneServices {
			s, ok := services[service]
			if !ok {
				continue
			}
			checked++
			if s.EnvFile != nil || !onlySandboxKey(s.Secrets) || len(s.Configs) > 0 {
				t.Errorf("%s: %s reads an env_file, a secret other than the sandbox key, or a config", name, service)
			}
			for key, value := range s.env(t, name, service) {
				if controlZoneName(key) || controlZoneValue.MatchString(value) {
					t.Errorf("%s: %s gets %s=%s", name, service, key, value)
				}
			}
			for _, m := range s.mounts(t, name, service) {
				source, target := m[0], m[1]
				if !plainVolume.MatchString(source) {
					t.Errorf("%s: %s mounts %s, which is not a named volume", name, service, source)
				}
				if owner, ok := controlVolumes[source]; ok {
					t.Errorf("%s: %s mounts %s, which %s mounts at /config/openneko", name, service, source, owner)
				}
				if strings.HasPrefix(target, "/config/openneko") || strings.HasSuffix(target, "secrets.json") || strings.HasSuffix(target, "secret-key") {
					t.Errorf("%s: %s mounts a volume at %s", name, service, target)
				}
			}
		}
	}
	if checked < 4 {
		t.Fatalf("checked %d agent-zone services, want serve in compose.yml, core.yml and demo.yml and the sandbox", checked)
	}
}

// onlySandboxKey reports whether secrets holds no secret but sandbox-key,
// which serve needs to open a sandbox session.
func onlySandboxKey(secrets []any) bool {
	for _, s := range secrets {
		m, ok := s.(map[string]any)
		if !ok || m["source"] != "sandbox-key" {
			return false
		}
	}
	return true
}
