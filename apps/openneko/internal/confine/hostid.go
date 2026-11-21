package confine

import (
	"os"
	"strings"
)

// HostID names this machine for a setting that must not move to another
// host. Read it before confinement: a confined process may not reach it.
func HostID() string {
	if id := strings.TrimSpace(machineID()); id != "" {
		return id
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		return "hostname:" + name
	}
	return ""
}
