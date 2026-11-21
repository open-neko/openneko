package testsock

import (
	"errors"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// LockedOpenFolder makes a folder that lets every account in and denies the
// current account the right to change its access list. A privilege of the
// token, such as SeRestorePrivilege, can still pass over that list.
func LockedOpenFolder(t testing.TB) string {
	t.Helper()
	dir := Dir(t)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(D;;WD;;;OW)(D;;WD;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, _ := sd.DACL()
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	return dir
}

type setupError struct{ error }

// WithoutPrivileges runs fn on a thread that acts as a copy of the process
// token with every privilege off. The thread is never handed back to the
// runtime, so it ends with the goroutine.
func WithoutPrivileges(t testing.TB, fn func() error) error {
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
		if left := enabled(token, overrides); len(left) != 0 {
			result <- setupError{errors.New("privileges stay on: " + left[0])}
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

var overrides = []string{"SeRestorePrivilege", "SeBackupPrivilege", "SeTakeOwnershipPrivilege", "SeSecurityPrivilege"}

// OverridePrivileges names the enabled privileges of the process token that
// pass over an access list.
func OverridePrivileges() []string {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return []string{"unknown: " + err.Error()}
	}
	defer token.Close()
	return enabled(token, overrides)
}

func enabled(token windows.Token, names []string) []string {
	var n uint32
	_ = windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &n)
	if n == 0 {
		return []string{"unknown"}
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buf[0], n, &n); err != nil {
		return []string{"unknown: " + err.Error()}
	}
	privs := (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0])).AllPrivileges()
	var on []string
	for _, name := range names {
		var luid windows.LUID
		p, _ := windows.UTF16PtrFromString(name)
		if windows.LookupPrivilegeValue(nil, p, &luid) != nil {
			continue
		}
		for _, priv := range privs {
			if priv.Luid == luid && priv.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 {
				on = append(on, name)
			}
		}
	}
	return on
}
