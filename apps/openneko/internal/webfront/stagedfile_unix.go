//go:build !windows

package webfront

import (
	"io"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const stagedOpenFlags = syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

func singleLink(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1
}

// hasHole tells whether a file of size bytes has a hole before its end. A
// file system without hole reports puts the first hole at the end.
func hasHole(f *os.File, size int64) bool {
	hole, err := f.Seek(0, unix.SEEK_HOLE)
	_, _ = f.Seek(0, io.SeekStart)
	return err == nil && hole < size
}
