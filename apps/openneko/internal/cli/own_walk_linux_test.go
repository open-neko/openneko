package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func setgidDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, fs.ModeSetgid|0o750); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode()&fs.ModeSetgid == 0 {
		t.Skip("this file system drops the setgid bit")
	}
}

func hasSetgid(t *testing.T, dir string) bool {
	t.Helper()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()&fs.ModeSetgid != 0
}

// TestOwnWalkFollowsNoLinkPlantedMidWalk swaps a folder for a link after the
// walk read its parent, as a live runner could.
func TestOwnWalkFollowsNoLinkPlantedMidWalk(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	outside := filepath.Join(base, "outside")
	runs := filepath.Join(data, "shared", "agent-runs")
	setgidDir(t, filepath.Join(outside, "inner"))
	setgidDir(t, filepath.Join(data, "a", "sub", "deep"))
	setgidDir(t, filepath.Join(data, "a", "kept"))
	setgidDir(t, filepath.Join(runs, "run1"))
	if err := os.Symlink(outside, filepath.Join(data, "link")); err != nil {
		t.Fatal(err)
	}
	ownWalkHook = func(dir string) {
		if dir != filepath.Join(data, "a") {
			return
		}
		sub := filepath.Join(dir, "sub")
		if err := os.RemoveAll(sub); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(outside, sub); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { ownWalkHook = nil })
	if err := ownTreeNoFollow(data, os.Getuid(), os.Getgid(), map[string]bool{runs: true}); err != nil {
		t.Fatal(err)
	}
	if !hasSetgid(t, filepath.Join(outside, "inner")) {
		t.Error("the walk followed a link out of the data folder")
	}
	if hasSetgid(t, filepath.Join(data, "a", "kept")) {
		t.Error("the walk did not reach a real folder")
	}
	if !hasSetgid(t, filepath.Join(runs, "run1")) {
		t.Error("the walk entered agent-runs")
	}
}

func TestOwnDirFollowsNoLinkBelowTheBase(t *testing.T) {
	t.Cleanup(func() { openNoFollowFirst = true })
	base := t.TempDir()
	data := filepath.Join(base, "data")
	outside := filepath.Join(base, "outside")
	setgidDir(t, filepath.Join(outside, "inner"))
	setgidDir(t, outside)
	setgidDir(t, filepath.Join(data, "shared", "run"))
	if err := os.Symlink(outside, filepath.Join(data, "shared", "door")); err != nil {
		t.Fatal(err)
	}
	for _, openat2 := range []bool{true, false} {
		openNoFollowFirst = openat2
		for _, dir := range []string{filepath.Join(data, "shared", "door"), filepath.Join(data, "shared", "door", "inner"), filepath.Join(data, "shared", "..", "..", "outside")} {
			if err := ownDir(data, dir, os.Getuid(), os.Getgid(), 0o700); err == nil {
				t.Errorf("openat2 %v: ownDir changed %s through a link or outside the base", openat2, dir)
			}
		}
		if err := ownDir(data, filepath.Join(data, "shared", "run"), os.Getuid(), os.Getgid(), 0o700); err != nil {
			t.Errorf("openat2 %v: %v", openat2, err)
		}
	}
	if !hasSetgid(t, filepath.Join(outside, "inner")) || !hasSetgid(t, outside) {
		t.Error("a folder behind a link changed")
	}
	if hasSetgid(t, filepath.Join(data, "shared", "run")) {
		t.Error("ownDir did not set the mode of a real folder")
	}
	linked := filepath.Join(base, "linked-data")
	if err := os.Symlink(data, linked); err != nil {
		t.Fatal(err)
	}
	if err := ownDir(linked, filepath.Join(linked, "shared", "run"), os.Getuid(), os.Getgid(), 0o700); err != nil {
		t.Errorf("a data folder that the operator linked elsewhere: %v", err)
	}
}
