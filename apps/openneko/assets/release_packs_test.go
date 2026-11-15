package assets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasesShipTheBuiltInPacks(t *testing.T) {
	root := repoRootForTest(t)
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	dockerfile := read("Dockerfile")
	web := dockerfile[strings.Index(dockerfile, " AS web\n"):]
	web = web[:strings.Index(web, "\nFROM ")]
	if !strings.Contains(web, "COPY packs /usr/local/share/openneko/packs\n") {
		t.Error("the web image does not copy the packs to /usr/local/share/openneko/packs, beside /usr/local/bin/openneko")
	}
	if !strings.Contains(web, "/usr/local/bin/openneko") {
		t.Error("the web image no longer puts openneko in /usr/local/bin; move the packs with it")
	}
	if !strings.Contains(read("scripts/build-native-release.sh"), `cp -R "$root/packs" "$tree/share/openneko/packs"`) {
		t.Error("the native archive does not hold share/openneko/packs")
	}
	if !strings.Contains(read("scripts/install-native.sh"), " share/openneko/packs\n") {
		t.Error("install-native.sh does not extract share/openneko/packs")
	}
}
