package confine

import (
	"slices"
	"strings"
	"testing"
	unicode16 "unicode/utf16"
)

// TestRunnerBlockHoldsOnlyItsOwnAndThePathVariables: a runner in an app
// container needs the folder variables, and gets no other variable of the
// process that starts it.
func TestRunnerBlockHoldsOnlyItsOwnAndThePathVariables(t *testing.T) {
	t.Setenv("OPENNEKO_PLANTED_SECRET", "planted-in-serve")
	block, err := envBlock([]string{"PATH=C:\\bin", "TMPDIR=C:\\runs\\tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(block) < 2 || block[len(block)-1] != 0 || block[len(block)-2] != 0 {
		t.Fatal("the block does not end with two NULs")
	}
	var names []string
	for entry := range strings.SplitSeq(string(unicode16.Decode(block[:len(block)-2])), "\x00") {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, strings.ToUpper(name))
		if strings.Contains(entry, "planted") {
			t.Errorf("the runner's block holds %q", entry)
		}
	}
	for _, name := range names {
		if name != "PATH" && name != "TMPDIR" && !slices.Contains(windowsPaths, name) {
			t.Errorf("the runner's block holds %s", name)
		}
	}
	for _, name := range []string{"SYSTEMROOT", "USERPROFILE", "LOCALAPPDATA"} {
		if !slices.Contains(names, name) {
			t.Errorf("the runner's block lacks %s: %v", name, names)
		}
	}
}
