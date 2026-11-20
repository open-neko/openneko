//go:build !linux

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
