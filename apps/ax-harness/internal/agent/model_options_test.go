package agent

import (
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestGeminiStagesUseNativeStructuredOutput(t *testing.T) {
	options := ax.Object()
	modelOptions(options, &RoutedClient{DefaultProvider: "google-gemini"}, "medium", "", false)
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
		modelOptions(options, &RoutedClient{DefaultProvider: tc.provider}, "medium", "minimal", false)
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
	modelOptions(options, &RoutedClient{DefaultProvider: "openai"}, "medium", "", false)
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

func TestExecutorThoughtsOnlyForStreamedSummaryProviders(t *testing.T) {
	for _, tc := range []struct {
		provider string
		streamed bool
		want     bool
	}{{"google-gemini", true, true}, {"anthropic", true, true}, {"google-gemini", false, false}, {"anthropic", false, false},
		{"openai", true, false}, {"deepseek", true, false}} {
		options := ax.Object()
		modelOptions(options, &RoutedClient{DefaultProvider: tc.provider}, "low", "", tc.streamed)
		executor, _ := options["executorOptions"].(map[string]ax.Value)
		context, _ := options["contextOptions"].(map[string]ax.Value)
		if got := executor["showThoughts"] == true && executor["stream"] == true; got != tc.want {
			t.Errorf("%s streamed=%v: executor %v, want thoughts %v", tc.provider, tc.streamed, executor, tc.want)
		}
		if context["showThoughts"] != nil {
			t.Errorf("%s: the context stage asks for thoughts", tc.provider)
		}
		if got := executorSummaries(&RoutedClient{DefaultProvider: tc.provider}, "low"); got != (tc.want || !tc.streamed && thoughtSummaries(tc.provider)) {
			t.Errorf("%s: executorSummaries %v", tc.provider, got)
		}
	}
}
