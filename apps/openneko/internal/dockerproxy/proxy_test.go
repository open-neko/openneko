package dockerproxy

import (
	"os"
	"testing"
)

func TestFindRunningWorkerRecursionGuard(t *testing.T) {
	t.Setenv(EnvMarker, "1")
	if got := FindRunningWorker(); got != "" {
		t.Fatalf("expected '' when EnvMarker is set, got %q", got)
	}
}

func TestFindRunningWorkerNoDocker(t *testing.T) {
	// Force docker to fail by clobbering PATH so the binary isn't found.
	t.Setenv("PATH", "/nonexistent-path-for-dockerproxy-test")
	t.Setenv(EnvMarker, "")
	if got := FindRunningWorker(); got != "" {
		t.Fatalf("expected '' when docker is unreachable, got %q", got)
	}
}

// TestEnvMarkerConstant guards the marker name from accidental rename; the
// embedded worker binary must use the same constant to honor the recursion
// guard, and changing it would silently break the proxy.
func TestEnvMarkerConstant(t *testing.T) {
	if EnvMarker != "OPENNEKO_PROXIED" {
		t.Fatalf("EnvMarker changed; coordinate with worker binary build")
	}
	_ = os.Environ // keep import
}

func TestShouldAttachStdin(t *testing.T) {
	cases := []struct {
		name string
		tty  bool
		mode os.FileMode
		want bool
	}{
		{"interactive tty", true, os.ModeCharDevice, true},
		{"piped input", false, os.ModeNamedPipe, true},
		{"redirected file", false, 0, true}, // regular file
		{"dev/null char device", false, os.ModeDevice | os.ModeCharDevice, false},
		{"non-tty char device", false, os.ModeCharDevice, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAttachStdin(tc.tty, tc.mode); got != tc.want {
				t.Fatalf("shouldAttachStdin(%v, %v) = %v, want %v", tc.tty, tc.mode, got, tc.want)
			}
		})
	}
}

func TestDockerExecArgs(t *testing.T) {
	// Non-interactive, no stdin (the CI/script path that triggered the spurious
	// exit): neither -i nor -t.
	got := dockerExecArgs("w-1", []string{"secrets", "list"}, false, false)
	want := []string{"exec", "-e", EnvMarker + "=1", "w-1", "openneko", "secrets", "list"}
	if !equalStrings(got, want) {
		t.Fatalf("non-interactive args = %v, want %v", got, want)
	}
	// Interactive: -i and -t.
	got = dockerExecArgs("w-1", []string{"secrets", "set", "p", "K"}, true, true)
	if !contains(got, "-i") || !contains(got, "-t") {
		t.Fatalf("interactive args missing -i/-t: %v", got)
	}
	// Piped (attach but not a tty): -i, no -t.
	got = dockerExecArgs("w-1", []string{"x"}, true, false)
	if !contains(got, "-i") || contains(got, "-t") {
		t.Fatalf("piped args want -i without -t: %v", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
