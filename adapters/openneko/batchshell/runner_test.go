package batchshell

import (
	"crypto/sha256"
	"encoding/hex"
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
	cfg := batch.Config{Script: script, ScriptSHA256: hex.EncodeToString(scriptHash[:]), WorkDir: root, ScriptCacheDir: RemoteCacheDir}
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
