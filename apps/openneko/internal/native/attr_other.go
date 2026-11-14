//go:build unix && !linux

package native

import "syscall"

func childAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
