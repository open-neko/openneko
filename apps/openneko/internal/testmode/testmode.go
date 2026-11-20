// Package testmode checks file modes in tests on every system.
package testmode

import (
	"io/fs"
	"os"
	"runtime"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/ownonly"
)

// HasModes reports whether the system keeps Unix mode bits. On Windows it
// logs that the mode checks of the test do not apply.
func HasModes(t testing.TB) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Log("Windows keeps no Unix mode bits; the mode check does not apply")
		return false
	}
	return true
}

// Is reports whether info has the permission bits want. On Windows it
// reports true.
func Is(t testing.TB, info fs.FileInfo, want fs.FileMode) bool {
	t.Helper()
	return !HasModes(t) || info.Mode().Perm() == want
}

// Private fails the test unless path is private to the current user: mode
// want on Unix, an access list of the user and SYSTEM only on Windows.
func Private(t testing.TB, path string, want fs.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if err := ownonly.Verify(path); err != nil {
			t.Fatal(err)
		}
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode %v, want %v", path, info.Mode().Perm(), want)
	}
}
