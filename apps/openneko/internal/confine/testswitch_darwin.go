package confine

import "testing"

var (
	noSandboxInit bool
	loosenForTest string
)

// SetForTest lets a proof test act as a macOS with no sandbox_init, or add
// rules to the agent runner's profile. It panics outside a test binary.
func SetForTest(noSeatbelt bool, extraRules string) {
	if !testing.Testing() {
		panic("confine.SetForTest works only in a test binary")
	}
	noSandboxInit, loosenForTest = noSeatbelt, extraRules
}
