package gjhost

import (
	"os"
	"path/filepath"
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

func TestGraphJinSocketLimitsOrRefusesAnOpenFolder(t *testing.T) {
	open := testsock.Dir(t)
	setList(t, open, "D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	ln, err := listen("unix:" + filepath.Join(open, "gj.sock"))
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	if err := ownacl.Verify(open); err != nil {
		t.Errorf("the GraphJin socket folder stays open: %v", err)
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	locked := testsock.Dir(t)
	setList(t, locked, "D:P(D;;WD;;;OW)(D;;WD;;;"+user.User.Sid.String()+")(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	path := filepath.Join(locked, "gj.sock")
	if ln, err := listen("unix:" + path); err == nil {
		_ = ln.Close()
		t.Error("GraphJin listens in a folder that stays open to every account")
	} else if _, err := os.Lstat(path); err == nil {
		t.Errorf("refused, but left a socket at %s", path)
	}
}
