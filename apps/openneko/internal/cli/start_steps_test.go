package cli

import (
	"reflect"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/compose"
)

func TestStartStepsRunInOrder(t *testing.T) {
	commands := func(mode compose.Mode) [][]string {
		var out [][]string
		for _, step := range startSteps(mode) {
			out = append(out, startStepArgs(step, []string{"--pull", "never"}))
		}
		return out
	}
	prod := [][]string{
		{"run", "--rm", "-T", "--pull", "never", "storage-reconcile"},
		{"run", "--rm", "-T", "--no-deps", "--pull", "never", "neko-migrate"},
		{"run", "--rm", "-T", "--no-deps", "--pull", "never", "openneko-config-init"},
		{"run", "--rm", "-T", "--no-deps", "--pull", "never", "openneko-serve-init"},
	}
	if got := commands(compose.ModeProd); !reflect.DeepEqual(got, prod) {
		t.Fatalf("prod steps = %v", got)
	}
	if got := commands(compose.ModeDev); !reflect.DeepEqual(got, prod) {
		t.Fatalf("dev steps = %v", got)
	}
	demo := slices.Clone(prod[:3])
	demo = append(demo,
		[]string{"run", "--rm", "-T", "--pull", "never", "adventureworks-init"},
		[]string{"run", "--rm", "-T", "--no-deps", "--pull", "never", "neko-adventureworks-seed"},
		prod[3],
	)
	if got := commands(compose.ModeDemo); !reflect.DeepEqual(got, demo) {
		t.Fatalf("demo steps = %v", got)
	}
}

func TestStartStepsExistInTheEmbeddedCompose(t *testing.T) {
	type service struct {
		Profiles []string `yaml:"profiles"`
	}
	load := func(names ...string) map[string]service {
		merged := map[string]service{}
		for _, name := range names {
			raw, err := assets.ComposeFS.ReadFile("compose/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Services map[string]service `yaml:"services"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			for key, value := range doc.Services {
				if current, ok := merged[key]; ok && len(value.Profiles) == 0 {
					value.Profiles = current.Profiles
				}
				merged[key] = value
			}
		}
		return merged
	}
	for mode, files := range map[compose.Mode][]string{
		compose.ModeProd: {"core.yml"},
		compose.ModeDev:  {"core.yml", "dev.yml"},
		compose.ModeDemo: {"core.yml", "demo.yml"},
	} {
		services := load(files...)
		for _, step := range startSteps(mode) {
			svc, ok := services[step.service]
			if !ok {
				t.Fatalf("%s: step %s is not in the compose files", mode, step.service)
			}
			if step.service != "adventureworks-init" && !slices.Contains(svc.Profiles, "steps") {
				t.Fatalf("%s: step %s is not in the steps profile", mode, step.service)
			}
		}
		for _, name := range legacyStepContainers(mode) {
			if !slices.Contains(services[name].Profiles, "steps") {
				t.Fatalf("%s: %s would remove a running service", mode, name)
			}
		}
	}
}
