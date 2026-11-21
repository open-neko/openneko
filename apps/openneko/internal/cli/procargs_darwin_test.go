package cli

import "golang.org/x/sys/unix"

func darwinProcargs(pid int) ([]byte, error) { return unix.SysctlRaw("kern.procargs2", pid) }
