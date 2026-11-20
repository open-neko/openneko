package llm

import "net"

func listenSocket(path string) (net.Listener, error) {
	return net.Listen("unix", path)
}
