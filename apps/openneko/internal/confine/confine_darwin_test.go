package confine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeProfileNamesTheRealPathOfASocketBeforeItExists(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "confine-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "check.sock")
	p, err := profile(serveBase, Spec{Listen: []string{sock}, ReadWrite: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join("/private", sock)
	for _, want := range []string{
		`(allow network-bind network-inbound (local unix-socket (path-literal "` + real + `")))`,
		`(allow network-outbound (remote unix-socket (path-literal "` + real + `")))`,
		`(allow file-read* file-write* (subpath "` + filepath.Join("/private", dir) + `"))`,
		`(allow network-outbound (remote ip "*:*"))`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the profile lacks %s:\n%s", want, p)
		}
	}
	if strings.Contains(p, "(allow mach-lookup)\n") || strings.Contains(p, "(allow network*)") {
		t.Errorf("the profile allows every Mach service or every socket:\n%s", p)
	}
}
