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
