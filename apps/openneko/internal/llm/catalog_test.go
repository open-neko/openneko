package llm

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestCatalogMatchesTypeScriptFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/llm/test/fixtures/model-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	defaults := map[string]string{}
	fields := map[string][]Field{}
	for _, p := range PrimaryProviders {
		defaults[p.Value] = DefaultModel(p.Value)
		fields[p.Value] = Fields(p.Value)
	}
	mine, _ := json.Marshal(map[string]any{"providers": PrimaryProviders, "defaults": defaults, "fields": fields, "tasks": Tasks})
	_ = json.Unmarshal(mine, &got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Go catalogue differs from the TypeScript fixture:\n go  %s\n ts  %s", mine, raw)
	}
	if len(defaultModels) != len(PrimaryProviders) {
		t.Fatalf("%d default models for %d providers", len(defaultModels), len(PrimaryProviders))
	}
}

func TestMaskSecret(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "",
		"abc":                 "•••",
		"12345678":            "••••••••",
		"sk-123456789":        "sk-•••••6789",
		"sk-planted-4f1e9c2a": "sk-••••••••••••9c2a",
	} {
		if got := MaskSecret(in); got != want {
			t.Errorf("MaskSecret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveRuntime(t *testing.T) {
	t.Setenv("OPENNEKO_IN_CONTAINER", "1")
	for _, c := range []struct {
		provider string
		config   map[string]any
		base     string
		endpoint Endpoint
		err      string
	}{
		{"openai", nil, "https://api.openai.com/v1", Endpoint{Host: "api.openai.com"}, ""},
		{"ollama", map[string]any{"url": "http://localhost:11434"}, "http://host.docker.internal:11434/v1", Endpoint{"host.docker.internal", 11434}, ""},
		{"ollama", map[string]any{"url": "http://[::1]:11434/v1"}, "http://host.docker.internal:11434/v1", Endpoint{"host.docker.internal", 11434}, ""},
		{"openai-compatible", map[string]any{"url": "https://Models.Example.com/v1/"}, "https://models.example.com/v1", Endpoint{Host: "models.example.com"}, ""},
		{"openai-compatible", map[string]any{"url": "https://models.example.com:443/v1"}, "https://models.example.com/v1", Endpoint{Host: "models.example.com"}, ""},
		{"openai-compatible", map[string]any{"url": "http://models.example.com"}, "http://models.example.com", Endpoint{"models.example.com", 80}, ""},
		{"openai-compatible", map[string]any{"url": "https://u:p@models.example.com/v1"}, "", Endpoint{}, "OpenAI-compatible Base URL must not carry credentials, a query or a fragment."},
		{"openai-compatible", map[string]any{"url": "https://models.example.com/v1?x=1"}, "", Endpoint{}, "OpenAI-compatible Base URL must not carry credentials, a query or a fragment."},
		{"openai-compatible", map[string]any{"url": "ftp://models.example.com"}, "", Endpoint{}, "OpenAI-compatible Base URL must use http or https."},
		{"openai-compatible", map[string]any{"url": "not a url"}, "", Endpoint{}, "Invalid OpenAI-compatible Base URL: not a url"},
		{"ollama", nil, "", Endpoint{}, "Ollama Base URL is required."},
		{"azure-openai", map[string]any{"resourceName": "res", "deploymentName": "gpt"}, "https://res.openai.azure.com/openai/v1", Endpoint{Host: "res.openai.azure.com"}, ""},
		{"azure-openai", map[string]any{"resourceName": "bad_name", "deploymentName": "gpt"}, "", Endpoint{}, "Azure resource name contains invalid characters."},
		{"vertex", map[string]any{"projectId": "p-1", "region": "global"}, "https://aiplatform.googleapis.com/v1beta1/projects/p-1/locations/global/endpoints/openapi", Endpoint{Host: "aiplatform.googleapis.com"}, ""},
		{"vertex", map[string]any{"projectId": "p-1", "region": "us-east5"}, "https://us-east5-aiplatform.googleapis.com/v1beta1/projects/p-1/locations/us-east5/endpoints/openapi", Endpoint{Host: "us-east5-aiplatform.googleapis.com"}, ""},
	} {
		rt, err := ResolveRuntime(c.provider, "m", c.config)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("%s %v: error %v, want %q", c.provider, c.config, err, c.err)
			}
			continue
		}
		if err != nil || rt.BaseURL != c.base || rt.Endpoint != c.endpoint {
			t.Errorf("%s %v: %+v %v", c.provider, c.config, rt, err)
		}
	}
	for _, p := range PrimaryProviders {
		cfg := map[string]any{"url": "https://x.example.com/v1", "resourceName": "r", "deploymentName": "d", "projectId": "p", "region": "global"}
		if _, err := ResolveRuntime(p.Value, "m", cfg); err != nil {
			t.Errorf("%s: %v", p.Value, err)
		}
	}
}
