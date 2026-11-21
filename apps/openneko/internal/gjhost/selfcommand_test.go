package gjhost

import (
	"path/filepath"
	"slices"
	"testing"
)

// A Seatbelt-confined serve may not read the folder it started in.
func TestSelfCommandStartsInTheConfigFolder(t *testing.T) {
	dir := t.TempDir()
	cmd := SelfCommand(filepath.Join(dir, "agentic.yml"))("unused.sock")
	if cmd.Dir != dir {
		t.Fatalf("the GraphJin child starts in %q, want %q", cmd.Dir, dir)
	}
	if !slices.Contains(cmd.Env, "PWD="+dir) {
		t.Fatalf("the GraphJin child environment has no PWD=%s", dir)
	}
}
