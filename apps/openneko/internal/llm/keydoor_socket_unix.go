//go:build !windows

package llm

import (
	"net"
	"syscall"
)

func listenSocket(path string) (net.Listener, error) {
	old := syscall.Umask(0o117)
	defer syscall.Umask(old)
	return net.Listen("unix", path)
}
