//go:build unix

package native

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func detachedState() string {
	if sid, err := unix.Getsid(0); err == nil && sid == os.Getpid() {
		return "detached"
	}
	return "attached"
}

func withConsole(*testing.T) {}
