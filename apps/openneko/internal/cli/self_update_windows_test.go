package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneSelfUpdateIsRefusedOnWindows(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "openneko.exe")
	if err := os.WriteFile(executable, []byte("old-cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := installStandaloneCLI(context.Background(), "v9.8.7", executable, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unsupported on windows") {
		t.Fatalf("error = %v, want the Windows refusal", err)
	}
	if got, err := os.ReadFile(executable); err != nil || string(got) != "old-cli" {
		t.Fatalf("the CLI changed: %q %v", got, err)
	}
}
