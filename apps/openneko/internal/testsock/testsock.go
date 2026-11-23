// Package testsock gives tests a short folder for Unix sockets.
package testsock

import (
	"os"
	"runtime"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/ownacl"
)

// Dir makes an owner-only folder under /tmp and removes it when the test
// ends. A socket path holds at most 104 bytes on macOS, and t.TempDir()
// there is too long.
func Dir(t testing.TB) string {
	t.Helper()
	base := "/tmp"
	if runtime.GOOS == "windows" {
		base = ""
	}
	dir, err := os.MkdirTemp(base, "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := ownacl.Set(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}
