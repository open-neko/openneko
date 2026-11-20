//go:build !windows

package serve

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// Listen replaces a stale socket at path and listens on it, readable and
// writable by the owner and group only.
func Listen(path string) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	old := syscall.Umask(0o117)
	listener, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("serve socket unavailable: %w", err)
	}
	return listener, nil
}
