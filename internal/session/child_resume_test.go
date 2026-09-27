package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
)

func TestChildReadReceiptSurvivesInterruptedChild(t *testing.T) {
	answers := []string{
		`{"javascriptCode":"final('Delegate the check',{})"}`,
		`{"javascriptCode":"const child=team.researcher({question:'Find reference'}); final('Use child evidence',{child});"}`,
		`{"javascriptCode":"final('Read the reference',{})"}`,
		`{"javascriptCode":"const evidence=catalog({key:'reference'}); final('Report the reference',{evidence});"}`,
		`{"answer":"REF-42"}`,
		`{"javascriptCode":"final('Continue the interrupted check',{})"}`,
		`{"javascriptCode":"const child=team.researcher({question:'Find reference'}); final('Use recovered evidence',{child});"}`,
		`{"javascriptCode":"final('Read the reference',{})"}`,
		`{"javascriptCode":"const evidence=catalog({key:'reference'}); final('Report the reference',{evidence});"}`,
		`{"answer":"REF-42"}`,
		`{"answer":"Verified REF-42."}`,
	}
	var modelCalls, reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(modelCalls.Add(1)) - 1
		if n >= len(answers) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[n]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	tools := agent.Tools{Capabilities: []agent.Capability{{
		Name: "catalog", Version: "1", Origin: "fixture", Effect: "read", Description: "Read one reference.",
		InputSchema: json.RawMessage(`{"type":"object","required":["key"],"properties":{"key":{"type":"string"}},"additionalProperties":false}`),
		Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reads.Add(1)
			return json.RawMessage(`{"reference":"REF-42"}`), nil
		},
	}}, ChildReads: []string{"catalog"}}
	spec := agent.Spec{Version: 1, RunID: "child-recovery", InputID: "input", Prompt: "Verify the reference with a child", MaxOperations: 4, MaxModelCalls: 16}
	root := t.TempDir()
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "model.request.finished" && e.CallID == 5 {
			return errors.New("host died during child answer")
		}
		return nil
	})
	if err == nil || reads.Load() != 1 {
		t.Fatalf("interrupted child err=%v reads=%d", err, reads.Load())
	}
	status, err := Inspect(root, spec)
	if err != nil || !status.CanResume || status.Outcome != "interrupted" {
		t.Fatalf("child checkpoint status=%+v err=%v", status, err)
	}
	var resumed []agent.Event
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		resumed = append(resumed, e)
		return nil
	})
	if err != nil || result.Status != "completed" || result.Answer != "Verified REF-42." || reads.Load() != 1 || modelCalls.Load() != int32(len(answers)) {
		t.Fatalf("resumed child result=%+v err=%v reads=%d models=%d", result, err, reads.Load(), modelCalls.Load())
	}
	reused := 0
	for _, e := range resumed {
		if e.Type == "tool.reused" && e.Name == "catalog" {
			reused++
		}
	}
	if reused != 1 {
		t.Fatalf("recovered read was not reused exactly once: %d", reused)
	}
}
