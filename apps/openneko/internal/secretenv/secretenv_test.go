package secretenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetReadsTheVariableThenItsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "k")
	if err := os.WriteFile(file, []byte(" from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECRETENV_T_A", "")
	t.Setenv("SECRETENV_T_A_FILE", file)
	if got := Get("SECRETENV_T_A"); got != "from-file" {
		t.Errorf("from the file: %q", got)
	}
	t.Setenv("SECRETENV_T_A", "from-env")
	if got := Get("SECRETENV_T_A"); got != "from-env" {
		t.Errorf("the variable wins over the file: %q", got)
	}
	t.Setenv("SECRETENV_T_B_FILE", filepath.Join(dir, "absent"))
	if got := Get("SECRETENV_T_B"); got != "" {
		t.Errorf("a missing file reads as unset: %q", got)
	}
}

func TestReadHandedNeedsTheFlag(t *testing.T) {
	t.Setenv(StdinEnv, "")
	if err := ReadHanded(strings.NewReader(`{"SECRETENV_T_C":"handed"}` + "\n")); err != nil || Get("SECRETENV_T_C") != "" {
		t.Fatalf("read stdin with no flag: %v", err)
	}
	t.Setenv(StdinEnv, "1")
	t.Setenv("SECRETENV_T_C", "from-env")
	if err := ReadHanded(strings.NewReader(`{"SECRETENV_T_C":"handed"}` + "\n")); err != nil || Get("SECRETENV_T_C") != "handed" {
		t.Fatalf("handed value: %q %v", Get("SECRETENV_T_C"), err)
	}
	if err := ReadHanded(strings.NewReader("")); err == nil {
		t.Error("an empty stdin with the flag set must fail")
	}
}
