package batchshell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-neko/harness/internal/batch"
)

func TestBundlePinIncludesImportsAndRejectsLinks(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "run.py")
	imported := filepath.Join(root, "vendor.py")
	for path, value := range map[string]string{script: "import vendor", imported: "VALUE = 1"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	initial, err := BundleSHA256(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imported, []byte("VALUE = 2"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := BundleSHA256(root)
	if err != nil || changed == initial {
		t.Fatalf("import change did not change bundle pin: %v", err)
	}
	scriptHash := sha256.Sum256([]byte("import vendor"))
	cli := filepath.Join(t.TempDir(), "openshell")
	if err := os.WriteFile(cli, []byte("binary"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := batch.Config{Script: script, ScriptSHA256: hex.EncodeToString(scriptHash[:]), WorkDir: root, ScriptCacheDir: RemoteCacheDir, ArtifactName: "contacts.csv"}
	opts := Options{CLI: cli, Gateway: "fixture", Image: "fixture", BundleRoot: root, BundleSHA256: initial}
	if _, err := New(cfg, opts); err == nil {
		t.Fatal("stale bundle pin accepted")
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	if _, err := BundleSHA256(root); err == nil {
		t.Fatal("symlink accepted in uploaded bundle")
	}
}

func TestReapOnlyMatchingOwnedSandbox(t *testing.T) {
	root := t.TempDir()
	cli, marker := filepath.Join(root, "openshell"), filepath.Join(root, "deleted")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"sandbox list"*) printf '%%s\n' '{"sandboxes":[{"name":"hb-fixture","labels":{"openneko.batch_run":"run-1"}}],"next_page_token":""}' ;;
  *"sandbox delete hb-fixture"*) touch '%s' ;;
  *) exit 1 ;;
esac
`, marker)
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{opts: Options{CLI: cli, Gateway: "fixture", RunID: "run-1"}, name: "hb-fixture"}
	if err := runner.reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("owned sandbox was not deleted: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	script = fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"sandbox list"*"--page-size"*) echo "error: unexpected argument '--page-size'" >&2; exit 2 ;;
  *"sandbox list"*"--limit"*) printf '%%s\n' '[{"name":"hb-fixture","labels":{"openneko.batch_run":"another-run"}}]' ;;
  *"sandbox delete hb-fixture"*) touch '%s' ;;
  *) exit 1 ;;
esac
`, marker)
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := runner.reap(context.Background()); err == nil {
		t.Fatal("foreign sandbox was deleted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("foreign sandbox delete marker: %v", err)
	}
}

func TestReapRejectsIncompleteCurrentInventory(t *testing.T) {
	root := t.TempDir()
	cli := filepath.Join(root, "openshell")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf '{\"sandboxes\":[],\"next_page_token\":\"more\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{opts: Options{CLI: cli, Gateway: "fixture", RunID: "run-1"}, name: "hb-fixture"}
	if err := runner.reap(context.Background()); err == nil {
		t.Fatal("accepted incomplete sandbox inventory")
	}
}
