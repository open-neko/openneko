package serve

import (
	"fmt"
	"net"
	"os"
)

// Listen replaces a stale socket at path and listens on it. Windows has no
// umask: the socket file takes the access list of its folder.
func Listen(path string) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("serve socket unavailable: %w", err)
	}
	return listener, nil
}
