package budgettriage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
)

func TestNativeTypesafeShadowTriageUsesFullDistribution(t *testing.T) {
	for _, tc := range []struct {
		name, choice, wantProfile, wantReason string
		probabilities                         map[string]float64
	}{
		{"clear short", "short_answer", "short", "classified", map[string]float64{"short_answer": .90, "multi_step": .05, "artifact_pipeline": .03, "uncertain": .02}},
		{"misleading short prompt", "artifact_pipeline", "artifact", "classified", map[string]float64{"short_answer": .04, "multi_step": .16, "artifact_pipeline": .75, "uncertain": .05}},
		{"split short and complex", "short_answer", "fixed", "low_confidence", map[string]float64{"short_answer": .55, "multi_step": .35, "artifact_pipeline": .05, "uncertain": .05}},
		{"uncertain", "uncertain", "fixed", "uncertain", map[string]float64{"short_answer": .20, "multi_step": .20, "artifact_pipeline": .20, "uncertain": .40}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer synthetic" {
					t.Errorf("unexpected native request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
				}
				var request ax.TypesafeRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "jev-fixture" || request.Questions["workload"].Type != "choice" {
					t.Errorf("invalid native choice request: %+v %v", request, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"workload": map[string]any{
					"type": "choice", "choice": tc.choice, "confidence": tc.probabilities[tc.choice], "probabilities": tc.probabilities}},
					"usage": map[string]int{"input_tokens": 100, "output_tokens": 20}})
			}))
			defer server.Close()
			client := ax.Typesafe(ax.Object("api_key", "synthetic", "base_url", server.URL, "model", "jev-fixture", "retry", ax.Object("maxRetries", 0)))
			input := Input{Summary: "Produce the requested result", ArtifactRequested: tc.name == "misleading short prompt", ToolFamilies: []string{"graphjin", "file"}, InputBytes: 180}
			observed, err := Evaluate(context.Background(), client, "jev-fixture", input,
				agent.TokenPrice{InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}, 10_000)
			if err != nil || calls != 1 || observed.SuggestedProfile != tc.wantProfile || observed.Reason != tc.wantReason ||
				observed.InputTokens != 100 || observed.OutputTokens != 20 || observed.Coverage != "complete" ||
				observed.ChargedMicros != 512 || len(observed.Probabilities) != 4 {
				t.Fatalf("observation=%+v err=%v calls=%d", observed, err, calls)
			}
			encoded, _ := json.Marshal(observed)
			if strings.Contains(string(encoded), input.Summary) || strings.Contains(string(encoded), "task_summary") {
				t.Fatal("telemetry leaked classifier input")
			}
		})
	}
}

func TestTriagePreflightAndUnavailableFallback(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := ax.Typesafe(ax.Object("api_key", "synthetic", "base_url", server.URL, "model", "jev-fixture", "retry", ax.Object("maxRetries", 0)))
	input := Input{Summary: "Investigate and report", ToolFamilies: []string{"graphjin"}, InputBytes: 300}
	price := agent.TokenPrice{InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}
	denied, err := Evaluate(context.Background(), client, "jev-fixture", input, price, 511)
	if err != nil || calls != 0 || denied.SuggestedProfile != "fixed" || denied.Reason != "budget_denied" || denied.ChargedMicros != 0 {
		t.Fatalf("preflight=%+v err=%v calls=%d", denied, err, calls)
	}
	unavailable, err := Evaluate(context.Background(), client, "jev-fixture", input, price, 1_000)
	if err != nil || calls != 1 || unavailable.SuggestedProfile != "fixed" || unavailable.Reason != "classifier_unavailable" ||
		unavailable.ChargedMicros != 512 || unavailable.Coverage != "unavailable" {
		t.Fatalf("fallback=%+v err=%v calls=%d", unavailable, err, calls)
	}
	if _, err = Evaluate(context.Background(), client, "jev-fixture", Input{Summary: strings.Repeat("x", 2049)}, price, 1_000); err == nil || calls != 1 {
		t.Fatal("unbounded classifier state was dispatched")
	}
}
