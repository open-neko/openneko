//go:build !linux

package cli

// guardAgentSocket refuses no peer here: the agent socket reads no peer
// process.
func guardAgentSocket() (func(pid int) bool, error) { return nil, nil }
