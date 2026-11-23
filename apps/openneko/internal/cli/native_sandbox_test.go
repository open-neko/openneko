//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

func TestPrepareNativeSandboxNeedsAKeyAndAPrivateFolder(t *testing.T) {
	dir := filepath.Join(testsock.Dir(t), "openneko-sandbox")
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
	open := filepath.Join(testsock.Dir(t), "open")
	if err := os.MkdirAll(open, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"unix://", "unix:", "unix://localhost"} {
		if err := prepareNativeSandbox(dir, prefix+filepath.Join(open, "sandbox.sock"), "k"); err == nil || !strings.Contains(err.Error(), "other users can enter it") {
			t.Fatalf("%s: a folder that the group can enter: %v", prefix, err)
		}
	}
	if err := prepareNativeSandbox(dir, "unix:sandbox.sock", "k"); err == nil || !strings.Contains(err.Error(), "names no socket path") {
		t.Fatalf("a unix URL with no path: %v", err)
	}
	long := "unix://" + filepath.Join(dir, strings.Repeat("s", native.MaxSocketPath()), "sandbox.sock")
	if err := prepareNativeSandbox(dir, long, "k"); err == nil || !strings.Contains(err.Error(), "shorter instance name") {
		t.Fatalf("a socket path over the limit: %v", err)
	}
	if err := prepareNativeSandbox(dir, "https://sandbox.example", "k"); err != nil {
		t.Fatalf("remote sandbox: %v", err)
	}
}
