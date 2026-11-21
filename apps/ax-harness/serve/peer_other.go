//go:build !linux

package serve

import "net"

// peerPID names no process here: only Linux serve sets RefusePeer.
func peerPID(net.Conn) (int, bool) { return 0, false }
