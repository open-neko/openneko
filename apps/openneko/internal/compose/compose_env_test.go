package compose

import (
	"slices"
	"testing"
)

func TestComposeEnvSetsTheOptionalSecretsThatAreUnset(t *testing.T) {
	got := ComposeEnv([]string{"PATH=/bin", "OPENNEKO_SANDBOX_KEY=k"})
	if !slices.Contains(got, "OPENNEKO_SANDBOX_KEY=k") || slices.Contains(got, "OPENNEKO_SANDBOX_KEY=") || !slices.Contains(got, "NEKO_EMBEDDING_API_KEY=") {
		t.Errorf("ComposeEnv = %v", got)
	}
}
