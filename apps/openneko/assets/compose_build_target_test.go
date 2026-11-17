package assets

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEveryComposeBuildTargetIsADockerfileStage(t *testing.T) {
	root := repoRootForTest(t)
	files, err := filepath.Glob(filepath.Join(root, "compose*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	recipes, err := filepath.Glob(filepath.Join(root, "recipes", "*", "compose*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, recipes...)
	if len(files) == 0 {
		t.Fatal("no compose files found")
	}
	stageLine := regexp.MustCompile(`(?im)^FROM\s+\S+\s+AS\s+(\S+)\s*$`)
	stagesOf := map[string]map[string]bool{}
	stages := func(dockerfile string) map[string]bool {
		if s, ok := stagesOf[dockerfile]; ok {
			return s
		}
		raw, err := os.ReadFile(dockerfile)
		if err != nil {
			t.Fatalf("read %s: %v", dockerfile, err)
		}
		s := map[string]bool{}
		for _, m := range stageLine.FindAllStringSubmatch(string(raw), -1) {
			s[m[1]] = true
		}
		stagesOf[dockerfile] = s
		return s
	}
	type build struct {
		Context    string `yaml:"context"`
		Dockerfile string `yaml:"dockerfile"`
		Target     string `yaml:"target"`
	}
	var doc struct {
		Services map[string]struct {
			Build yaml.Node `yaml:"build"`
		} `yaml:"services"`
	}
	checked := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		doc.Services = nil
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for name, svc := range doc.Services {
			if svc.Build.Kind != yaml.MappingNode {
				continue
			}
			var b build
			if err := svc.Build.Decode(&b); err != nil {
				t.Fatalf("%s %s build: %v", file, name, err)
			}
			if b.Target == "" {
				continue
			}
			dockerfile := b.Dockerfile
			if dockerfile == "" {
				dockerfile = "Dockerfile"
			}
			path := filepath.Join(filepath.Dir(file), b.Context, dockerfile)
			if !stages(path)[b.Target] {
				t.Errorf("%s service %s builds target %q, which %s does not define", filepath.Base(file), name, b.Target, path)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no build target checked")
	}
}
