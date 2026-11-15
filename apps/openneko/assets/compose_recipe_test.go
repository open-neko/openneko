package assets

import (
	"os/exec"
	"testing"

	"gopkg.in/yaml.v3"
)

var documentedRootComposeSets = map[string][]string{
	"bundled database": {"compose.yml", "compose.db.yml"},
	"sandbox":          {"compose.yml", "compose.db.yml", "recipes/sandbox/compose.yml"},
}

func TestRootComposeSetsDefineEveryService(t *testing.T) {
	for name, files := range documentedRootComposeSets {
		t.Run(name, func(t *testing.T) {
			defined := map[string]bool{}
			named := map[string]string{}
			for _, file := range files {
				var doc struct {
					Services map[string]map[string]any `yaml:"services"`
				}
				if err := yaml.Unmarshal(readRootCompose(t, file), &doc); err != nil {
					t.Fatalf("%s: %v", file, err)
				}
				for svc, body := range doc.Services {
					named[svc] = file
					if body["image"] != nil || body["build"] != nil {
						defined[svc] = true
					}
				}
			}
			for svc, file := range named {
				if !defined[svc] {
					t.Errorf("%s names service %q, which no file gives an image or a build", file, svc)
				}
			}
		})
	}
}

func TestRootComposeSetsPassComposeConfig(t *testing.T) {
	if exec.Command("docker", "compose", "version").Run() != nil {
		t.Skip("docker compose is not installed")
	}
	for name, files := range documentedRootComposeSets {
		t.Run(name, func(t *testing.T) {
			args := []string{"compose"}
			for _, file := range files {
				args = append(args, "-f", file)
			}
			cmd := exec.Command("docker", append(args, "config", "-q")...)
			cmd.Dir = "../../.."
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("docker compose config: %v\n%s", err, out)
			}
		})
	}
}
