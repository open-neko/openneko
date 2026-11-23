package cli

import (
	"bytes"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// guardAgentSocket makes serve the child subreaper of its own processes and
// refuses serve's own process tree on the agent socket: below Landlock ABI 9
// the network part and its children can connect to it. As the subreaper,
// serve adopts each orphan of the network part, so a double fork does not
// leave the tree. It must run before serve starts a child.
func guardAgentSocket() (func(pid int) bool, error) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return nil, fmt.Errorf("make openneko serve the subreaper of its children: %w", err)
	}
	self := os.Getpid()
	return func(pid int) bool { return pid != 0 && descendsFrom(pid, self) }, nil
}

const maxPeerDepth = 64

// descendsFrom tells whether pid is ancestor or one of its children, by the
// parent links in /proc. A chain longer than maxPeerDepth counts as a child.
// When a link breaks because a process on the chain exited, the walk starts
// again: its children have a new parent by then. A link that stays broken
// at the same process is a process this account cannot see, which serve's
// tree never holds. A chain that keeps changing counts as a child.
func descendsFrom(pid, ancestor int) bool {
	broken := -1
	for range 16 {
		in, at := walkParents(pid, ancestor)
		if at < 0 {
			return in
		}
		if at == broken {
			return false
		}
		broken = at
	}
	return true
}

// walkParents returns the process whose parent it could not read, or -1.
func walkParents(pid, ancestor int) (bool, int) {
	for range maxPeerDepth {
		if pid == ancestor {
			return true, -1
		}
		if pid <= 1 {
			return false, -1
		}
		parent, err := parentOf(pid)
		if err != nil {
			return false, pid
		}
		pid = parent
	}
	return true, -1
}

var parentOf = procParent

func procParent(pid int) (int, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, fmt.Errorf("no name in /proc/%d/stat", pid)
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 2 {
		return 0, fmt.Errorf("no parent in /proc/%d/stat", pid)
	}
	return strconv.Atoi(string(f[1]))
}
