package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/session"
)

func TestAgentDelegatesThroughBrokerHTTP(t *testing.T) {
	for _, tc := range []struct{ status, kind string }{
		{"answered", "answer"}, {"blocked", "refusal"}, {"needs_clarification", "clarification"}, {"error", "partial"}, {"interrupted", ""},
	} {
		t.Run(tc.status, func(t *testing.T) {
			lookups := 0
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				lookups++
				var request map[string]any
				json.NewDecoder(r.Body).Decode(&request)
				if r.URL.Path != "/v1/harness/lookup" || request["operationId"] != float64(1) || request["instruction"] != "Find reference" || request["dataSourceId"] != "source-1" {
					t.Errorf("wrong lookup: %v", request)
				}
				io.WriteString(w, `{"response":{"status":"`+tc.status+`","answer":"REF-42","evidence":["record-42"],"trace_id":"trace-42"}}`)
			}))
			defer broker.Close()
			calls := 0
			answers := []string{`{"javascriptCode":"final('Find reference', {})"}`, `{"javascriptCode":"const evidence=lookup('Find reference'); final('Report reference', {evidence});"}`, `{"answer":"REF-42"}`}
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if calls >= len(answers) {
					http.Error(w, "unexpected call", 400)
					return
				}
				if calls == 2 && !strings.Contains(string(body), "record-42") {
					t.Error("responder did not receive remote evidence")
				}
				content := answers[calls]
				calls++
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", content), "finish_reason", "stop"))))
			}))
			defer model.Close()
			lookup, _ := GraphJin(broker.URL, "synthetic", "source-1")
			client := ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture"))
			var events []agent.Event
			root := t.TempDir()
			result, err := session.Run(context.Background(), root, agent.Spec{Version: 1, RunID: "r", InputID: "i", Prompt: "Find reference"}, client, lookup, func(e agent.Event) error {
				events = append(events, e)
				if tc.status == "interrupted" && e.Type == "tool.finished" {
					return errors.New("disconnected")
				}
				return nil
			})
			if tc.status == "interrupted" {
				if err == nil || lookups != 1 {
					t.Fatalf("expected interrupted read: %v", err)
				}
				files, _ := filepath.Glob(filepath.Join(root, "*.json"))
				if len(files) != 1 {
					t.Fatal("missing checkpoint")
				}
				data, _ := os.ReadFile(files[0])
				if !strings.Contains(string(data), "record-42") {
					t.Fatal("read evidence lost")
				}
				_, err = session.Run(context.Background(), root, agent.Spec{Version: 1, RunID: "r", InputID: "i", Prompt: "Find reference"}, client, lookup, func(agent.Event) error { return nil })
				if err == nil || lookups != 1 {
					t.Fatal("interrupted run replayed effects")
				}
				return
			}

			if err != nil || result.Status != "completed" || result.Kind != tc.kind || result.Answer != "REF-42" || lookups != 1 || calls != 3 {
				t.Fatalf("result=%+v err=%v lookups=%d calls=%d", result, err, lookups, calls)
			}
			replayed, err := session.Run(context.Background(), root, agent.Spec{Version: 1, RunID: "r", InputID: "i", Prompt: "Find reference"}, client, lookup, func(agent.Event) error { return nil })
			if err != nil || replayed.Answer != "REF-42" || lookups != 1 || calls != 3 || len(replayed.Delegations) != 1 {
				t.Fatalf("durable replay failed: %+v %v", replayed, err)
			}
			starts, ends := 0, 0
			for _, e := range events {
				if e.Type == "tool.started" {
					starts++
				}
				if e.Type == "tool.finished" {
					ends++
				}
			}
			if starts != 1 || ends != 1 {
				t.Fatalf("tool events: %d/%d", starts, ends)
			}
		})
	}
}
