package confine

import "fmt"

const runnerMechanism = "landlock+seccomp"

// runnerGaps names what the agent runner keeps. Its seccomp filter closes,
// on every ABI: sockets, new processes, the reach into and signals to other
// processes, truncate and metadata changes by path, and path watches.
func runnerGaps(abi int) []string {
	out := []string{"the metadata (size, times, extended attributes) of a path it names"}
	if abi < 2 {
		out = append(out, "a rename or link between folders fails (ABI 2)")
	}
	if abi < 5 {
		out = append(out, "ioctl on a device it opened (ABI 5)")
	}
	return out
}

// RunnerAvailable tells whether this system can confine an agent runner,
// with no change to the process.
func RunnerAvailable() State {
	st := Available()
	st.Mechanism = runnerMechanism
	if !st.Confined {
		return st
	}
	st.Gaps = runnerGaps(st.ABI)
	if err := seccompAvailable(); err != nil {
		st.Confined = false
		st.Reason = fmt.Sprintf("Linux %s refuses %v", kernel(), err)
		if InContainer() {
			st.Reason = fmt.Sprintf("the container engine refuses %v: use an engine whose default seccomp profile allows the seccomp call (INSTALL.md, \"Agent confinement\")", err)
		}
	}
	return st
}

// ApplyRunner confines an agent runner for good: Landlock keeps the paths of
// spec and refuses TCP, and the seccomp filter of runnerFilter does the rest.
// On an error the process may hold part of the confinement and must not run
// agent code.
func ApplyRunner(spec Spec) (State, error) {
	if avail := RunnerAvailable(); !avail.Confined {
		return avail, fmt.Errorf("%w: %s", ErrUnsupported, avail.Reason)
	}
	spec.NoTCP = true
	st := Available()
	if !skipLandlock {
		var err error
		if st, err = Apply(spec); err != nil {
			st.Mechanism = runnerMechanism
			return st, err
		}
	}
	st.Mechanism = runnerMechanism
	if skipSeccomp {
		return st, nil
	}
	if err := refuseCalls(); err != nil {
		st.Confined, st.Reason = false, err.Error()
		return st, err
	}
	st.Gaps = runnerGaps(st.ABI)
	return st, nil
}
