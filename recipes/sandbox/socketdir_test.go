//go:build linux

package main

import (
	"os"
	"testing"
)

// shortSocketDir makes a folder under /tmp and removes it when the test ends.
// A socket path holds at most 108 bytes on Linux, and t.TempDir() can be longer.
func shortSocketDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
