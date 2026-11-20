package assets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const sandboxImageRef = "${OPENNEKO_SANDBOX_IMAGE:-ghcr.io/open-neko/neko-sandbox:${OPENNEKO_VERSION:?set OPENNEKO_VERSION to the release tag, for example v6.0.0}}"

var publishedImage = regexp.MustCompile(`ghcr\.io/open-neko/([a-z0-9-]+):\$\{OPENNEKO_VERSION:(?:-latest|\?[^}]*)\}`)

func TestReleasePublishesEveryImageThatAnInstallNames(t *testing.T) {
	root := repoRootForTest(t)
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	named := map[string]bool{}
	for _, name := range []string{
		"apps/openneko/assets/compose/core.yml",
		"apps/openneko/assets/compose/demo.yml",
		"apps/openneko/assets/compose/bundled-db.yml",
		"recipes/sandbox/compose.yml",
		"recipes/sandbox/compose.native.yml",
	} {
		for _, m := range publishedImage.FindAllStringSubmatch(read(name), -1) {
			named[m[1]] = true
		}
	}
	if !named["neko-sandbox"] {
		t.Fatal("the sandbox recipe names no published image")
	}

	var workflow struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Include []map[string]string `yaml:"include"`
					Image   []string            `yaml:"image"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []struct {
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(read(".github/workflows/release-binaries.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	var budgets map[string]float64
	if err := json.Unmarshal([]byte(read("scripts/image-size-budgets.json")), &budgets); err != nil {
		t.Fatal(err)
	}
	build := workflow.Jobs["build-image"]
	var buildWith map[string]string
	for _, step := range build.Steps {
		if step.With["outputs"] != "" {
			buildWith = step.With
		}
	}
	if buildWith["context"] != "${{ matrix.context || '.' }}" || buildWith["file"] != "${{ matrix.file || './Dockerfile' }}" {
		t.Fatalf("the build step does not take its context and file from the matrix: %v", buildWith)
	}
	var stack string
	for _, step := range workflow.Jobs["image-size"].Steps {
		stack += step.Run
	}
	for image := range named {
		for _, platform := range []string{"linux/amd64", "linux/arm64"} {
			if !slices.ContainsFunc(build.Strategy.Matrix.Include, func(row map[string]string) bool {
				return row["image"] == image && row["platform"] == platform
			}) {
				t.Errorf("the release builds no %s for %s", image, platform)
			}
		}
		if !slices.Contains(workflow.Jobs["merge-manifests"].Strategy.Matrix.Image, image) {
			t.Errorf("the release merges no manifest for %s", image)
		}
		if budgets[image] <= 0 {
			t.Errorf("scripts/image-size-budgets.json has no budget for %s", image)
		}
		if !regexp.MustCompile(`(^|\s)` + regexp.QuoteMeta(image) + `(\s|$)`).MatchString(stack) {
			t.Errorf("the pull budget job does not count %s", image)
		}
	}
	for _, row := range build.Strategy.Matrix.Include {
		if row["image"] == "neko-sandbox" && (row["context"] != "recipes/sandbox" || row["file"] != "recipes/sandbox/Dockerfile") {
			t.Errorf("the release builds neko-sandbox from %q and %q, want recipes/sandbox", row["context"], row["file"])
		}
	}
}

func TestSandboxRecipePinsTheReleaseImage(t *testing.T) {
	root := repoRootForTest(t)
	for _, file := range []string{"recipes/sandbox/compose.yml", "recipes/sandbox/compose.native.yml"} {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Services map[string]map[string]yaml.Node `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, svc := range []string{"openneko-sandbox", "openneko-egress"} {
			got := doc.Services[svc]
			if image := got["image"]; image.Value != sandboxImageRef {
				t.Errorf("%s %s image = %q, want %q", file, svc, image.Value, sandboxImageRef)
			}
			if _, builds := got["build"]; builds {
				t.Errorf("%s %s has a build: line; a source build is the explicit OPENNEKO_SANDBOX_IMAGE choice", file, svc)
			}
			if policy, set := got["pull_policy"]; set && policy.Value != "missing" {
				t.Errorf("%s %s pull_policy = %q", file, svc, policy.Value)
			}
		}
	}
}

func TestReleaseChecksUseThePublishedSandboxImage(t *testing.T) {
	root := repoRootForTest(t)
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	smoke := read(".github/workflows/post-release-smoke.yml")
	for _, want := range []string{
		"recipes/sandbox/compose.yml \\\n",
		"OPENNEKO_EGRESS_PROOF_IMAGE: ghcr.io/open-neko/neko-sandbox:${{ steps.ctx.outputs.tag }}",
		"if grep -q 'build:' ~/.config/openneko/compose.override.yml; then",
	} {
		if !strings.Contains(smoke, want) {
			t.Errorf("post-release smoke lacks %q", want)
		}
	}
	if strings.Contains(smoke, "docker build") {
		t.Error("post-release smoke builds an image instead of testing the published one")
	}
	if !strings.Contains(read(".github/workflows/pr-checks.yml"), "docker build -t openneko-sandbox:proof .") {
		t.Error("the PR checks no longer build the sandbox Dockerfile")
	}
}
