package serve

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// soPeerPidfd is SO_PEERPIDFD (Linux 6.5), the same on amd64 and arm64.
const soPeerPidfd = 77

// peer names the process at the other end of a Unix socket; pid 0 is a
// process outside this process's pid namespace, such as web in another
// container. alive tells whether that same process still runs, so a check
// by pid cannot pass for a process that exited and whose pid came back.
func peer(conn net.Conn) (pid int, alive func() bool, ok bool) {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return 0, nil, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, nil, false
	}
	var cred *unix.Ucred
	pidfd := -1
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		if cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); cerr == nil {
			if v, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, soPeerPidfd); err == nil {
				pidfd = v
			}
		}
	}); err != nil || cerr != nil {
		return 0, nil, false
	}
	pid = int(cred.Pid)
	if pidfd < 0 && pid > 0 {
		if pidfd, err = unix.PidfdOpen(pid, 0); err != nil {
			return pid, func() bool { return false }, true
		}
	}
	if pidfd < 0 {
		return pid, func() bool { return true }, true
	}
	return pid, func() bool {
		defer unix.Close(pidfd)
		return !errors.Is(unix.PidfdSendSignal(pidfd, 0, nil, 0), unix.ESRCH)
	}, true
}
