package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestMixedToolContextKeepsConstraintAndPendingApproval(t *testing.T) {
	const constraint = "Prepare the action, but never execute it without approval"
	answers := []string{
		`{"javascriptCode":"final('Inspect and prepare the action',{});"}`,
		`{"javascriptCode":"const first=read({step:1}); console.log(first.label);"}`,
		`{"javascriptCode":"const approval=propose({action:'record.update',arguments:{id:'R-42'},summary:'Update R-42'}); console.log(approval.id);"}`,
		`{"javascriptCode":"const second=read({step:2}); console.log(second.label);"}`,
		`{"javascriptCode":"const third=read({step:3}); console.log(third.label);"}`,
		`{"javascriptCode":"const fourth=read({step:4}); console.log(fourth.label);"}`,
		`{"javascriptCode":"const fifth=read({step:5}); console.log(fifth.label);"}`,
		`{"javascriptCode":"const saved=harnessSavedOperation(2); final('Report the pending approval',{approvalId:saved.result.id});"}`,
		`{"answer":"Approval approval-1 is pending; no update was executed."}`,
	}
	var mu sync.Mutex
	var requests []string
	ordinaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		isSummary := strings.Contains(string(body), "Working Code State (verbatim)")
		index := ordinaryCalls
		if !isSummary {
			ordinaryCalls++
		}
		mu.Unlock()
		if !isSummary && index >= len(answers) {
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		content := "Objective: prepare an approval for R-42. User constraint: " + constraint + ". The approved receipt is approval-1, pending_approval. No effect executed."
		if !isSummary {
			content = answers[index]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", content), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	reads, proposals := 0, 0
	tools := Tools{Propose: func(_ context.Context, proposal Proposal) (ProposalReceipt, error) {
		proposals++
		if proposal.Action != "record.update" {
			t.Errorf("wrong proposed action: %+v", proposal)
		}
		return ProposalReceipt{ID: "approval-1", Status: "pending_approval"}, nil
	}, Capabilities: []Capability{{Name: "read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read one record.",
		InputSchema: json.RawMessage(`{"type":"object","required":["step"],"properties":{"step":{"type":"integer"}},"additionalProperties":false}`),
		Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reads++
			return json.Marshal(ax.Object("label", "REF-42", "irrelevant", strings.Repeat("noise-", 1500)))
		}}}}
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "mixed-context", InputID: "input", Prompt: constraint,
		MaxOperations: 8, MaxModelCalls: 20}, client, tools, func(Event) error { return nil })
	if err != nil || result.Status != "completed" || result.Kind != "approval" || len(result.Proposals) != 1 ||
		result.Proposals[0].ID != "approval-1" || reads != 5 || proposals != 1 || ordinaryCalls != len(answers) {
		t.Fatalf("result=%+v err=%v reads=%d proposals=%d calls=%d", result, err, reads, proposals, ordinaryCalls)
	}
	mu.Lock()
	seen := append([]string(nil), requests...)
	mu.Unlock()
	approvalVisibleAfter := false
	largest := 0
	for i, request := range seen {
		if strings.Contains(request, "Working Code State (verbatim)") {
			continue
		}
		if !strings.Contains(request, constraint) {
			t.Fatalf("request %d lost the original constraint", i+1)
		}
		if i > 2 && strings.Contains(request, "approval-1") && strings.Contains(request, "pending_approval") {
			approvalVisibleAfter = true
		}
		if len(request) > largest {
			largest = len(request)
		}
	}
	if !approvalVisibleAfter || largest > 50_000 {
		t.Fatalf("approval was lost or model context grew without bound: visible=%v largest=%d", approvalVisibleAfter, largest)
	}
	t.Logf("ordinary calls=%d total requests=%d largest request=%d bytes", ordinaryCalls, len(seen), largest)
}
