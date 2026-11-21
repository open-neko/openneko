//go:build linux || darwin

package webfront

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	openNekoBuild     sync.Once
	openNekoBinary    string
	openNekoBuildFail string
)

// releaseBinary builds openneko as the release does, with CGO_ENABLED=0.
func releaseBinary(t *testing.T) string {
	t.Helper()
	openNekoBuild.Do(func() {
		dir, err := os.MkdirTemp("", "openneko-bin-")
		if err != nil {
			openNekoBuildFail = err.Error()
			return
		}
		openNekoBinary = filepath.Join(dir, "openneko")
		cmd := exec.Command("go", "build", "-o", openNekoBinary, "./cmd/openneko")
		cmd.Dir = "../.."
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			openNekoBuildFail = string(out)
		}
	})
	if openNekoBuildFail != "" {
		t.Fatalf("openneko build: %s", openNekoBuildFail)
	}
	return openNekoBinary
}
