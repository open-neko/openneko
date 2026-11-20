package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBrewCaskInstallsTheNativeArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the cask script runs on the Linux release runner")
	}
	script := filepath.Join(repoRootForTest(t), "scripts", "write-brew-cask.sh")
	targets := []string{"darwin_arm64", "darwin_amd64", "linux_arm64", "linux_amd64"}
	sums := func(skip string) string {
		var b strings.Builder
		for i, target := range targets {
			if target != skip {
				b.WriteString(strings.Repeat(string(rune('a'+i)), 64) + "  openneko-native_6.1.0_" + target + ".tar.gz\n")
			}
		}
		return b.String()
	}
	run := func(tag, body string) (string, error) {
		file := filepath.Join(t.TempDir(), "checksums-native.txt")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", script, tag, file).Output()
		return string(out), err
	}

	cask, err := run("v6.1.0", sums(""))
	if err != nil {
		t.Fatal(err)
	}
	for i, target := range targets {
		want := "sha256 \"" + strings.Repeat(string(rune('a'+i)), 64) + "\"\n      url \"https://github.com/open-neko/openneko/releases/download/v#{version}/openneko-native_#{version}_" + target + ".tar.gz\""
		if !strings.Contains(cask, want) {
			t.Errorf("the cask lacks %s with its checksum", target)
		}
	}
	for _, want := range []string{`version "6.1.0"`, `binary "bin/openneko"`, `"-dr", "com.apple.quarantine", staged_path.to_s`} {
		if !strings.Contains(cask, want) {
			t.Errorf("the cask lacks %s", want)
		}
	}
	if _, err := run("v6.1.0", sums("darwin_amd64")); err == nil {
		t.Error("the script wrote a cask with a missing checksum")
	}
	if _, err := run("v6.1.0-rc1", sums("")); err == nil {
		t.Error("the script wrote a cask for a prerelease")
	}
}
