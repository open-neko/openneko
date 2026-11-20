package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNativeUpgradeNamesAnOlderSandboxImage(t *testing.T) {
	previous := listNativeSandboxImages
	t.Cleanup(func() { listNativeSandboxImages = previous })
	listNativeSandboxImages = func(context.Context) ([]string, error) {
		return []string{
			"ghcr.io/open-neko/neko-sandbox:v5.9.0",
			"ghcr.io/open-neko/neko-sandbox:v6.0.0",
			"openneko-sandbox:local",
		}, nil
	}
	var out bytes.Buffer
	warnStaleNativeSandbox(context.Background(), &out, normalizeUpgradeImageVersion("6.0.0\n"))
	got := out.String()
	if strings.Count(got, "warning:") != 1 || !strings.Contains(got, "neko-sandbox:v5.9.0") || !strings.Contains(got, "OPENNEKO_VERSION=v6.0.0") {
		t.Fatalf("warning = %q", got)
	}

	out.Reset()
	listNativeSandboxImages = func(context.Context) ([]string, error) { return nil, errors.New("no docker") }
	warnStaleNativeSandbox(context.Background(), &out, "v6.0.0")
	if out.Len() != 0 {
		t.Fatalf("warning without Docker = %q", out.String())
	}
}
