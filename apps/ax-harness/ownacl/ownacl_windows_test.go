package ownacl

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestSocketFolderLimitsAnOpenFolder(t *testing.T) {
	dir := t.TempDir()
	setTestList(t, dir, "D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
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
	locked := func() string {
		dir := t.TempDir()
		setTestList(t, dir, "D:P(D;;WD;;;OW)(D;;WD;;;"+user.User.Sid.String()+")(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
		return dir
	}
	if dir := locked(); SocketFolder(filepath.Join(dir, "x.sock")) == nil {
		if err := Verify(dir); err != nil {
			t.Fatalf("SocketFolder accepts a folder that stays open to everyone: %v", err)
		}
		t.Log("a privilege of the token passed over the list, and the folder is limited")
	}

	control := t.TempDir()
	setTestList(t, control, "D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	if err := withoutPrivileges(t, func() error { return SocketFolder(filepath.Join(control, "x.sock")) }); err != nil {
		t.Fatalf("control: with no privilege, SocketFolder cannot limit a folder the account may change: %v", err)
	}
	dir := locked()
	if err := withoutPrivileges(t, func() error { return SocketFolder(filepath.Join(dir, "x.sock")) }); err == nil {
		t.Fatal("with no privilege, SocketFolder accepts a folder that stays open to everyone")
	}
}

type setupError struct{ error }

// withoutPrivileges runs fn on a thread that acts as a copy of the process
// token with every privilege off. The thread ends with the goroutine.
func withoutPrivileges(t *testing.T, fn func() error) error {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := windows.ImpersonateSelf(windows.SecurityImpersonation); err != nil {
			result <- setupError{err}
			return
		}
		var token windows.Token
		if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, true, &token); err != nil {
			result <- setupError{err}
			return
		}
		defer token.Close()
		if err := windows.AdjustTokenPrivileges(token, true, nil, 0, nil, nil); err != nil {
			result <- setupError{err}
			return
		}
		result <- fn()
	}()
	err := <-result
	if se := (setupError{}); errors.As(err, &se) {
		t.Fatalf("act without privileges: %v", se.error)
	}
	return err
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
