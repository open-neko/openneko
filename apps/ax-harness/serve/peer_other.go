//go:build !linux

package serve

import "net"

// peer names no process here: only Linux serve sets RefusePeer.
func peer(net.Conn) (int, func() bool, bool) { return 0, nil, false }
