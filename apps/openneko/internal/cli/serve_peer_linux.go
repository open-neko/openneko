package cli

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
)

// refuseNetworkPeers refuses the network part of serve and its children on
// the agent socket: below Landlock ABI 9 they can connect to it.
func refuseNetworkPeers(network int) func(pid int) bool {
	return func(pid int) bool { return descendsFrom(pid, network) }
}

// descendsFrom tells whether pid is ancestor or one of its children, by the
// parent links in /proc.
func descendsFrom(pid, ancestor int) bool {
	for range 64 {
		if pid == ancestor {
			return true
		}
		if pid <= 1 {
			return false
		}
		parent, err := parentOf(pid)
		if err != nil {
			return false
		}
		pid = parent
	}
	return false
}

func parentOf(pid int) (int, error) {
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
