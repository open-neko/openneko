//go:build windows

package assets

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestInstallNativePowerShellKeepsUnexpandedPathEntries(t *testing.T) {
	shell := powerShell(t)
	arch := hostArch(t)
	key, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	old, oldKind, err := key.GetStringValue("Path")
	missing := errors.Is(err, registry.ErrNotExist)
	if err != nil && !missing {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE)
		if err != nil {
			t.Errorf("restore the user PATH: %v", err)
			return
		}
		defer k.Close()
		switch {
		case missing:
			err = k.DeleteValue("Path")
		case oldKind == registry.EXPAND_SZ:
			err = k.SetExpandStringValue("Path", old)
		default:
			err = k.SetStringValue("Path", old)
		}
		if err != nil {
			t.Errorf("restore the user PATH: %v", err)
		}
	})

	entry := `%USERPROFILE%\openneko-path-test`
	if err := key.SetExpandStringValue("Path", entry+";"+old); err != nil {
		t.Fatal(err)
	}

	archive := nativeArchive(t, true, []releaseFile{
		{"bin/openneko.exe", string(fakeOpenNeko(t))},
		{"share/openneko/web/index.html", "<html></html>"},
	})
	name := "openneko-native_9.9.9_windows_" + arch + ".zip"
	local := filepath.Join(t.TempDir(), "local")
	bindir := filepath.Join(local, "Programs", "OpenNeko", "bin")
	cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", filepath.Join(repoRootForTest(t), "scripts", "install-native.ps1"))
	cmd.Env = append(os.Environ(), "LOCALAPPDATA="+local, "PROCESSOR_ARCHITECTURE="+strings.ToUpper(arch), "PROCESSOR_ARCHITEW6432=",
		"OPENNEKO_PREFIX=", "OPENNEKO_RELEASE_URL="+serveNativeRelease(t, name, archive, ""), "OPENNEKO_VERSION=9.9.9",
		"FAKE_OPENNEKO_RECORD="+filepath.Join(t.TempDir(), "ran"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}

	got, kind, err := key.GetStringValue("Path")
	if err != nil {
		t.Fatal(err)
	}
	if kind != registry.EXPAND_SZ {
		t.Fatalf("the user PATH is now of kind %d, not REG_EXPAND_SZ", kind)
	}
	parts := strings.Split(got, ";")
	if parts[0] != entry {
		t.Fatalf("the installer expanded %s: %q", entry, got)
	}
	if parts[len(parts)-1] != bindir {
		t.Fatalf("the installer did not add %s: %q", bindir, got)
	}
}
