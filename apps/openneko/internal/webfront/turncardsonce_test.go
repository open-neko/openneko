package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func TestCardFencesKeepEveryCardCheck(t *testing.T) {
	raw, err := os.ReadFile("testdata/cardfences.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name      string
		Rich      bool
		Arguments json.RawMessage
		Problems  []string
		Messages  json.RawMessage
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	refused := 0
	for _, c := range cases {
		if len(c.Problems) > 0 {
			refused++
		}
	}
	if len(cases) < 50 || refused < 30 {
		t.Fatalf("fixture holds %d cases, %d refused", len(cases), refused)
	}
	saved := cardSurfaceID
	cardSurfaceID = func() string { return "answer-<id>" }
	defer func() { cardSurfaceID = saved }()
	for _, c := range cases {
		answer := "East leads.\n\n```neko_cards\n" + string(c.Arguments) + "\n```"
		text, messages, issues := extractCardFences(answer, c.Rich)
		if text != "East leads." {
			t.Errorf("%s: text %q", c.Name, text)
		}
		if len(c.Problems) > 0 {
			if len(messages) != 0 || !reflect.DeepEqual(issues, c.Problems) {
				t.Errorf("%s:\n got %d messages, issues %q\nwant %q", c.Name, len(messages), issues, c.Problems)
			}
			continue
		}
		if len(issues) != 0 || !reflect.DeepEqual(decodeAny([]byte(jsStringify(messages, false))), decodeAny(c.Messages)) {
			t.Errorf("%s: issues %q\n got %s\nwant %s", c.Name, issues, jsStringify(messages, false), compactJSON(t, c.Messages))
		}
	}
}

func scriptedModel(t *testing.T, responses []string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		content := responses[min(len(bodies), len(responses)-1)]
		bodies = append(bodies, string(body))
		mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("content-type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0,
				"delta": map[string]any{"role": "assistant", "content": content}}}})
			end, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
			_, _ = w.Write([]byte("data: " + string(chunk) + "\n\ndata: " + string(end) + "\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(model.Close)
	return model, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func TestWebTurnWritesTheCardsOnce(t *testing.T) {
	const fence = "```neko_cards\n{\"title\":\"Sales by region\",\"table\":\"Region | Sales\\nEast | 12\\nWest | 9\",\"callout\":\"East leads.\"}\n```"
	cases := []struct {
		name, actor, answer string
		surfaces            int
	}{
		{"executor tool and responder fence",
			"try { await mcp_neko_ui_render_cards({title:'Sales by region', table:'Region | Sales\\nEast | 12\\nWest | 9', callout:'East leads.'}); } catch (e) {} final('Report', {east:12, west:9});",
			"Answer: East sold the most.\n\n" + fence, 1},
		{"responder fence only", "final('Report', {east:12, west:9});", "Answer: East sold the most.\n\n" + fence, 1},
		{"responder fence that fails the check", "final('Report', {east:12, west:9});",
			"Answer: East sold the most.\n\n```neko_cards\n{\"title\":\"Sales\",\"chart\":\"pie\"}\n```", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearAxEnv(t, nil)
			model, bodies := scriptedModel(t, []string{
				`{"javascriptCode":"final('Sales by region', {})"}`,
				`{"javascriptCode":` + string(mustJSON(c.actor)) + `}`,
				c.answer,
			})
			socket := filepath.Join(t.TempDir(), "serve.sock")
			ln, err := serve.Listen(socket)
			if err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithCancel(context.Background())
			srv := &serve.Server{Getenv: os.Getenv, MaxTurns: 2}
			done := make(chan struct{})
			go func() { _ = srv.Serve(ctx, ln); close(done) }()
			defer func() { stop(); <-done }()
			ws, err := turnWorkspace("org-cards", "thread-cards", "run-cards")
			if err != nil {
				t.Skipf("workspace: %v", err)
			}
			for _, d := range []string{ws.SkillsRoot, ws.RunRoot} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			var events []string
			tools := &fakeTools{reply: []any{}}
			res, err := (&API{}).runAgentCore(context.Background(), agentCoreInput{
				Backend: &llm.AxBackendConfig{Route: llm.AxModelRoute{Provider: "openai-compatible", Model: "fixture", URL: model.URL, KeyEnv: "OPENAI_API_KEY"}},
				Prompt:  "Which region sold the most?", OrgID: "org-cards", ThreadID: "thread-cards", RunID: "run-cards",
				Workspace: ws, Keys: map[string]string{"OPENAI_API_KEY": "k"}, Socket: socket,
				NativeDelegationOff: true,
				FilesEnabled:        func() bool { return false },
				toolBackend:         func(turnBinding) toolBackend { return tools },
				withoutScriptData:   true,
				Emit: func(_ context.Context, e any) error {
					mu.Lock()
					events = append(events, jsStringify(e, false))
					mu.Unlock()
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != "completed" || res.FinalText != "East sold the most." {
				t.Fatalf("result %+v\nevents %v", res, events)
			}
			mu.Lock()
			defer mu.Unlock()
			tools.mu.Lock()
			for _, e := range tools.events {
				events = append(events, string(mustJSON(e)))
			}
			tools.mu.Unlock()
			surfaces := 0
			for _, e := range events {
				if strings.Contains(e, `"type":"surface"`) {
					surfaces++
				}
			}
			if surfaces != c.surfaces {
				t.Fatalf("%d answer surfaces, want %d\nevents %v", surfaces, c.surfaces, events)
			}
			for _, body := range bodies() {
				if strings.Contains(body, "render_cards") {
					t.Fatal("a model request offers the card tool")
				}
			}
		})
	}
}
