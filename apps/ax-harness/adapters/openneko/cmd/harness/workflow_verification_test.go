package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/openneko/apps/ax-harness/adapters/openneko/broker"
	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
	"github.com/open-neko/openneko/apps/ax-harness/internal/session"
)

func TestWorkflowCompletionNeedsBrokerConfirmedOutput(t *testing.T) {
	for _, emitOutput := range []bool{false, true} {
		name := "without output"
		if emitOutput {
			name = "with output"
		}
		t.Run(name, func(t *testing.T) {
			brokerCalls := 0
			brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				brokerCalls++
				if r.URL.Path != "/v1/harness/workflow-output/emit" || r.Header.Get("Authorization") != "Bearer scoped" {
					t.Errorf("unexpected broker request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"outputId":"output-1","kind":"finding"}`))
			}))
			defer brokerServer.Close()
			emit, err := broker.WorkflowOutput(brokerServer.URL, "scoped")
			if err != nil {
				t.Fatal(err)
			}
			tools := agent.Tools{}
			binding := ""
			tools.Capabilities = []agent.Capability{{Name: "workflow_output_emit", Version: "1", Origin: "openneko", Effect: "durable",
				Description: "Persist workflow output.", InputSchema: json.RawMessage(`{"type":"object","required":["kind"],"properties":{"kind":{"type":"string"}},"additionalProperties":false}`),
				Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
					return emit(ctx, raw, binding)
				}}}
			binding, err = tools.Binding("workflow_output_emit")
			if err != nil {
				t.Fatal(err)
			}
			bindWorkflowOutputVerification(&tools, binding)
			code := `final('Report completion',{});`
			if emitOutput {
				code = `const receipt=workflow_output_emit({kind:'finding'}); final('Report completion',{receipt});`
			}
			encoded, _ := json.Marshal(code)
			answers := []string{`{"javascriptCode":"final('Produce output',{})"}`, `{"javascriptCode":` + string(encoded) + `}`, `{"answer":"Workflow completed."}`}
			modelCalls := 0
			var modelRequests []string
			modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				modelRequests = append(modelRequests, string(body))
				if modelCalls >= len(answers) {
					http.Error(w, "unexpected model call", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[modelCalls]), "finish_reason", "stop"))))
				modelCalls++
			}))
			defer modelServer.Close()
			client := ax.NewOpenAICompatibleClient(ax.Object("base_url", modelServer.URL, "api_key", "synthetic", "model", "fixture"))
			spec := agent.Spec{Version: 1, RunID: name, InputID: "input", Prompt: "Produce workflow output"}
			var events []agent.Event
			result, err := session.RunWithTools(context.Background(), t.TempDir(), spec, client, tools, func(e agent.Event) error { events = append(events, e); return nil })
			if err != nil || modelCalls != 3 || brokerCalls != boolInt(emitOutput) {
				t.Fatalf("result=%+v err=%v model=%d broker=%d", result, err, modelCalls, brokerCalls)
			}
			if emitOutput && (result.Status != "completed" || result.Answer != "Workflow completed.") ||
				!emitOutput && (result.Status != "failed" || result.Code != "verification_failed") {
				t.Fatalf("unverified workflow completion: %+v", result)
			}
			if len(events) < 2 || events[len(events)-2].Type != "terminal.checked" || events[len(events)-1].Type != "run.finished" ||
				!strings.HasPrefix(events[len(events)-2].Origin, "openneko-workflow-output-") {
				t.Fatalf("missing durable terminal check: %+v", events)
			}
			updates, applied := 0, 0
			for _, event := range events {
				if event.Type == "runtime.state.updated" {
					updates++
					if event.StateUpdate == nil || event.StateUpdate.Target != "root/responder" || !strings.Contains(string(event.StateUpdate.State), `"output_id":"output-1"`) {
						t.Fatalf("incorrect state update: %+v", event)
					}
				}
				if event.Type == "runtime.state.applied" {
					applied++
					if event.OperationID != 1 || event.Origin != "next-response" {
						t.Fatalf("incorrect Ax application acknowledgement: %+v", event)
					}
				}
			}
			if updates != boolInt(emitOutput) || applied != boolInt(emitOutput) || emitOutput && (strings.Contains(modelRequests[0], "operation_id") || strings.Contains(modelRequests[1], "operation_id") || strings.Count(modelRequests[2], "operation_id") != 1) {
				t.Fatalf("workflow state not confined to responder: updates=%d applied=%d occurrences=%d,%d,%d", updates, applied,
					strings.Count(modelRequests[0], "operation_id"), strings.Count(modelRequests[1], "operation_id"), strings.Count(modelRequests[2], "operation_id"))
			}
		})
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
