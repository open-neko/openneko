package cli

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// legacyMetadataCalls tries the old amd64 calls that change a file by path.
func legacyMetadataCalls(path string) map[string]error {
	p, err := unix.BytePtrFromString(path)
	if err != nil {
		return map[string]error{"name the victim": err}
	}
	call := func(nr uintptr, a1, a2 uintptr) error {
		_, _, e := unix.Syscall(nr, uintptr(unsafe.Pointer(p)), a1, a2)
		return errnoErr(e)
	}
	uid, gid := uintptr(os.Getuid()), uintptr(os.Getgid())
	out := map[string]error{
		"chmod a file outside its folders (chmod)":            call(unix.SYS_CHMOD, 0o600, 0),
		"chown a file outside its folders (chown)":            call(unix.SYS_CHOWN, uid, gid),
		"chown a file outside its folders (lchown)":           call(unix.SYS_LCHOWN, uid, gid),
		"set the times of a file outside its folders (utime)": call(unix.SYS_UTIME, 0, 0),
	}
	fd, _, e := unix.Syscall(unix.SYS_INOTIFY_INIT, 0, 0, 0)
	if e == 0 {
		unix.Close(int(fd))
	}
	out["start a watch (inotify_init)"] = errnoErr(e)
	return out
}
