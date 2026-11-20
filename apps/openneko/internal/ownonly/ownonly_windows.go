package ownonly

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// Set gives path a protected access list that allows only the current user
// and SYSTEM. A folder passes the list to its files and subfolders.
func Set(path string) error {
	info, err := os.Stat(path)
	if err != nil {
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
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("limit %s to its owner: %w", path, err)
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
