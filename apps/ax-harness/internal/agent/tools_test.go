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

func TestInvalidToolSelectionEmitsMetadataWithoutDispatch(t *testing.T) {
	answers := []string{
		`{"javascriptCode":"final('Check the catalog',{})"}`,
		`{"javascriptCode":"try { catalog({id:'wrong'}) } catch (error) {} final('No verified row',{})"}`,
		`{"answer":"No verified row."}`,
	}
	var modelCalls, toolCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		index := int(modelCalls.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message",
			ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	tool := Capability{Name: "catalog", Version: "1", Origin: "fixture", Effect: "read",
		Description: "Read a row.", InputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}},"additionalProperties":false}`),
		Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			toolCalls.Add(1)
			return json.RawMessage(`{"id":42}`), nil
		}}
	var observed []Event
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "invalid-tool", InputID: "input", Prompt: "Check the catalog"},
		client, Tools{Capabilities: []Capability{tool}}, func(e Event) error { observed = append(observed, e); return nil })
	if err != nil || result.Status != "completed" || modelCalls.Load() != 3 || toolCalls.Load() != 0 {
		t.Fatalf("result=%+v err=%v model=%d tool=%d", result, err, modelCalls.Load(), toolCalls.Load())
	}
	var rejected, started int
	for _, event := range observed {
		switch event.Type {
		case "tool.input.rejected":
			rejected++
			if event.Name != "catalog" || event.Error != "invalid_input" || len(event.Data) != 0 {
				t.Fatalf("rejection exposed input: %+v", event)
			}
		case "tool.started":
			started++
		}
	}
	if rejected != 1 || started != 0 {
		t.Fatalf("rejected=%d started=%d", rejected, started)
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
			return errors.New("event sink unavailable")
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
