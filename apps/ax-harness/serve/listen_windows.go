package serve

import (
	"fmt"
	"net"
	"os"

	"github.com/open-neko/openneko/apps/ax-harness/ownacl"
)

// Listen replaces a stale socket at path and listens on it. Windows has no
// umask: the socket file takes the access list of its folder, so Listen
// limits the folder to the current account first.
func Listen(path string) (net.Listener, error) {
	if err := ownacl.SocketFolder(path); err != nil {
		return nil, fmt.Errorf("serve socket unavailable: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("serve socket unavailable: %w", err)
	}
	return listener, nil
}
