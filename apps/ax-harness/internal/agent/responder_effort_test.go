package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The host's default responder effort is "low". Ax maps the name to each
// provider's own level; Together has no level below "high", so its responder
// keeps the level of the main effort.
func TestResponderEffortLowReachesEachProviderAsItsOwnLevel(t *testing.T) {
	want := map[string]struct{ low, medium string }{
		"anthropic":         {`"thinking":{"type":"enabled","budget_tokens":5000}`, `"thinking":{"type":"enabled","budget_tokens":10000}`},
		"deepseek":          {`"reasoning_effort":"medium"`, `"reasoning_effort":"high"`},
		"google-gemini":     {`"thinkingConfig":{"thinkingLevel":"low","includeThoughts":true}`, `"thinkingConfig":{"thinkingLevel":"medium","includeThoughts":true}`},
		"groq":              {`"reasoning_effort":"low"`, `"reasoning_effort":"medium"`},
		"ollama":            {`"reasoning_effort":"medium"`, `"reasoning_effort":"high"`},
		"openai-compatible": {`"reasoning_effort":"low"`, `"reasoning_effort":"medium"`},
		"openai":            {`"reasoning_effort":"low"`, `"reasoning_effort":"medium"`},
		"openrouter":        {`"reasoning":{"effort":"low"}`, `"reasoning":{"effort":"medium"}`},
		"together":          {`"reasoning_effort":"high"`, `"reasoning_effort":"high"`},
		"x-grok":            {`"reasoning_effort":"low"`, `"reasoning_effort":"high"`},
	}
	paths, _ := filepath.Glob("testdata/thinking/*.json")
	if len(paths) != len(want) {
		t.Fatalf("%d provider fixtures, %d expectations", len(paths), len(want))
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture thinkingFixture
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatal(err)
		}
		levels, ok := want[fixture.Name]
		if !ok {
			t.Fatalf("no expectation for %s", fixture.Name)
		}
		for responder, field := range map[string]string{"low": levels.low, "": levels.medium} {
			var streamed []string
			server := thinkingServer(t, fixture, &streamed)
			result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "effort-" + fixture.Name, InputID: "i", Prompt: "How many orders?",
				ReasoningEffort: "medium", ResponderReasoningEffort: responder, StreamResponses: true}, thinkingClient(fixture, server.URL), Tools{}, func(Event) error { return nil })
			if err != nil || result.Status != "completed" {
				t.Fatalf("%s %q: %+v %v", fixture.Name, responder, result, err)
			}
			if len(streamed) != 1 || !strings.Contains(streamed[0], field) {
				t.Fatalf("%s responder effort %q: the request lacks %s: %v", fixture.Name, responder, field, streamed)
			}
		}
	}
}
