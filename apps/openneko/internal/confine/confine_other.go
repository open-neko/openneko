//go:build !linux && !darwin

package confine

import (
	"fmt"
	"runtime"
)

func Available() State {
	return State{Reason: fmt.Sprintf("confinement on %s is not built yet", runtime.GOOS)}
}

func Kernel() string { return runtime.GOOS }

func Apply(Spec) (State, error) {
	st := Available()
	return st, fmt.Errorf("%w: %s", ErrUnsupported, st.Reason)
}

func Program(path string) (exec, read []string) { return []string{path}, nil }

func RunnerAvailable() State { return Available() }

func ApplyRunner(Spec) (State, error) { return Apply(Spec{}) }

func InContainer() bool { return false }

func LSMList() (string, error) { return "", ErrUnsupported }

// SetForTest has nothing to set on this system.
func SetForTest(int, bool, bool) {}
