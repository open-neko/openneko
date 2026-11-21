package gjhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/ownacl"
)

func setList(t *testing.T, dir, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, _ := sd.DACL()
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestGraphJinSocketRefusesAnOpenFolderItDidNotMake(t *testing.T) {
	open := testsock.Dir(t)
	setList(t, open, "D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	path := filepath.Join(open, "gj.sock")
	if ln, err := listen("unix:" + path); err == nil {
		_ = ln.Close()
		t.Fatal("GraphJin listens in a folder that is open to every account")
	} else if !strings.Contains(err.Error(), "socket folder") {
		t.Fatalf("refused for another reason: %v", err)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("refused, but left a socket at %s", path)
	}
	if ownacl.Verify(open) == nil {
		t.Error("GraphJin changed the access list of a folder it did not make")
	}

	fresh := filepath.Join(open, "graphjin", "gj.sock")
	ln, err := listen("unix:" + fresh)
	if err != nil {
		t.Fatalf("listen in a new folder: %v", err)
	}
	_ = ln.Close()
	if err := ownacl.Verify(filepath.Dir(fresh)); err != nil {
		t.Errorf("the new GraphJin socket folder is open: %v", err)
	}
}
