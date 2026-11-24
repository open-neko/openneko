package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestComposeEnvSetsTheOptionalSecretsThatAreUnset(t *testing.T) {
	got := ComposeEnv([]string{"PATH=/bin", "OPENNEKO_SANDBOX_KEY=k"})
	if !slices.Contains(got, "OPENNEKO_SANDBOX_KEY=k") || slices.Contains(got, "OPENNEKO_SANDBOX_KEY=") || !slices.Contains(got, "NEKO_EMBEDDING_API_KEY=") {
		t.Errorf("ComposeEnv = %v", got)
	}
}

func TestComposeEnvKeepsTheSandboxKeyBetweenRuns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := &Supervisor{RuntimeDir: t.TempDir()}

	s.composeEnv([]string{"PATH=/bin", "OPENNEKO_SANDBOX_KEY=k1"})
	raw, err := os.ReadFile(filepath.Join(s.RuntimeDir, optionalSecretsFile))
	if err != nil || strings.Contains(string(raw), "k1") {
		t.Fatalf("saved file = %q, %v; want the key encrypted", raw, err)
	}
	if got := s.composeEnv([]string{"PATH=/bin"}); !slices.Contains(got, "OPENNEKO_SANDBOX_KEY=k1") {
		t.Errorf("unset variable: %v; want the saved key", got)
	}
	if got := s.composeEnv([]string{"OPENNEKO_SANDBOX_KEY=k2"}); !slices.Contains(got, "OPENNEKO_SANDBOX_KEY=k2") {
		t.Errorf("new key: %v", got)
	}
	if got := s.composeEnv(nil); !slices.Contains(got, "OPENNEKO_SANDBOX_KEY=k2") {
		t.Errorf("after a new key: %v; want k2", got)
	}
	s.composeEnv([]string{"OPENNEKO_SANDBOX_KEY="})
	if got := s.composeEnv(nil); !slices.Contains(got, "OPENNEKO_SANDBOX_KEY=") {
		t.Errorf("after an empty key: %v; want the key off", got)
	}
}
