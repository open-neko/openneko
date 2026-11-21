package confine

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const runnerMechanism = "seatbelt"

// Available tells whether serve itself can be confined. On macOS only the
// agent runner is confined.
func Available() State {
	return State{Reason: "openneko serve's own confinement on macOS is not built yet"}
}

func Kernel() string {
	if v, err := unix.Sysctl("kern.osproductversion"); err == nil && v != "" {
		return "macOS " + v
	}
	return "macOS"
}

func Apply(Spec) (State, error) {
	st := Available()
	return st, fmt.Errorf("%w: %s", ErrUnsupported, st.Reason)
}

func Program(path string) (exec, read []string) { return []string{path}, nil }

func InContainer() bool { return false }

func LSMList() (string, error) { return "", ErrUnsupported }

var runnerGaps = []string{"the metadata (size, times) of a path it names"}

// RunnerAvailable tells whether this system can confine an agent runner,
// with no change to the process.
func RunnerAvailable() State {
	st := State{Mechanism: runnerMechanism}
	if noSandboxInit || lookup("sandbox_init") == 0 {
		st.Reason = Kernel() + " has no sandbox_init, the call that confines an agent runner"
		return st
	}
	st.Confined, st.Gaps = true, runnerGaps
	return st
}

// ApplyRunner confines an agent runner for good with a Seatbelt profile
// that refuses by default: no network, no other program, no signal or
// inspection of another process, and only the paths of spec. On an error
// the process must not run agent code.
func ApplyRunner(spec Spec) (State, error) {
	st := RunnerAvailable()
	if !st.Confined {
		return st, fmt.Errorf("%w: %s", ErrUnsupported, st.Reason)
	}
	profile, err := runnerProfile(spec)
	if err == nil {
		err = sandboxInit(profile)
	}
	if err != nil {
		st.Confined, st.Gaps, st.Reason = false, nil, err.Error()
		return st, err
	}
	return st, nil
}

// The read of "/" itself is needed: without it the CONF0 probe child dies
// at start on macOS 15.5. The sysctl read of kern.procargs2 gives another
// process's arguments and environment.
const runnerBase = `(version 1)
(deny default)
(allow file-read* (literal "/"))
(allow file-read* (subpath "/usr/lib") (subpath "/System/Library") (subpath "/System/Volumes/Preboot/Cryptexes") (subpath "/private/var/db/dyld") (literal "/dev/null") (literal "/dev/urandom") (literal "/dev/random"))
(allow file-read-metadata)
(allow sysctl-read)
(deny sysctl-read (sysctl-name-prefix "kern.procargs"))
(allow signal (target self))
(allow process-info* (target self))
`

func runnerProfile(spec Spec) (string, error) {
	var b strings.Builder
	b.WriteString(runnerBase)
	for _, rule := range []struct {
		allow, format string
		paths         []string
	}{
		{"file-read*", "(subpath %s)", spec.Read},
		{"file-read* file-write*", "(subpath %s)", spec.ReadWrite},
		{"file-read* process-exec", "(literal %s)", spec.Exec},
		{"network-outbound", "(remote unix-socket (path-literal %s))", spec.Sockets},
	} {
		for _, p := range rule.paths {
			if !filepath.IsAbs(p) {
				return "", fmt.Errorf("the confinement path %q is not absolute", p)
			}
			for _, q := range realPaths(p) {
				lit, err := sbplString(q)
				if err != nil {
					return "", err
				}
				fmt.Fprintf(&b, "(allow %s "+rule.format+")\n", rule.allow, lit)
			}
		}
	}
	b.WriteString(loosenForTest)
	return b.String(), nil
}

// realPaths gives the path as named and as Seatbelt sees it: macOS matches
// rules on the resolved path, and /var, /tmp and /etc are links to /private.
func realPaths(p string) []string {
	p = filepath.Clean(p)
	out := []string{p}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
		out = append(out, r)
	}
	return out
}

func sbplString(p string) (string, error) {
	if !utf8.ValidString(p) || strings.ContainsAny(p, "\"\\") || strings.IndexFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("the confinement path %q holds a character that the Seatbelt profile cannot carry", p)
	}
	return `"` + p + `"`, nil
}
