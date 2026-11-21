package llm

import (
	"net"

	"github.com/open-neko/openneko/apps/ax-harness/ownacl"
)

func listenSocket(path string) (net.Listener, error) {
	if err := ownacl.SocketFolder(path); err != nil {
		return nil, err
	}
	return net.Listen("unix", path)
}
