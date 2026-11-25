package llm

import (
	"testing"
)

func TestCatalogIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range ModelProviders {
		if seen[p.Value] {
			t.Errorf("provider %s is listed twice", p.Value)
		}
		seen[p.Value] = true
		if !IsModelProvider(p.Value) || p.Label == "" || p.Description == "" {
			t.Errorf("provider %s is not complete: %+v", p.Value, p)
		}
		if DefaultModel(p.Value) == "" && p.Value != "openai-compatible" {
			t.Errorf("provider %s has no default model", p.Value)
		}
		if len(Fields(p.Value)) == 0 {
			t.Errorf("provider %s has no form fields", p.Value)
		}
	}
	if len(defaultModels) != len(PrimaryProviders) || len(recordDefaultModels) != len(RecordProviders) {
		t.Fatalf("default models: %d for %d primary providers, %d for %d record providers",
			len(defaultModels), len(PrimaryProviders), len(recordDefaultModels), len(RecordProviders))
	}
	for _, p := range RecordProviders {
		if IsPrimaryProvider(p.Value) {
			t.Errorf("record provider %s can be the primary provider", p.Value)
		}
	}
	tasks := map[string]bool{}
	for _, task := range Tasks {
		if tasks[task.Task] || task.Label == "" || task.Description == "" || task.Task == DefaultRoute.Task {
			t.Errorf("task %+v is not valid", task)
		}
		tasks[task.Task] = true
	}
	if !IsTask(DecideTask) {
		t.Error("the Decisions task is not in the task list")
	}
}

func TestIsDecisionModelAsksAx(t *testing.T) {
	for _, c := range []struct {
		provider, model string
		want            bool
	}{
		{"google-gemini-decisions", "gemini-decision-1", true},
		{"openai-decisions", "gpt-6-luna", true},
		{"typesafe", "jev-latest", true},
		{"google-gemini", "gemini-3.8-flash", false},
		{"anthropic", "claude-opus-4-7", false},
		{"openai", "gpt-4.1-mini", false},
	} {
		if got := IsDecisionModel(c.provider, c.model, nil); got != c.want {
			t.Errorf("%s %s: IsDecisionModel = %v, want %v", c.provider, c.model, got, c.want)
		}
	}
	if IsDecisionModel("ollama", "llama3.2", map[string]any{"url": "http://localhost:11434"}) {
		t.Error("an Ollama text model counts as a decision model")
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
