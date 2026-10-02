package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestCapabilityOrderIsStable(t *testing.T) {
	call := func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }
	capability := func(name string) Capability {
		return Capability{Name: name, Version: "1", Origin: "host", Effect: "read", Description: "Read a fixture.", InputSchema: json.RawMessage(`{"type":"object"}`), Call: call}
	}
	first := Tools{Capabilities: []Capability{capability("zeta"), capability("alpha")}}
	second := Tools{Capabilities: []Capability{capability("alpha"), capability("zeta")}}
	a, err := first.admitted()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.admitted()
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"alpha", "zeta"} {
		if a[i].Name != want || b[i].Name != want {
			t.Fatalf("tool order changed: %q, %q", a[i].Name, b[i].Name)
		}
	}
}

func TestCatalogFingerprintSeparatesScopeFromAdmittedContract(t *testing.T) {
	read := Capability{Name: "catalog", Version: "1", Origin: "host", Effect: "read",
		Description: "Read catalog.", InputSchema: json.RawMessage(`{"type":"object"}`),
		Call: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}
	first := Tools{Scope: "org:a", Capabilities: []Capability{read}, ChildReads: []string{"catalog"}}
	second := first
	second.Scope = "org:b"
	firstScoped, err := first.CatalogHash()
	if err != nil {
		t.Fatal(err)
	}
	secondScoped, err := second.CatalogHash()
	if err != nil || firstScoped == secondScoped {
		t.Fatalf("run scope was not bound: %q %q %v", firstScoped, secondScoped, err)
	}
	firstComparable, err := first.CatalogFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	secondComparable, err := second.CatalogFingerprint()
	if err != nil || firstComparable != secondComparable {
		t.Fatalf("same admitted contract differed by scope: %q %q %v", firstComparable, secondComparable, err)
	}
	second.ChildReads = nil
	changed, err := second.CatalogFingerprint()
	if err != nil || changed == firstComparable {
		t.Fatalf("changed child grant reused fingerprint: %q %q %v", firstComparable, changed, err)
	}
}

func TestChildReadsRejectEffectsAndMissingTools(t *testing.T) {
	read := Capability{Name: "catalog", Version: "1", Origin: "host", Effect: "read", Description: "Read catalog.", InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}
	write := read
	write.Name, write.Effect = "write", "durable"
	for _, names := range [][]string{{"write"}, {"missing"}, {"catalog", "catalog"}} {
		tools := Tools{Capabilities: []Capability{read, write}, ChildReads: names}
		admitted, err := tools.admitted()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tools.childReads(admitted); err == nil {
			t.Fatalf("accepted child authority %v", names)
		}
	}
	tools := Tools{Capabilities: []Capability{read, write}, ChildReads: []string{"catalog"}}
	admitted, err := tools.admitted()
	if err != nil {
		t.Fatal(err)
	}
	child, err := tools.childReads(admitted)
	if err != nil || len(child) != 1 || child[0].Name != "catalog" {
		t.Fatalf("child=%v err=%v", child, err)
	}
	withChild, _ := tools.CatalogHash()
	tools.ChildReads = nil
	withoutChild, _ := tools.CatalogHash()
	if withChild == withoutChild {
		t.Fatal("delegation grant did not affect catalog binding")
	}
}

func TestFinalizerRequiresTerminalVerificationAndPinsVersion(t *testing.T) {
	finalizer := func(context.Context, []SavedOperation) (TerminalDecision, error) {
		return TerminalDecision{Accepted: false}, nil
	}
	tools := Tools{FinalizerGate: finalizer, FinalizerGateVersion: "v1"}
	if _, err := tools.CatalogHash(); err == nil {
		t.Fatal("accepted a finalizer without terminal verification")
	}
	tools.TerminalGate = func(context.Context, Result, []SavedOperation) (TerminalDecision, error) {
		return TerminalDecision{Accepted: false}, nil
	}
	tools.TerminalGateVersion = "v1"
	first, err := tools.CatalogHash()
	if err != nil {
		t.Fatal(err)
	}
	tools.FinalizerGateVersion = "v2"
	second, err := tools.CatalogHash()
	if err != nil || first == second {
		t.Fatalf("finalizer policy version did not bind catalog: %q %q %v", first, second, err)
	}
}

func TestModelAdmissionEventFailurePreventsDispatch(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "must not dispatch", 500)
	}))
	defer model.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture"))
	spec := Spec{Version: 1, RunID: "model-admission", InputID: "input", Prompt: "Answer"}
	_, err := RunWithTools(context.Background(), spec, client, Tools{}, func(e Event) error {
		if e.Type == "model.request.started" {
			return errors.New("journal unavailable")
		}
		return nil
	})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("dispatch after failed event: err=%v calls=%d", err, calls.Load())
	}
}

func TestFailedDurableToolCannotReportCompletedAction(t *testing.T) {
	answers := []string{
		`{"javascriptCode":"final('Save the file',{})"}`,
		`{"javascriptCode":"const receipt=file_write({}); final('Report completion',{receipt});"}`,
		`{"answer":"I saved the file."}`,
	}
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer model.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture"))
	tool := Capability{Name: "file_write", Version: "1", Origin: "fixture", Effect: "durable", Description: "Save a file.", InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"is_error":true,"error":"write_denied"}`), nil
	}}
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "failed-write", InputID: "input", Prompt: "Save the file"},
		client, Tools{Capabilities: []Capability{tool}}, func(Event) error { return nil })
	if err != nil || result.Status != "failed" || result.Kind != "partial" || result.Code != "incomplete_result" || result.Answer != "The run did not complete; a tool returned an incomplete or failed result." || calls.Load() != 3 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestProposalTrustBoundary(t *testing.T) {
	for _, input := range []string{
		`{"action":"a","arguments":{},"summary":"Ask","status":"approved"}`,
		`{"action":"a","arguments":null,"summary":"Ask"}`,
		`{"action":"a","arguments":[],"summary":"Ask"}`,
		`{"action":"","arguments":{},"summary":"Ask"}`,
	} {
		if _, err := ParseProposal([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	for _, input := range []string{
		`{"id":"r","status":"executed"}`,
		`{"id":"r","status":"approved"}`,
		`{"status":"pending_approval"}`,
		`{"status":"denied"}`,
		`{"id":"r","status":"pending_approval","executed":true}`,
	} {
		if _, err := ParseProposalReceipt([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	if _, err := ParseProposalReceipt([]byte(`{"status":"denied","reason":"Not authorized"}`)); err != nil {
		t.Fatal(err)
	}
}
