//go:build unix

package native

import (
	"io"
	"os"
	"strconv"
	"syscall"
)

// FollowSupervisor sends this process SIGTERM when the supervisor that
// started it exits. Outside a supervisor it does nothing.
func FollowSupervisor() {
	fd, err := strconv.Atoi(os.Getenv(SupervisorFDEnv))
	_ = os.Unsetenv(SupervisorFDEnv)
	if err != nil || fd < 3 {
		return
	}
	syscall.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), "supervisor")
	go func() {
		_, _ = io.Copy(io.Discard, f)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
}
