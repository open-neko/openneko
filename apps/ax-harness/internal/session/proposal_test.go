package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
)

func proposalModel(t *testing.T, code string) (ax.AIClient, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	answers := []string{`{"javascriptCode":"final('Prepare the request',{})"}`, "", `{"answer":"Awaiting the recorded approval."}`}
	executor, _ := json.Marshal(map[string]string{"javascriptCode": code})
	answers[1] = string(executor)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if n >= len(answers) {
			t.Error("unexpected model request")
			http.Error(w, "unexpected", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[n]), "finish_reason", "stop"))))
	}))
	t.Cleanup(server.Close)
	return ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture")), &calls
}

const proposalCode = `const read=lookup('reference'); const approval=propose({action:'reference.update',arguments:{value:42},summary:'Update the reference'}); final('Answer',{read,approval});`

func TestProposalCheckpointIOFailureStopsDispatchAndReplay(t *testing.T) {
	for _, phase := range []string{"before_dispatch", "after_dispatch"} {
		t.Run(phase, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "session")
			backup := root + ".retained"
			broken := false
			breakStorage := func() {
				t.Helper()
				if err := os.Rename(root, backup); err != nil {
					t.Fatal(err)
				}
				// A file in place of the directory reliably fails checkpoint writes,
				// including when tests run with privileges that bypass permissions.
				if err := os.WriteFile(root, []byte("unavailable"), 0600); err != nil {
					t.Fatal(err)
				}
				broken = true
			}
			client, _ := proposalModel(t, `const approval=propose({action:'a',arguments:{},summary:'Prepare'}); final('Answer',{approval});`)
			spec := agent.Spec{Version: 1, RunID: "io-failure", InputID: "input", Prompt: "Prepare"}
			calls := 0
			tools := agent.Tools{Propose: func(context.Context, agent.Proposal) (agent.ProposalReceipt, error) {
				calls++
				if phase == "after_dispatch" {
					breakStorage()
				}
				return agent.ProposalReceipt{ID: "request", Status: "pending_approval"}, nil
			}}
			_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
				if phase == "before_dispatch" && e.Type == "tool.started" {
					breakStorage()
				}
				return nil
			})
			wantCalls := 0
			if phase == "after_dispatch" {
				wantCalls = 1
			}
			if err == nil || !broken || calls != wantCalls {
				t.Fatalf("err=%v broken=%v calls=%d want=%d", err, broken, calls, wantCalls)
			}
			if err := os.Remove(root); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backup, root); err != nil {
				t.Fatal(err)
			}
			// Restoring storage cannot establish whether an unfinished operation ran.
			// Resume must reject it before consulting a model or dispatching again.
			_, err = ResumeWithTools(context.Background(), root, spec, nil, tools, func(agent.Event) error { return nil })
			if err == nil || calls != wantCalls {
				t.Fatalf("unfinished proposal replayed: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestProposalCheckpointReuseAndTerminalReplay(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "proposal", InputID: "input", Prompt: "Propose updating the reference"}
	var reads, proposals int
	tools := agent.Tools{
		Lookup: func(ctx context.Context, input string) (json.RawMessage, error) {
			reads++
			if agent.OperationID(ctx) != 1 {
				t.Fatal("lookup identity")
			}
			return json.RawMessage(`{"response":{"answer":"REF-42"}}`), nil
		},
		Propose: func(ctx context.Context, p agent.Proposal) (agent.ProposalReceipt, error) {
			proposals++
			if agent.OperationID(ctx) != 2 || p.Action != "reference.update" || string(p.Arguments) != `{"value":42}` {
				t.Fatalf("proposal identity/input: %+v", p)
			}
			return agent.ProposalReceipt{ID: "request-42", Status: "pending_approval"}, nil
		},
	}
	client, _ := proposalModel(t, proposalCode)
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "tool.finished" && e.Name == "propose" {
			return errors.New("host delivery lost")
		}
		return nil
	})
	if err == nil {
		t.Fatal("delivery failure was ignored")
	}
	report, err := Inspect(root, spec)
	if err != nil || !report.CanResume || len(report.Operations) != 2 || report.Operations[1].Name() != "propose" {
		t.Fatalf("%+v %v", report, err)
	}
	client, calls := proposalModel(t, proposalCode)
	var reused int
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "tool.reused" {
			reused++
		}
		return nil
	})
	if err != nil || result.Status != "completed" || result.Kind != "approval" || len(result.Proposals) != 1 || len(result.Delegations) != 1 || reads != 1 || proposals != 1 || reused != 2 || calls.Load() != 3 {
		t.Fatalf("result=%+v err=%v reads=%d proposals=%d reused=%d", result, err, reads, proposals, reused)
	}
	report, err = Inspect(root, spec)
	if err != nil || report.Outcome != "terminal" {
		t.Fatalf("%+v %v", report, err)
	}
	replay, err := ResumeWithTools(context.Background(), root, spec, nil, agent.Tools{}, func(agent.Event) error { return nil })
	if err != nil || replay.Proposals[0].ID != "request-42" || calls.Load() != 3 {
		t.Fatalf("proposal replay: %+v %v", replay, err)
	}
}

func TestCapabilityBindingChangeRejectsInterruptedResume(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "binding", InputID: "input", Prompt: "Read status"}
	called := 0
	capability := agent.Capability{Name: "native_status", Version: "1", Origin: "fixture", Effect: "read", Description: "Read status.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		called++
		return json.RawMessage(`{"status":"ok"}`), nil
	}}
	client, _ := proposalModel(t, `const status=native_status({}); final('Done',{status});`)
	_, err := RunWithTools(context.Background(), root, spec, client, agent.Tools{Capabilities: []agent.Capability{capability}}, func(event agent.Event) error {
		if event.Type == "tool.finished" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || called != 1 {
		t.Fatalf("err=%v called=%d", err, called)
	}
	report, err := Inspect(root, spec)
	if err != nil || !report.CanResume || len(report.Operations) != 1 || report.Operations[0].Binding == "" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	capability.InputSchema = json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string"}}}`)
	_, err = ResumeWithTools(context.Background(), root, spec, nil, agent.Tools{Capabilities: []agent.Capability{capability}}, func(agent.Event) error { return nil })
	if err == nil || called != 1 {
		t.Fatalf("changed capability resumed: err=%v called=%d", err, called)
	}
}

func TestTrustedOperationBudgetAboveLegacyFour(t *testing.T) {
	spec := agent.Spec{Version: 1, RunID: "five", InputID: "input", Prompt: "Read five items", MaxOperations: 5}
	called := 0
	capability := agent.Capability{Name: "native_read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read item.", InputSchema: json.RawMessage(`{"type":"object","required":["item"],"properties":{"item":{"type":"integer"}},"additionalProperties":false}`), Call: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		called++
		return json.RawMessage(`{"ok":true}`), nil
	}}
	client, _ := proposalModel(t, `for(let item=1;item<=5;item++) native_read({item}); final('Done',{});`)
	root := t.TempDir()
	result, err := RunWithTools(context.Background(), root, spec, client, agent.Tools{Capabilities: []agent.Capability{capability}}, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || called != 5 {
		t.Fatalf("result=%+v err=%v called=%d", result, err, called)
	}
	report, err := Inspect(root, spec)
	if err != nil || len(report.Operations) != 5 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestModelRequestBudgetSurvivesResume(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "model-budget", InputID: "input", Prompt: "Read status", MaxModelCalls: 2}
	client, calls := proposalModel(t, `const status=native_status({}); final('Done',{status});`)
	capability := agent.Capability{Name: "native_status", Version: "1", Origin: "fixture", Effect: "read", Description: "Read status.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"ok"}`), nil
	}}
	tools := agent.Tools{Capabilities: []agent.Capability{capability}}
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "tool.finished" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || calls.Load() != 2 {
		t.Fatalf("first attempt err=%v model calls=%d", err, calls.Load())
	}
	report, err := Inspect(root, spec)
	if err != nil || !report.CanResume {
		t.Fatalf("recovery=%+v err=%v", report, err)
	}
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "failed" || result.Code != "model_budget_exceeded" || calls.Load() != 2 {
		t.Fatalf("result=%+v err=%v model calls=%d", result, err, calls.Load())
	}
}

func TestProposalReceiptReconciliationRequiresMatchingTool(t *testing.T) {
	input := `{"action":"reference.update","arguments":{},"summary":"Update the reference"}`
	spec := agent.Spec{Version: 1, RunID: "proposal", InputID: "input", Prompt: "Prepare"}
	state := checkpoint{Version: 1, Spec: spec, Operations: []operation{{ID: 1, Tool: "propose", Instruction: input}}, Events: []agent.Event{
		{Version: 1, RunID: spec.RunID, InputID: spec.InputID, Sequence: 1, Type: "run.started"},
		{Version: 1, RunID: spec.RunID, InputID: spec.InputID, Sequence: 2, Type: "tool.started", Name: "propose", OperationID: 1},
	}}
	root, _ := fixture(t, state)
	receipt := Receipt{ID: 1, Instruction: input, Result: json.RawMessage(`{"id":"request","status":"pending_approval"}`)}
	if _, err := Reconcile(root, spec, []Receipt{receipt}); err == nil {
		t.Fatal("lookup receipt repaired a proposal")
	}
	receipt.Tool = "propose"
	receipt.Result = json.RawMessage(`{"id":"request","status":"executed"}`)
	if _, err := Reconcile(root, spec, []Receipt{receipt}); err == nil {
		t.Fatal("execution receipt accepted as proposal")
	}
	receipt.Result = json.RawMessage(`{"id":"request","status":"pending_approval"}`)
	report, err := Reconcile(root, spec, []Receipt{receipt})
	if err != nil || !report.CanResume || report.Operations[0].Name() != "propose" {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestLookupAndProposalShareOperationBudget(t *testing.T) {
	client, _ := proposalModel(t, `lookup('one'); propose({action:'a',arguments:{},summary:'First'}); lookup('two'); propose({action:'a',arguments:{},summary:'Second'}); const limited=propose({action:'a',arguments:{},summary:'Third'}); final('Answer',{limited});`)
	var ids []uint64
	tools := agent.Tools{
		Lookup: func(ctx context.Context, _ string) (json.RawMessage, error) {
			ids = append(ids, agent.OperationID(ctx))
			return json.RawMessage(`{"response":{"answer":"read"}}`), nil
		},
		Propose: func(ctx context.Context, p agent.Proposal) (agent.ProposalReceipt, error) {
			ids = append(ids, agent.OperationID(ctx))
			return agent.ProposalReceipt{ID: p.Summary, Status: "pending_approval"}, nil
		},
	}
	spec := agent.Spec{Version: 1, RunID: "budget", InputID: "input", Prompt: "Prepare"}
	root := t.TempDir()
	result, err := RunWithTools(context.Background(), root, spec, client, tools, func(agent.Event) error { return nil })
	if err != nil || len(ids) != 4 || len(result.Proposals) != 2 || len(result.Delegations) != 2 {
		t.Fatalf("%+v %v %v", result, err, ids)
	}
	for i, id := range ids {
		if id != uint64(i+1) {
			t.Fatal("shared sequence reset")
		}
	}
	if _, err = Inspect(root, spec); err != nil {
		t.Fatal(err)
	}
}
