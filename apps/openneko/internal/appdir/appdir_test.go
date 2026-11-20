package appdir

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestFoldersFollowTheSystem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "Local"))
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, "")
	}
	state, err := State()
	if err != nil {
		t.Fatal(err)
	}
	got := []string{Config(), Data(), state}
	want := []string{
		filepath.Join(home, ".config", "openneko"),
		filepath.Join(home, ".local", "share", "openneko"),
		filepath.Join(home, ".local", "state", "openneko"),
	}
	if runtime.GOOS == "windows" {
		want = []string{
			filepath.Join(home, "Roaming", "OpenNeko"),
			filepath.Join(home, "Local", "OpenNeko"),
			filepath.Join(home, "Local", "OpenNeko", "state"),
		}
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("folder %d = %s, want %s", i, got[i], want[i])
		}
	}

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "c"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "d"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(xdg, "s"))
	state, _ = State()
	if Config() != filepath.Join(xdg, "c", "openneko") || Data() != filepath.Join(xdg, "d", "openneko") || state != filepath.Join(xdg, "s", "openneko") {
		t.Errorf("an XDG folder does not win: %s %s %s", Config(), Data(), state)
	}
}
