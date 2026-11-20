package webfront

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseSkillsComeFromShareBesideTheExecutable(t *testing.T) {
	release, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(release, "bin", "openneko")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "openneko")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	writeSkill(t, filepath.Join(checkout, "packages", "llm", "assets", "sandbox-skills"), "xlsx")
	wd := filepath.Join(checkout, "apps", "openneko")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()

	if got := assetsRootFrom(link, empty, "sandbox-skills", "skills/sandbox", true); got != "" {
		t.Fatalf("no release and no checkout: got %q", got)
	}
	if got, want := assetsRootFrom(link, wd, "sandbox-skills", "skills/sandbox", true), filepath.Join(checkout, "packages", "llm", "assets", "sandbox-skills"); got != want {
		t.Fatalf("checkout: got %q, want %q", got, want)
	}
	if got := assetsRootFrom(link, wd, "sandbox-skills", "skills/sandbox", false); got != "" {
		t.Fatalf("checkout with no walk: got %q", got)
	}
	shared := filepath.Join(release, "share", "openneko", "skills", "sandbox")
	writeSkill(t, shared, "xlsx")
	for _, dir := range []string{empty, wd} {
		if got := assetsRootFrom(link, dir, "sandbox-skills", "skills/sandbox", false); got != shared {
			t.Fatalf("release, wd %s: got %q, want %q", dir, got, shared)
		}
	}
}

func TestBundledSkillsCheckNeedsEverySandboxSkill(t *testing.T) {
	builtin, sandbox := t.TempDir(), t.TempDir()
	t.Setenv("OPENNEKO_BUILTIN_SKILLS_ROOT", builtin)
	t.Setenv("OPENNEKO_SANDBOX_SKILLS_ROOT", sandbox)
	if _, err := BundledSkillsCheck(); err == nil || !strings.Contains(err.Error(), "no built-in skill") {
		t.Fatalf("empty: err = %v", err)
	}
	writeSkill(t, builtin, "graphjin-config")
	for name := range sandboxSkills {
		if name != "xlsx" {
			writeSkill(t, sandbox, name)
		}
	}
	if _, err := BundledSkillsCheck(); err == nil || !strings.Contains(err.Error(), "xlsx") {
		t.Fatalf("no xlsx: err = %v", err)
	}
	writeSkill(t, sandbox, "xlsx")
	names, err := BundledSkillsCheck()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(sandboxSkills)+1 {
		t.Fatalf("names = %v", names)
	}
}

func TestTheRepositorySkillsPassTheBundledSkillsCheck(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_BUILTIN_SKILLS_ROOT", filepath.Join(root, "packages", "llm", "assets", "builtin-skills"))
	t.Setenv("OPENNEKO_SANDBOX_SKILLS_ROOT", filepath.Join(root, "recipes", "sandbox", "skills"))
	if _, err := BundledSkillsCheck(); err != nil {
		t.Fatal(err)
	}
}

func TestNoCheckoutWalkInProductionOrBesideARelease(t *testing.T) {
	release := t.TempDir()
	exe := filepath.Join(release, "bin", "openneko")
	t.Setenv("OPENNEKO_ENV", "development")
	if !checkoutWalk(exe) {
		t.Fatal("development with no release layout: no walk")
	}
	if err := os.MkdirAll(filepath.Join(release, "share", "openneko"), 0o755); err != nil {
		t.Fatal(err)
	}
	if checkoutWalk(exe) {
		t.Fatal("an executable in a release layout walks to a checkout")
	}
	checkout := t.TempDir()
	writeSkill(t, filepath.Join(checkout, "packages", "llm", "assets", "builtin-skills"), "graphjin-config")
	if err := os.MkdirAll(filepath.Join(checkout, "packs", "slack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "packs", "slack", "pack.yaml"), []byte("metadata:\n  id: slack\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := assetsRootFrom(exe, checkout, "builtin-skills", "skills/builtin", checkoutWalk(exe)); got != "" {
		t.Fatalf("release layout with no skills folder: got %q", got)
	}
	if got := builtinPacksRoot(exe, checkout, checkoutWalk(exe)); got != "" {
		t.Fatalf("release layout with no packs folder: got %q", got)
	}
	t.Setenv("OPENNEKO_PACKS_DIR", "")
	t.Chdir(checkout)
	if n, err := BundledPacksCheck(); err != nil || n != 1 {
		t.Fatalf("checkout packs in development: %d %v", n, err)
	}
}
