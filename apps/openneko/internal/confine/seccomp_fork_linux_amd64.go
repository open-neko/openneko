package confine

import "golang.org/x/sys/unix"

var forkCalls = []uintptr{unix.SYS_FORK, unix.SYS_VFORK}
