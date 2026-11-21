package confine

import "golang.org/x/sys/unix"

var forkCalls = []uintptr{unix.SYS_FORK, unix.SYS_VFORK}

var legacyRefusedCalls = []uintptr{
	unix.SYS_CHMOD, unix.SYS_CHOWN, unix.SYS_LCHOWN,
	unix.SYS_UTIME, unix.SYS_UTIMES, unix.SYS_FUTIMESAT,
	unix.SYS_INOTIFY_INIT,
}
