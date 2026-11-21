package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/installation"
	"github.com/open-neko/neko/apps/openneko/internal/instance"
)

// TestGraphjinEngineChildStartsWhereTheConfigFolderIsClosed: the network
// part of serve runs confined and cannot read the config folder. The engine
// child it starts must not read installation.json, or the customer GraphJin
// stops at each start. A host command still reads the file and reports it.
func TestGraphjinEngineChildStartsWhereTheConfigFolderIsClosed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(instance.EnvName, "")
	t.Cleanup(func() { _ = instance.Select("") })
	dir := filepath.Join(root, "openneko")
	if err := os.MkdirAll(filepath.Join(dir, installation.Filename), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		cmd := NewRoot()
		cmd.SetArgs(args)
		cmd.SetOut(new(strings.Builder))
		cmd.SetErr(new(strings.Builder))
		return cmd.Execute()
	}
	unreadable := func(err error) bool {
		return err != nil && strings.Contains(err.Error(), installation.Filename)
	}

	if err := run("graphjin", "status", "--config", filepath.Join(root, "none.yml")); !unreadable(err) {
		t.Fatalf("control: a host command did not report the unreadable %s: %v", installation.Filename, err)
	}

	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.WriteString("{}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = old; stdin.Close() })
	err = run("graphjin", "serve", "--child", "--config", filepath.Join(root, "none.yml"), "--addr", "unix:"+filepath.Join(root, "gj.sock"))
	if unreadable(err) {
		t.Fatalf("the GraphJin engine child read %s and stopped: %v", installation.Filename, err)
	}
}
