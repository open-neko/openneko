//go:build !linux && !windows

package serve

import "syscall"

// Pdeathsig is Linux only. The macOS runner profile refuses process-fork,
// so a runner left by a killed serve has no children.
func runnerAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
