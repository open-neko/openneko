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

	locked := testsock.LockedOpenFolder(t)
	path := filepath.Join(locked, "gj.sock")
	if ln, err := listen("unix:" + path); err != nil {
		if _, err := os.Lstat(path); err == nil {
			t.Errorf("refused, but left a socket at %s", path)
		}
	} else {
		_ = ln.Close()
		if err := ownacl.Verify(locked); err != nil {
			t.Errorf("GraphJin listens in a folder that stays open to every account: %v", err)
		} else {
			t.Logf("limited a folder that denies the change, with the privileges %v", testsock.OverridePrivileges())
		}
	}

	control := testsock.Dir(t)
	setList(t, control, "D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	if err := testsock.WithoutPrivileges(t, func() error {
		ln, err := listen("unix:" + filepath.Join(control, "gj.sock"))
		if err == nil {
			_ = ln.Close()
		}
		return err
	}); err != nil {
		t.Fatalf("control: with no privilege, cannot listen in a folder the account may limit: %v", err)
	}
	path = filepath.Join(testsock.LockedOpenFolder(t), "gj.sock")
	err = testsock.WithoutPrivileges(t, func() error {
		ln, err := listen("unix:" + path)
		if err == nil {
			_ = ln.Close()
		}
		return err
	})
	if err == nil {
		t.Error("with no privilege, GraphJin listens in a folder that stays open to every account")
	} else if !strings.Contains(err.Error(), "socket folder") {
		t.Errorf("with no privilege, refused for another reason: %v", err)
	} else if _, err := os.Lstat(path); err == nil {
		t.Errorf("refused, but left a socket at %s", path)
	}
}
