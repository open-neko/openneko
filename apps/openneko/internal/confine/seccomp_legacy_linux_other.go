//go:build linux && !amd64

package confine

var forkCalls, legacyRefusedCalls []uintptr
