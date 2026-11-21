package ownacl

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func setList(t *testing.T, path, sddl string) {
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

func TestSocketFolderLimitsAnOpenFolder(t *testing.T) {
	dir := t.TempDir()
	setList(t, dir, "D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	if err := Verify(dir); err == nil {
		t.Fatal("Verify accepts a folder open to everyone")
	}
	if err := SocketFolder(filepath.Join(dir, "x.sock")); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
}

func TestSocketFolderRefusesAFolderItCannotLimit(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	setList(t, dir, "D:P(D;;WD;;;OW)(D;;WD;;;"+user.User.Sid.String()+")(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	if err := SocketFolder(filepath.Join(dir, "x.sock")); err == nil {
		t.Fatal("SocketFolder accepts a folder that stays open to everyone")
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
	setList(t, file, "D:P(D;;WD;;;WD)(A;;FA;;;SY)(A;;FA;;;"+user.User.Sid.String()+")")
	if err := Verify(file); err != nil {
		t.Fatal(err)
	}
}
