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
		`{"javascriptCode":"const first=read({step:1}); console.log('noise-'.repeat(3000),first.label);"}`,
		`{"javascriptCode":"const approval=propose({action:'record.update',arguments:{id:'R-42'},summary:'Update R-42'}); console.log(approval.id);"}`,
		`{"javascriptCode":"const second=read({step:2}); console.log('noise-'.repeat(3000),second.label);"}`,
		`{"javascriptCode":"const third=read({step:3}); console.log('noise-'.repeat(3000),third.label);"}`,
		`{"javascriptCode":"const fourth=read({step:4}); console.log('noise-'.repeat(3000),fourth.label);"}`,
		`{"javascriptCode":"const fifth=read({step:5}); console.log('noise-'.repeat(3000),fifth.label);"}`,
		`{"javascriptCode":"const saved=harnessSavedOperation(2); final('Report the pending approval',{approvalId:saved.result.id});"}`,
		`Answer: Approval approval-1 is pending; no update was executed.`,
	}
	var mu sync.Mutex
	var requests []string
	ordinaryCalls := 0
	summaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		isSummary := strings.Contains(string(body), "You are an internal AxAgent trajectory summarizer")
		index := ordinaryCalls
		if isSummary {
			summaryCalls++
		} else {
			ordinaryCalls++
		}
		mu.Unlock()
		if !isSummary && index >= len(answers) {
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		content := "Objective: Prepare and report the pending update.\nCurrent state and artifacts: approval-1 is pending.\nExact callables and formats: read; propose; harnessSavedOperation.\nEvidence: record R-42.\nUser constraints and preferences: " + constraint + ".\nFailures to avoid: Do not execute the update.\nNext step: report pending approval."
		if !isSummary {
			content = answers[index]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", content), "finish_reason", "stop"))))
	}))
	defer server.Close()
	provider := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	router, err := ax.NewMultiServiceRouter([]ax.Value{ax.RouterServiceEntry{Key: "work", Service: provider}})
	if err != nil {
		t.Fatal(err)
	}
	client := &RoutedClient{AIClient: router, Stages: StageModels{Context: "work", Executor: "work", Responder: "work"}}
	reads, proposals := 0, 0
	var events []Event
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
		MaxOperations: 8, MaxModelCalls: 20}, client, tools, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil || result.Status != "completed" || result.Kind != "approval" || len(result.Proposals) != 1 ||
		result.Proposals[0].ID != "approval-1" || reads != 5 || proposals != 1 || ordinaryCalls != len(answers) || summaryCalls == 0 {
		for i, request := range requests {
			t.Logf("request %d bytes=%d checkpoint=%v summary=%v responder=%v approval=%v", i+1, len(request),
				strings.Contains(request, "Working Code State (verbatim)"), strings.Contains(strings.ToLower(request), "summariz"),
				strings.Contains(request, "\"answer\""), strings.Contains(request, "approval-1"))
		}
		t.Fatalf("result=%+v err=%v reads=%d proposals=%d calls=%d", result, err, reads, proposals, ordinaryCalls)
	}
	mu.Lock()
	seen := append([]string(nil), requests...)
	mu.Unlock()
	approvalVisibleAfter := false
	largest := 0
	checkpointVisible := false
	for i, request := range seen {
		checkpointVisible = checkpointVisible || strings.Contains(request, "Working Code State (verbatim)")
		if !strings.Contains(request, constraint) {
			t.Fatalf("request %d lost the original constraint", i+1)
		}
		// Ax 25 summarizes runtime bindings by shape, so the receipt status is
		// read on demand; the id and the binding must stay in context.
		if i > 2 && strings.Contains(request, "approval-1") && strings.Contains(request, "approval: object") {
			approvalVisibleAfter = true
		}
		if len(request) > largest {
			largest = len(request)
		}
	}
	if !checkpointVisible || !approvalVisibleAfter || largest > 50_000 {
		t.Fatalf("checkpoint/approval was lost or model context grew without bound: checkpoint=%v approval=%v largest=%d", checkpointVisible, approvalVisibleAfter, largest)
	}
	starts := 0
	for _, event := range events {
		if event.Type == "model.request.started" {
			starts++
		}
	}
	if starts != len(seen) {
		t.Fatalf("model admissions=%d, HTTP requests=%d; internal summarization bypassed the budget", starts, len(seen))
	}
	t.Logf("ordinary calls=%d summary calls=%d checkpointVisible=%v largest request=%d bytes", ordinaryCalls, summaryCalls, checkpointVisible, largest)
}
