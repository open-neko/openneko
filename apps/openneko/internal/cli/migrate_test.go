package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvTruthy(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "on", " on "} {
		t.Setenv("OPENNEKO_TEST_TRUTHY", value)
		if !envTruthy("OPENNEKO_TEST_TRUTHY") {
			t.Fatalf("%q should be true", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off", "garbage"} {
		t.Setenv("OPENNEKO_TEST_TRUTHY", value)
		if envTruthy("OPENNEKO_TEST_TRUTHY") {
			t.Fatalf("%q should be false", value)
		}
	}
}

func TestMigrateConfigRepoDryRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENNEKO_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")
	root := filepath.Join(home, ".config", "openneko", "agents", "orgs", "org")
	if err := os.MkdirAll(filepath.Join(root, "skills", "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "report", "SKILL.md"), []byte("---\nname: report\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-qm", "team"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	var out bytes.Buffer
	cmd := newMigrateCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"config-repo", "--dry-run"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "config repository would move: "+filepath.Join(root, ".git")) ||
		!strings.Contains(out.String(), "rename .git (kept as .git.pre-plain)") {
		t.Errorf("output: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Error("the dry run moved the repository")
	}
}
