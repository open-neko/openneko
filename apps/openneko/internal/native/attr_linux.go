package native

import "syscall"

var parentWatch = false

func childAttr() *syscall.SysProcAttr {
	if parentWatch {
		return &syscall.SysProcAttr{Setpgid: true}
	}
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
