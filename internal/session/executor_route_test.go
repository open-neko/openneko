package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
)

func TestExecutorErrorRouteSurvivesInterruptedAttempt(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "error-route-resume", InputID: "input", Prompt: "Finish without running a tool", MaxModelCalls: 12}
	var selected []string
	serve := func(alias string, answers []string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			selected = append(selected, alias)
			if len(answers) == 0 {
				http.Error(w, "unexpected model request", http.StatusBadRequest)
				return
			}
			answer := answers[0]
			answers = answers[1:]
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
		}))
	}
	contextServer := serve("context", []string{`{"javascriptCode":"final('Plan',{});"}`, `{"javascriptCode":"final('Plan again',{});"}`, `{"answer":"Done"}`})
	defer contextServer.Close()
	baseServer := serve("base", []string{`{"javascriptCode":"throw new Error('recoverable executor error');"}`})
	defer baseServer.Close()
	strongServer := serve("strong", []string{`{"javascriptCode":"final('Done',{});"}`})
	defer strongServer.Close()
	entries := []ax.Value{}
	for _, route := range []struct{ key, url string }{{"context", contextServer.URL}, {"base", baseServer.URL}, {"strong", strongServer.URL}} {
		service := ax.NewOpenAICompatibleClient(ax.Object("base_url", route.url, "api_key", "synthetic", "model", "fixture"))
		service.Name = route.key
		entries = append(entries, ax.RouterServiceEntry{Key: route.key, Service: service})
	}
	router, err := ax.NewMultiServiceRouter(entries)
	if err != nil {
		t.Fatal(err)
	}
	client := &agent.RoutedClient{AIClient: router, Stages: agent.StageModels{
		Context: "context", Executor: "base", Responder: "context", ExecutorEscalation: "strong", ExecutorAfterErrors: 1,
	}}
	_, err = RunWithTools(context.Background(), root, spec, client, agent.Tools{}, func(e agent.Event) error {
		if e.Type == "executor.step.failed" {
			return errors.New("delivery stopped after durable error receipt")
		}
		return nil
	})
	if err == nil || strings.Join(selected, ",") != "context,base" {
		t.Fatalf("first attempt err=%v routes=%v", err, selected)
	}
	report, err := Inspect(root, spec)
	if err != nil || !report.CanResume || report.NextAttempt != 2 {
		t.Fatalf("error receipt did not survive interruption: report=%+v err=%v", report, err)
	}
	var resumed []agent.Event
	result, err := ResumeWithTools(context.Background(), root, spec, client, agent.Tools{}, func(e agent.Event) error {
		resumed = append(resumed, e)
		return nil
	})
	if err != nil || result.Status != "completed" || result.Answer != "Done" || strings.Join(selected, ",") != "context,base,context,strong,context" {
		t.Fatalf("resumed result=%+v err=%v routes=%v", result, err, selected)
	}
	var strongSeen, failed int
	for _, e := range resumed {
		if e.Type == "executor.step.failed" {
			failed++
		}
		if e.Type == "model.request.started" && e.Origin == "strong" {
			strongSeen++
		}
	}
	if failed != 1 || strongSeen != 1 {
		t.Fatalf("durable error or actual route missing: failed=%d strong=%d", failed, strongSeen)
	}
}
