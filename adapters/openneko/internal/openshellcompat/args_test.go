package openshellcompat

import (
	"reflect"
	"testing"
)

func TestColdCreatePreservesAuthorityAndStaging(t *testing.T) {
	args := []string{"--gateway", "isolated", "sandbox", "create", "--name", "run-1", "--from", "image", "--provider", "model", "--policy", "policy.json", "--upload", "org:/sandbox", "--no-tty", "--", "/bin/sh", "-lc", "true"}
	before := append([]string(nil), args...)
	got, err := Prepare(args)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"--gateway", "isolated", "sandbox", "create", "--name", "run-1", "--from", "image", "--provider", "model", "--policy", "policy.json", "--no-tty", "--detach", "--", "/bin/sleep", "infinity"},
		{"--gateway", "isolated", "sandbox", "upload", "run-1", "org", "/sandbox"},
	}
	if !reflect.DeepEqual(got.Commands, want) || !reflect.DeepEqual(args, before) {
		t.Fatalf("unexpected arguments: %v", got)
	}
}
func TestUnrelatedCommandsUnchanged(t *testing.T) {
	for _, args := range [][]string{
		{"sandbox", "exec", "-n", "run", "--", "/bin/sh", "-lc", "true"},
		{"sandbox", "create", "--", "python", "warm.py"},
		{"--gateway", "sandbox", "provider", "create", "--", "/bin/sh", "-lc", "true"},
		{"sandbox", "create", "--help"},
	} {
		got, err := Prepare(args)
		if err != nil || !reflect.DeepEqual(got.Commands, [][]string{args}) {
			t.Fatalf("changed unrelated command %v: %v", args, err)
		}
	}
}
func TestNoKeepFailsExplicitly(t *testing.T) {
	if _, err := Prepare([]string{"sandbox", "create", "--no-keep", "--", "/bin/sh", "-lc", "true"}); err == nil {
		t.Fatal("incompatible cleanup semantics accepted")
	}
}
