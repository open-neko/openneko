// Package testsock gives tests a short folder for Unix sockets.
package testsock

import (
	"os"
	"runtime"
	"testing"
)

// Dir makes a folder under /tmp and removes it when the test ends. A socket
// path holds at most 104 bytes on macOS, and t.TempDir() there is too long.
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
	return dir
}
