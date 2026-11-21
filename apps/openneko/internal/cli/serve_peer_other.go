//go:build !linux

package cli

// refuseNetworkPeers is nil here: the agent socket reads no peer process.
func refuseNetworkPeers(int) func(pid int) bool { return nil }
