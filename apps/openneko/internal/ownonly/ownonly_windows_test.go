package ownonly

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func allowed(t *testing.T, path string) ([]string, bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	var sids []string
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if !slices.Contains(sids, sid) {
			sids = append(sids, sid)
		}
	}
	slices.Sort(sids)
	return sids, control&windows.SE_DACL_PROTECTED != 0
}

func TestSetLeavesOnlyTheOwnerAndSystem(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{user.User.Sid.String(), "S-1-5-18"}
	slices.Sort(want)

	dir := t.TempDir()
	before := filepath.Join(dir, "before")
	if err := os.WriteFile(before, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Mkdir(dir); err != nil {
		t.Fatal(err)
	}
	after := filepath.Join(dir, "sub", "after")
	if err := os.MkdirAll(filepath.Dir(after), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(after, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "secret-key")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Set(file); err != nil {
		t.Fatal(err)
	}
	if err := Set(file); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{dir, before, filepath.Dir(after), after, file} {
		sids, protected := allowed(t, path)
		if !slices.Equal(sids, want) {
			t.Errorf("%s allows %v, want only %v", path, sids, want)
		}
		if (path == dir || path == file) && !protected {
			t.Errorf("%s still inherits from its parent", path)
		}
	}
}
