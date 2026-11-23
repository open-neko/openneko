package ownacl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func setTestList(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, _ := sd.DACL()
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func listOf(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

func TestSocketFolderRefusesAnOpenFolderItDidNotMake(t *testing.T) {
	dir := t.TempDir()
	setTestList(t, dir, "D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	before := listOf(t, dir)
	err := SocketFolder(filepath.Join(dir, "x.sock"))
	if err == nil || !strings.Contains(err.Error(), "does not change a folder it did not make") {
		t.Fatalf("SocketFolder took a folder open to everyone: %v", err)
	}
	if after := listOf(t, dir); after != before {
		t.Fatalf("SocketFolder changed the list of a folder it did not make: %s to %s", before, after)
	}
}

func TestSocketFolderMakesAMissingFolderOwnerOnly(t *testing.T) {
	parent := t.TempDir()
	setTestList(t, parent, "D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	dir := filepath.Join(parent, "sockets")
	if err := SocketFolder(filepath.Join(dir, "x.sock")); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err != nil {
		t.Fatalf("the new folder: %v", err)
	}
	if err := SocketFolder(filepath.Join(dir, "y.sock")); err != nil {
		t.Fatalf("an owner-only folder that exists: %v", err)
	}
}

func TestVerifyReadsDenyEntriesAsHarmless(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	setTestList(t, file, "D:P(D;;WD;;;WD)(A;;FA;;;SY)(A;;FA;;;"+user.User.Sid.String()+")")
	if err := Verify(file); err != nil {
		t.Fatal(err)
	}
}

func TestSetReadsTheListBack(t *testing.T) {
	defer func(f func(string, *windows.ACL) error) { setList = f }(setList)
	setList = func(string, *windows.ACL) error { return nil }
	dir := t.TempDir()
	open := filepath.Join(dir, "f")
	if err := os.WriteFile(open, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	setTestList(t, open, "D:P(A;;FA;;;WD)(A;;FA;;;SY)")
	if err := Set(open); err == nil {
		t.Fatal("Set reports success while the file stays open to everyone")
	}
}

func TestSetRefusesAVolumeWithNoAccessList(t *testing.T) {
	flags, err := volumeFlags(t.TempDir())
	if err != nil || flags&windows.FILE_PERSISTENT_ACLS == 0 {
		t.Fatalf("control: the test folder's volume keeps no access list (flags %#x, %v)", flags, err)
	}
	defer func(f func(string) (uint32, error)) { volumeFlags = f }(volumeFlags)
	volumeFlags = func(string) (uint32, error) { return flags &^ windows.FILE_PERSISTENT_ACLS, nil }
	if err := Set(t.TempDir()); err == nil {
		t.Fatal("Set accepts a volume that keeps no access list")
	}
}
