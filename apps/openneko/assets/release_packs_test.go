package assets

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleasesShipTheBuiltInPacks(t *testing.T) {
	root := repoRootForTest(t)
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	dockerfile := read("Dockerfile")
	web := dockerfile[strings.Index(dockerfile, " AS web\n"):]
	web = web[:strings.Index(web, "\nFROM ")]
	if !strings.Contains(web, "COPY packs /usr/local/share/openneko/packs\n") {
		t.Error("the web image does not copy the packs to /usr/local/share/openneko/packs, beside /usr/local/bin/openneko")
	}
	if !strings.Contains(web, "/usr/local/bin/openneko") {
		t.Error("the web image no longer puts openneko in /usr/local/bin; move the packs with it")
	}
	if !strings.Contains(read("scripts/build-native-release.sh"), `cp -R "$root/packs" "$tree/share/openneko/packs"`) {
		t.Error("the native archive does not hold share/openneko/packs")
	}
	if !strings.Contains(read("scripts/install-native.sh"), ` --no-same-owner bin/openneko share/openneko
`) {
		t.Error("install-native.sh does not extract share/openneko")
	}
}

func TestReleasesShipTheBundledSkills(t *testing.T) {
	root := repoRootForTest(t)
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	dockerfile := read("Dockerfile")
	web := dockerfile[strings.Index(dockerfile, " AS web\n"):]
	web = web[:strings.Index(web, "\nFROM ")]
	if !strings.Contains(web, "COPY packages/llm/assets/builtin-skills /usr/local/share/openneko/skills/builtin\n") {
		t.Error("the web image does not copy the built-in skills to /usr/local/share/openneko/skills/builtin")
	}
	skills, err := filepath.Glob(filepath.Join(root, "recipes", "sandbox", "skills", "*", "SKILL.md"))
	if err != nil || len(skills) == 0 {
		t.Fatalf("no sandbox skills in the repository: %v", err)
	}
	for _, skill := range skills {
		name := filepath.Base(filepath.Dir(skill))
		if !strings.Contains(web, "COPY recipes/sandbox/skills/"+name+" /usr/local/share/openneko/skills/sandbox/"+name+"\n") {
			t.Errorf("the web image does not copy the sandbox skill %s", name)
		}
	}
	if strings.Contains(web, "COPY recipes/sandbox/skills /") {
		t.Error("the web image copies the sandbox skills folder whole, with its package files and tests")
	}
	build := read("scripts/build-native-release.sh")
	for _, want := range []string{
		`cp -R "$root/packages/llm/assets/builtin-skills" "$tree/share/openneko/skills/builtin"`,
		`for skill in "$root"/recipes/sandbox/skills/*/SKILL.md; do`,
		`share/openneko/skills/sandbox/{docx,pdf,pptx,skill-creator,xlsx}/SKILL\\.md`,
	} {
		if !strings.Contains(build, want) {
			t.Errorf("build-native-release.sh lacks %q", want)
		}
	}
}

func TestReleasesShipEveryAssetFolderThatTheProductLooksUp(t *testing.T) {
	root := repoRootForTest(t)
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	checkoutLookups := map[string]bool{
		"internal/webfront/workspace.go":   true,
		"internal/webfront/packcatalog.go": true,
		"internal/compose/compose.go":      true,
		"internal/cli/eval.go":             true,
	}
	rootVariables := map[string]bool{
		"OPENNEKO_BUILTIN_SKILLS_ROOT": true,
		"OPENNEKO_SANDBOX_SKILLS_ROOT": true,
		"OPENNEKO_MCP_SKILLS_ROOT":     true,
		"OPENNEKO_WORKSPACE_ROOT":      true,
	}
	call := regexp.MustCompile(`\b(assetsRoot|releaseShareDir)\(([^)]*)\)`)
	assetsArgs := regexp.MustCompile(`^"[A-Z_]+", "[^"]+", "([^"]+)"$`)
	shareArgs := regexp.MustCompile(`^[a-z]+, "([^"]+)"$`)
	webDir := regexp.MustCompile(`"share", "openneko", "([^"]+)"`)
	rootVariable := regexp.MustCompile(`"(OPENNEKO_[A-Z_]*_ROOT)"`)
	released := map[string]bool{}
	module := filepath.Join(root, "apps", "openneko")
	err := filepath.WalkDir(module, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(module, path)
		rel = filepath.ToSlash(rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		if strings.Contains(src, "os.Getwd()") && !checkoutLookups[rel] {
			t.Errorf("%s reads the working folder; ship what it finds there in share/openneko and add the file to this test", rel)
		}
		for _, m := range rootVariable.FindAllStringSubmatch(src, -1) {
			if !rootVariables[m[1]] {
				t.Errorf("%s reads %s; ship its folder in share/openneko and add the variable to this test", rel, m[1])
			}
		}
		for _, m := range call.FindAllStringSubmatch(src, -1) {
			args := strings.TrimSpace(m[2])
			if strings.HasSuffix(args, " string") || (rel == "internal/webfront/workspace.go" && args == "exe, release") {
				continue
			}
			found := assetsArgs.FindStringSubmatch(args)
			if m[1] == "releaseShareDir" {
				found = shareArgs.FindStringSubmatch(args)
			}
			if found == nil {
				t.Errorf("%s: %s(%s) names no fixed release folder", rel, m[1], args)
				continue
			}
			released[found[1]] = true
		}
		for _, m := range webDir.FindAllStringSubmatch(src, -1) {
			released[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"web", "packs", "skills/builtin", "skills/sandbox"} {
		if !released[want] {
			t.Errorf("the scan found no lookup of share/openneko/%s; the scan is broken", want)
		}
	}
	dockerfile := read("Dockerfile")
	web := dockerfile[strings.Index(dockerfile, " AS web\n"):]
	web = web[:strings.Index(web, "\nFROM ")]
	build := read("scripts/build-native-release.sh")
	for folder := range released {
		if !strings.Contains(build, "share/openneko/"+folder) {
			t.Errorf("the native archive does not hold share/openneko/%s", folder)
		}
		if folder == "web" {
			if !strings.Contains(web, "OPENNEKO_WEB_DIR=") {
				t.Error("the web image does not set OPENNEKO_WEB_DIR")
			}
			continue
		}
		if !strings.Contains(web, "/usr/local/share/openneko/"+folder) {
			t.Errorf("the web image does not hold /usr/local/share/openneko/%s", folder)
		}
	}
}
