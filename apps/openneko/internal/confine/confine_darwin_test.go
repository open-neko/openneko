package confine

import (
	"fmt"
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
		`(allow file-write-create file-write-unlink (literal "` + real + `"))`,
		fmt.Sprintf(`(allow sysctl-read (sysctl-name "kern.proc.pid.%d"))`, os.Getpid()),
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the profile lacks %s:\n%s", want, p)
		}
	}
	if strings.Contains(p, "(allow mach-lookup)\n") || strings.Contains(p, "(allow network*)") {
		t.Errorf("the profile allows every Mach service or every socket:\n%s", p)
	}
}

func TestProfilesNameEachSysctlTheyRead(t *testing.T) {
	for name, base := range map[string]string{"runner": runnerBase, "serve": serveBase} {
		p, err := profile(base, Spec{})
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"(allow sysctl-read)", "(allow sysctl-read)\n", "kern.procargs", `"kern.proc."`, `"kern.proc.pid."`, "(allow process-info*)", "(target others)"} {
			if strings.Contains(p, bad) {
				t.Errorf("the %s profile holds %s:\n%s", name, bad, p)
			}
		}
		if !strings.Contains(p, `(sysctl-name "hw.pagesize")`) || !strings.Contains(p, `(sysctl-name "hw.ncpu")`) {
			t.Errorf("the %s profile does not let the Go runtime read hw.pagesize and hw.ncpu:\n%s", name, p)
		}
	}
}
