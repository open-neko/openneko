package assets

import (
	"os"
	"path/filepath"
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
