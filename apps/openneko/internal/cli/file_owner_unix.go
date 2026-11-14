//go:build !windows

package cli

import (
	"io/fs"
	"syscall"
)

func fileOwner(info fs.FileInfo) (int, int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
