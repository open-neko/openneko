//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareNativeSandboxNeedsAKeyAndAPrivateFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "openneko-sandbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareNativeSandbox(dir, "", ""); err != nil {
		t.Fatalf("no sandbox: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("socket folder mode %v %v, want 0700", info.Mode().Perm(), err)
	}
	url := "unix://" + filepath.Join(dir, "sandbox.sock")
	if err := prepareNativeSandbox(dir, url, ""); err == nil || !strings.Contains(err.Error(), "needs a key") {
		t.Fatalf("no key: %v", err)
	}
	if err := prepareNativeSandbox(dir, url, "k"); err != nil {
		t.Fatalf("own folder with a key: %v", err)
	}
	open := filepath.Join(t.TempDir(), "open")
	if err := os.MkdirAll(open, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := prepareNativeSandbox(dir, "unix://"+filepath.Join(open, "sandbox.sock"), "k"); err == nil || !strings.Contains(err.Error(), "other users can enter it") {
		t.Fatalf("a folder that the group can enter: %v", err)
	}
	if err := prepareNativeSandbox(dir, "https://sandbox.example", "k"); err != nil {
		t.Fatalf("remote sandbox: %v", err)
	}
}
