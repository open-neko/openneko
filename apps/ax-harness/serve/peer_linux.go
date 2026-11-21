package serve

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID names the process at the other end of a Unix socket; 0 is a
// process outside this process's pid namespace, such as web in another
// container.
func peerPID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) { cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || cerr != nil {
		return 0, false
	}
	return int(cred.Pid), true
}
