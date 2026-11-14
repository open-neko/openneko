package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoopbackRewriteOnlyInAContainer(t *testing.T) {
	marker := filepath.Join(t.TempDir(), ".dockerenv")
	old := dockerEnvFile
	dockerEnvFile = marker
	t.Cleanup(func() { dockerEnvFile = old })
	resolve := func() string {
		rt, err := ResolveRuntime("ollama", "m", map[string]any{"url": "http://127.0.0.1:11434"})
		if err != nil {
			t.Fatal(err)
		}
		return rt.BaseURL
	}
	for _, c := range []struct {
		env    string
		marker bool
		want   string
	}{
		{"", false, "http://127.0.0.1:11434/v1"},
		{"", true, "http://host.docker.internal:11434/v1"},
		{"false", true, "http://127.0.0.1:11434/v1"},
		{"1", false, "http://host.docker.internal:11434/v1"},
	} {
		t.Setenv("OPENNEKO_IN_CONTAINER", c.env)
		_ = os.Remove(marker)
		if c.marker {
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := resolve(); got != c.want {
			t.Errorf("OPENNEKO_IN_CONTAINER=%q marker=%v: %s, want %s", c.env, c.marker, got, c.want)
		}
	}
	t.Setenv("OPENNEKO_IN_CONTAINER", "")
	rt, err := ResolveRuntime("openai-compatible", "m", map[string]any{"url": "http://localhost:8000/v1"})
	if err != nil || rt.BaseURL != "http://localhost:8000/v1" || rt.Endpoint != (Endpoint{"localhost", 8000}) {
		t.Errorf("a native process keeps localhost: %+v %v", rt, err)
	}
}
