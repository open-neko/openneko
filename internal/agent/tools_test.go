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
