package webfront

import (
	"context"
	"encoding/json"
	"errors"
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
		text, messages, issues := extractCardFences(context.Background(), answer, c.Rich, nil)
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
		repaired            string
		repairs, surfaces   int
	}{
		{"executor tool and responder fence",
			"try { await mcp_neko_ui_render_cards({title:'Sales by region', table:'Region | Sales\\nEast | 12\\nWest | 9', callout:'East leads.'}); } catch (e) {} final('Report', {east:12, west:9});",
			"Answer: East sold the most.\n\n" + fence, "", 0, 1},
		{"responder fence only", "final('Report', {east:12, west:9});", "Answer: East sold the most.\n\n" + fence, "", 0, 1},
		{"responder fence that the repair fixes", "final('Report', {east:12, west:9});",
			"Answer: East sold the most.\n\n```neko_cards\n{\"title\":\"Sales\",\"callout\":\"East leads.\",\"mood\":\"warning\"}\n```",
			`{"title":"Sales","callout":"East leads.","mood":"watch"}`, 1, 1},
		{"responder fence that the repair cannot fix", "final('Report', {east:12, west:9});",
			"Answer: East sold the most.\n\n```neko_cards\n{\"title\":\"Sales\",\"chart\":\"pie\"}\n```", `{"title":"Sales","chart":"pie"}`, 2, 0},
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
			repairs := 0
			tools := &fakeTools{reply: []any{}}
			res, err := (&API{}).runAgentCore(context.Background(), agentCoreInput{
				Backend: &llm.AxBackendConfig{Route: llm.AxModelRoute{Provider: "openai-compatible", Model: "fixture", URL: model.URL, KeyEnv: "OPENAI_API_KEY"}},
				Prompt:  "Which region sold the most?", OrgID: "org-cards", ThreadID: "thread-cards", RunID: "run-cards",
				Workspace: ws, Keys: map[string]string{"OPENAI_API_KEY": "k"}, Socket: socket,
				NativeDelegationOff: true,
				FilesEnabled:        func() bool { return false },
				toolBackend:         func(turnBinding) toolBackend { return tools },
				withoutScriptData:   true,
				repairCards: func(context.Context, string, []string, bool) (string, error) {
					repairs++
					return c.repaired, nil
				},
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
			if repairs != c.repairs {
				t.Fatalf("%d repairs, want %d", repairs, c.repairs)
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

func TestCardsPromptCarriesTheCardFieldContract(t *testing.T) {
	for _, rich := range []bool{true, false} {
		text := cardsSection(rich)
		for _, want := range []string{`"mood":{`, `"enum":["good","watch","act"]`, `"enum":["line","bar","area","donut"]`, `"keyFigures":{`, `"maxItems":4`} {
			if !strings.Contains(text, want) {
				t.Errorf("rich=%v: the cards prompt lacks %s", rich, want)
			}
		}
		if strings.Contains(text, `"mapValue":{`) != rich {
			t.Errorf("rich=%v: the map fields belong only to rich cards", rich)
		}
	}
}

func TestCardFenceRepair(t *testing.T) {
	ctx := context.Background()
	good := `{"title":"Sales","table":"Region | Sales\nEast | 12","callout":"East leads.","mood":"watch"}`
	bad := `{"title":"Sales","table":"Region | Sales\nEast | 12","callout":"East leads.","mood":"warning"}`
	answer := func(body string) string { return "East leads.\n\n```neko_cards\n" + body + "\n```" }
	var asked [][]string
	repair := func(reply string, err error) cardRepair {
		return func(_ context.Context, cards string, problems []string, rich bool) (string, error) {
			if strings.TrimSpace(cards) != bad || !rich {
				t.Fatalf("repair got %q rich=%v", cards, rich)
			}
			asked = append(asked, problems)
			return reply, err
		}
	}

	_, messages, issues := extractCardFences(ctx, answer(good), true, repair("", errors.New("not called")))
	if len(messages) != 1 || len(issues) != 0 || len(asked) != 0 {
		t.Fatalf("a valid fence: %d messages, issues %q, %d repairs", len(messages), issues, len(asked))
	}
	text, messages, issues := extractCardFences(ctx, answer(bad), true, repair(good, nil))
	if text != "East leads." || len(messages) != 1 || len(issues) != 0 || len(asked) != 1 || !strings.HasPrefix(asked[0][0], "mood: Invalid option") {
		t.Fatalf("a repaired fence: %q %d messages, issues %q, asked %q", text, len(messages), issues, asked)
	}
	_, messages, issues = extractCardFences(ctx, answer(bad), true, repair(bad, nil))
	if len(messages) != 0 || len(issues) != 2 || issues[0] != "after repair" || len(asked) != 3 {
		t.Fatalf("a repair that still fails: %d messages, issues %q, %d repairs", len(messages), issues, len(asked))
	}
	_, messages, issues = extractCardFences(ctx, answer(bad), true, repair("", errors.New("model down")))
	if len(messages) != 0 || issues[len(issues)-1] != "repair failed: model down" || len(asked) != 4 {
		t.Fatalf("a repair error: %d messages, issues %q", len(messages), issues)
	}
	_, messages, issues = extractCardFences(ctx, answer(bad), true, nil)
	if len(messages) != 0 || len(issues) != 1 {
		t.Fatalf("no repair: %d messages, issues %q", len(messages), issues)
	}
}

func TestCardRepairHostCall(t *testing.T) {
	f, db, g := hostFront(t)
	ctx := t.Context()
	seedPrimary(t, db, "openai", "gpt-5", `{}`, `{"apiKey":"k"}`)
	g.reply = map[string]any{"fixedCards": "```json\n{\"title\":\"Sales\",\"callout\":\"East leads.\",\"mood\":\"watch\"}\n```"}
	bad := "{\"title\":\"Sales\",\"callout\":\"East leads.\",\"mood\":\"warning\"}"
	_, messages, issues := extractCardFences(ctx, "East leads.\n\n```neko_cards\n"+bad+"\n```", false, f.API.cardRepair("org"))
	if len(messages) != 1 || len(issues) != 0 {
		t.Fatalf("%d messages, issues %q", len(messages), issues)
	}
	if len(g.calls) != 1 {
		t.Fatalf("gateway calls %d", len(g.calls))
	}
	v := g.calls[0].request.Values
	if v["cards"] != bad || !strings.HasPrefix(v["problems"].(string), "mood: Invalid option") ||
		!strings.Contains(v["schema"].(string), `"enum":["good","watch","act"]`) || strings.Contains(v["schema"].(string), `"mapValue"`) ||
		!strings.Contains(g.calls[0].request.Signature, "-> fixedCards:string") {
		t.Fatalf("request %+v", g.calls[0].request)
	}
	var task string
	if err := db.QueryRow(ctx, `select task from spend_ledger where org_id = 'org' order by created_at desc limit 1`).Scan(&task); err != nil || task != "turn" {
		t.Fatalf("spend row: %q %v", task, err)
	}
}

func TestCardFenceRepairSeesTheLaterFaults(t *testing.T) {
	stages := []string{
		`{"title":"Sales","keyFigures":["March 1,311,000"],"mood":"warning","callout":"Up."}`,
		`{"title":"Sales","keyFigures":["March 1,311,000"],"mood":"watch","callout":"Up."}`,
		`{"title":"Sales","keyFigures":["March: 1,311,000"],"mood":"watch","callout":"Up."}`,
	}
	var asked []string
	_, messages, issues := extractCardFences(context.Background(), "Up.\n\n```neko_cards\n"+stages[0]+"\n```", true,
		func(_ context.Context, cards string, problems []string, _ bool) (string, error) {
			asked = append(asked, problems[0])
			return stages[len(asked)], nil
		})
	if len(messages) != 1 || len(issues) != 0 || len(asked) != 2 ||
		!strings.HasPrefix(asked[0], "mood:") || !strings.HasPrefix(asked[1], "keyFigures.0 needs") {
		t.Fatalf("%d messages, issues %q, asked %q", len(messages), issues, asked)
	}
}
