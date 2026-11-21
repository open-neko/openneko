package cli

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInfo struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

// replaceInPlace renames next onto dst in one step with POSIX semantics,
// which Windows 10 1809 and later allow also when dst is the image of a
// running process. dst never goes missing.
func replaceInPlace(next, dst string) error {
	abs, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(next)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(from, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	name, err := windows.UTF16FromString(abs)
	if err != nil {
		return err
	}
	n := len(name) - 1
	buf := make([]byte, max(int(unsafe.Offsetof(fileRenameInfo{}.FileName))+len(name)*2, int(unsafe.Sizeof(fileRenameInfo{}))))
	info := (*fileRenameInfo)(unsafe.Pointer(&buf[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS | windows.FILE_RENAME_IGNORE_READONLY_ATTRIBUTE
	info.FileNameLength = uint32(n * 2)
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, &buf[0], uint32(len(buf)))
}
