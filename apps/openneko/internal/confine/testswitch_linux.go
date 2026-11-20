package confine

import "testing"

var skipLandlock, skipSeccomp bool

// SetForTest makes this process act as a kernel with Landlock ABI abi at
// most, and lets a proof test leave out Landlock or the seccomp filter of an
// agent runner. It panics outside a test binary.
func SetForTest(abi int, noLandlock, noSeccomp bool) {
	if !testing.Testing() {
		panic("confine.SetForTest works only in a test binary")
	}
	abiCap, skipLandlock, skipSeccomp = abi, noLandlock, noSeccomp
}
