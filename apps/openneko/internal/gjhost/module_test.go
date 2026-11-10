package gjhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pinnedCommit = "6f14910d5e64"

func TestGraphJinReplacesPointAtThePinnedUpstreamCommit(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "replace"))
		from, to, ok := strings.Cut(line, "=>")
		if !ok || !strings.Contains(from+to, "graphjin") {
			continue
		}
		found++
		target := strings.Fields(to)
		if len(target) != 2 {
			t.Errorf("GraphJin replace %q has no module version; a local path is not allowed", line)
			continue
		}
		if !strings.HasPrefix(target[0], "github.com/dosco/graphjin/") {
			t.Errorf("GraphJin replace %q points away from github.com/dosco/graphjin", line)
		}
		if !strings.HasSuffix(target[1], "-"+pinnedCommit) {
			t.Errorf("GraphJin replace %q is not upstream's v3.21.18 commit %s", line, pinnedCommit)
		}
	}
	if found == 0 {
		t.Fatal("go.mod has no GraphJin replace lines; this test reads the wrong file")
	}

	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "ARG GRAPHJIN_VERSION=3.21.18") || !strings.Contains(string(dockerfile), pinnedCommit) {
		t.Fatalf("the Dockerfile pin and the commit %s must move together", pinnedCommit)
	}
}
