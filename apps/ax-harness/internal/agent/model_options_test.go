package agent

import (
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestGeminiStagesUseNativeStructuredOutput(t *testing.T) {
	options := ax.Object()
	modelOptions(options, &RoutedClient{DefaultProvider: "google-gemini"}, "medium", "")
	for _, key := range stageKeys {
		stage, _ := options[key].(map[string]ax.Value)
		if stage["structuredOutputMode"] != "native" {
			t.Fatalf("%s structuredOutputMode = %v, want native", key, stage["structuredOutputMode"])
		}
	}
	summarizer, _ := options["summarizerOptions"].(map[string]ax.Value)
	if options["structuredOutputMode"] != "native" || summarizer["structuredOutputMode"] != "native" {
		t.Fatalf("top-level and summarizer modes = %v, %v; want native", options["structuredOutputMode"], summarizer["structuredOutputMode"])
	}
}

func TestResponderEffortReplacesTheMainEffortForTheResponderOnly(t *testing.T) {
	for _, tc := range []struct{ provider, field string }{{"openai", "reasoning_effort"}, {"google-gemini", "thinkingTokenBudget"}} {
		options := ax.Object()
		modelOptions(options, &RoutedClient{DefaultProvider: tc.provider}, "medium", "minimal")
		for _, key := range stageKeys {
			stage, _ := options[key].(map[string]ax.Value)
			want := "medium"
			if key == "responderOptions" {
				want = "minimal"
			}
			if stage[tc.field] != want {
				t.Fatalf("%s %s %s = %v, want %s", tc.provider, key, tc.field, stage[tc.field], want)
			}
		}
		if tc.provider == "google-gemini" {
			responder, _ := options["responderOptions"].(map[string]ax.Value)
			if responder["showThoughts"] != true {
				t.Fatalf("gemini responder showThoughts = %v", responder["showThoughts"])
			}
		}
	}
	options := ax.Object()
	modelOptions(options, &RoutedClient{DefaultProvider: "openai"}, "medium", "")
	responder, _ := options["responderOptions"].(map[string]ax.Value)
	if responder["reasoning_effort"] != "medium" {
		t.Fatalf("responder effort without an override = %v, want medium", responder["reasoning_effort"])
	}
}

func TestSpecAcceptsMinimalForTheResponderOnly(t *testing.T) {
	base := Spec{Version: 1, RunID: "r", InputID: "i", Prompt: "p"}
	for _, tc := range []struct {
		main, responder string
		valid           bool
	}{{"", "minimal", true}, {"", "low", true}, {"medium", "high", true}, {"minimal", "", false}, {"", "none", false}, {"", "max", false}} {
		spec := base
		spec.ReasoningEffort, spec.ResponderReasoningEffort = tc.main, tc.responder
		if spec.valid() != tc.valid {
			t.Fatalf("effort %q responder %q valid = %v, want %v", tc.main, tc.responder, spec.valid(), tc.valid)
		}
	}
}
