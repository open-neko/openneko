//go:build unix && !linux

package native

import "syscall"

// Pdeathsig is Linux only; here a child learns of the supervisor's exit
// from the pipe in FollowSupervisor.
var parentWatch = true

func childAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
