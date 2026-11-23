package ownacl

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const systemSID = "S-1-5-18"

// Set gives path a protected access list that allows only the current user
// and SYSTEM, and reads it back. A folder passes the list to its files and
// subfolders. A volume that keeps no access list is refused.
func Set(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := keepsLists(path); err != nil {
		return err
	}
	want, err := sddl(info.IsDir())
	if err != nil {
		return err
	}
	if have, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION); err == nil && strings.Replace(have.String(), "D:PAI(", "D:P(", 1) == want {
		return nil
	}
	sd, err := windows.SecurityDescriptorFromString(want)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := setList(path, dacl); err != nil {
		return fmt.Errorf("limit %s to its owner: %w", path, err)
	}
	if err := Verify(path); err != nil {
		return fmt.Errorf("limit %s to its owner: the list did not hold: %w", path, err)
	}
	return nil
}

var setList = func(path string, dacl *windows.ACL) error {
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

var volumeFlags = func(path string) (uint32, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	root := make([]uint16, max(len(path), windows.MAX_PATH)+1)
	if err := windows.GetVolumePathName(p, &root[0], uint32(len(root))); err != nil {
		return 0, err
	}
	var flags uint32
	if err := windows.GetVolumeInformation(&root[0], nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return 0, err
	}
	return flags, nil
}

func keepsLists(path string) error {
	flags, err := volumeFlags(path)
	if err != nil {
		return fmt.Errorf("read the volume of %s: %w", path, err)
	}
	if flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return fmt.Errorf("the volume of %s keeps no access list (FAT or exFAT), so OpenNeko cannot limit it to its owner; use a folder on an NTFS or ReFS volume", path)
	}
	return nil
}

func sddl(dir bool) (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	return fmt.Sprintf("D:P(A;%[1]s;FA;;;SY)(A;%[1]s;FA;;;%[2]s)", inherit, user.User.Sid.String()), nil
}

// Verify returns an error unless the access list of path allows only the
// current user and SYSTEM.
func Verify(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return fmt.Errorf("%s has no access list, so everyone may open it", path)
	}
	owner := user.User.Sid.String()
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return err
		}
		if denies(ace.Header.AceType) {
			continue
		}
		if !simple(ace.Header.AceType) {
			return fmt.Errorf("%s holds an access entry of type %d that OpenNeko does not read", path, ace.Header.AceType)
		}
		if sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String(); sid != owner && sid != systemSID {
			return fmt.Errorf("%s also allows %s", path, sid)
		}
	}
	return nil
}

// Access entry types of winnt.h. The allowed and allowed-callback entries
// keep the SID at the same place; the object entries do not.
const (
	aceAllowed              = 0
	aceDenied               = 1
	aceDeniedObject         = 6
	aceAllowedCallback      = 9
	aceDeniedCallback       = 10
	aceDeniedCallbackObject = 12
)

func denies(t uint8) bool {
	switch t {
	case aceDenied, aceDeniedObject, aceDeniedCallback, aceDeniedCallbackObject:
		return true
	}
	return false
}

func simple(t uint8) bool {
	return t == aceAllowed || t == aceAllowedCallback
}

// SocketFolder makes the folder of a socket path owner-only when it does not
// exist. A Windows socket takes the access list of its folder, so a folder
// that exists and allows another account is refused; SocketFolder does not
// change the access list of a folder it did not make.
func SocketFolder(path string) error {
	dir := filepath.Dir(path)
	err := os.Mkdir(dir, 0o700)
	if err == nil {
		if err := Set(dir); err != nil {
			_ = os.Remove(dir)
			return fmt.Errorf("socket folder %s: %w", dir, err)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("socket folder %s: %w", dir, err)
	}
	if err := Verify(dir); err != nil {
		return fmt.Errorf("socket folder %s is open to another account, and OpenNeko does not change a folder it did not make; give the socket a folder of its own: %w", dir, err)
	}
	return nil
}
