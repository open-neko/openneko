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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
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
		notices             int
	}{
		{"executor tool and responder fence",
			"try { await mcp_neko_ui_render_cards({title:'Sales by region', table:'Region | Sales\\nEast | 12\\nWest | 9', callout:'East leads.'}); } catch (e) {} final('Report', {east:12, west:9});",
			"Answer: East sold the most.\n\n" + fence, "", 0, 1, 0},
		{"responder fence only", "final('Report', {east:12, west:9});", "Answer: East sold the most.\n\n" + fence, "", 0, 1, 0},
		{"responder fence that the repair fixes", "final('Report', {east:12, west:9});",
			"Answer: East sold the most.\n\n```neko_cards\n{\"title\":\"Sales\",\"callout\":\"East leads.\",\"mood\":\"warning\"}\n```",
			`{"title":"Sales","callout":"East leads.","mood":"watch"}`, 1, 1, 0},
		{"responder fence that the repair cannot fix", "final('Report', {east:12, west:9});",
			"Answer: East sold the most.\n\n```neko_cards\n{\"title\":\"Sales\",\"chart\":\"pie\"}\n```", `{"title":"Sales","chart":"pie"}`, 2, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearAxEnv(t, nil)
			model, bodies := scriptedModel(t, []string{
				`{"javascriptCode":"final('Sales by region', {})"}`,
				`{"javascriptCode":` + string(mustJSON(c.actor)) + `}`,
				c.answer,
			})
			socket := filepath.Join(testsock.Dir(t), "serve.sock")
			ln, err := serve.Listen(socket)
			if err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithCancel(context.Background())
			serveDoor(t, socket)
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
				repairCards: func(context.Context, string, []string, bool) (string, llm.Usage, error) {
					repairs++
					cost := 0.0004
					return c.repaired, llm.Usage{Coverage: "complete", Input: 900, Output: 80, Total: 980, Calls: 1, CostStatus: "estimated", CostUSD: &cost}, nil
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
			fixing, spends, notices := 0, 0, 0
			for _, e := range events {
				fixing += strings.Count(e, `"message":"Fixing a chart"`)
				if strings.Contains(e, `"source":"card_repair"`) && strings.Contains(e, `"estimatedCostUsd":0.0004`) {
					spends++
				}
				notices += strings.Count(e, `{"type":"notice","code":"card_not_shown","message":"A chart could not be shown"}`)
			}
			if fixing != min(c.repairs, 1) || spends != c.repairs || notices != c.notices {
				t.Fatalf("%d status lines, %d repair spends, %d notices\nevents %v", fixing, spends, notices, events)
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
		return func(_ context.Context, cards string, problems []string, rich bool) (string, llm.Usage, error) {
			if strings.TrimSpace(cards) != bad || !rich {
				t.Fatalf("repair got %q rich=%v", cards, rich)
			}
			asked = append(asked, problems)
			return reply, llm.Usage{}, err
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
		func(_ context.Context, cards string, problems []string, _ bool) (string, llm.Usage, error) {
			asked = append(asked, problems[0])
			return stages[len(asked)], llm.Usage{}, nil
		})
	if len(messages) != 1 || len(issues) != 0 || len(asked) != 2 ||
		!strings.HasPrefix(asked[0], "mood:") || !strings.HasPrefix(asked[1], "keyFigures.0 needs") {
		t.Fatalf("%d messages, issues %q, asked %q", len(messages), issues, asked)
	}
}

func TestTwoCardFencesShowOneAnswerSurface(t *testing.T) {
	answer := "East leads.\n\n```neko_cards\n{\"title\":\"Sales\",\"table\":\"Region | Sales\\nEast | 12\",\"callout\":\"East leads.\"}\n```\n\n" +
		"West is second.\n\n```neko_cards\n{\"title\":\"Margins\",\"table\":\"Region | Margin\\nWest | 9\",\"callout\":\"West holds.\",\"mood\":\"good\"}\n```"
	text, messages, issues := extractCardFences(context.Background(), answer, true, nil)
	if len(issues) != 0 || len(messages) != 1 || text != "East leads.\n\nWest is second." {
		t.Fatalf("text %q, %d messages, issues %q", text, len(messages), issues)
	}
	components := jsArray(jsGet(jsGet(messages[0], "createSurface"), "components"))
	ids := map[string]bool{}
	byID := map[string]any{}
	for _, c := range components {
		id := jsGet(c, "id").(string)
		if ids[id] {
			t.Fatalf("component id %s twice: %s", id, jsStringify(messages, false))
		}
		ids[id], byID[id] = true, c
	}
	root := byID["root"]
	if jsGet(root, "title") != "Sales" || jsStringify(jsGet(root, "children"), false) != `["table","callout","part2-section"]` {
		t.Fatalf("root %s", jsStringify(root, false))
	}
	section := byID["part2-section"]
	if jsGet(section, "component") != "Section" || jsGet(section, "title") != "Margins" ||
		jsStringify(jsGet(section, "children"), false) != `["part2-table","part2-callout"]` ||
		!strings.Contains(jsStringify(byID["part2-table"], false), `West`) || jsGet(byID["part2-callout"], "mood") != "good" {
		t.Fatalf("section %s", jsStringify(messages, false))
	}
}

func TestCardRepairsHaveATurnLimit(t *testing.T) {
	bad := `{"title":"Sales","callout":"East leads.","mood":"warning"}`
	answer := strings.Repeat("```neko_cards\n"+bad+"\n```\n\n", 3)
	calls := 0
	_, messages, issues := extractCardFences(context.Background(), answer, true,
		func(context.Context, string, []string, bool) (string, llm.Usage, error) {
			calls++
			return bad, llm.Usage{}, nil
		})
	if calls != cardRepairsPerTurn || len(messages) != 0 || !slices.Contains(issues, "no repair left in this turn") {
		t.Fatalf("%d repair calls, %d messages, issues %q", calls, len(messages), issues)
	}

	old := cardRepairTimeout
	t.Cleanup(func() { cardRepairTimeout = old })
	cardRepairTimeout = 50 * time.Millisecond
	began := time.Now()
	calls = 0
	_, _, issues = extractCardFences(context.Background(), answer, true,
		func(ctx context.Context, _ string, _ []string, _ bool) (string, llm.Usage, error) {
			calls++
			if _, ok := ctx.Deadline(); !ok {
				t.Error("a repair call has no deadline")
			}
			<-ctx.Done()
			return "", llm.Usage{}, ctx.Err()
		})
	if took := time.Since(began); took > 2*time.Second || calls != 2 || !slices.Contains(issues, "repair failed: context deadline exceeded") {
		t.Fatalf("took %v, %d calls, issues %q", took, calls, issues)
	}
}

func TestACardRepairKeepsTheFieldsNoProblemNamed(t *testing.T) {
	bad := `{"title":"Sales","table":"Region | Sales\nEast | 12","callout":"East leads.","mood":"warning"}`
	for _, c := range []struct {
		name, original, reply string
		shown                 bool
		issue                 string
	}{
		{"fixes the named field", bad, `{"title":"Sales","table":"Region | Sales\nEast | 12","callout":"East leads.","mood":"watch"}`, true, ""},
		{"changes a figure", bad, `{"title":"Sales","table":"Region | Sales\nEast | 13","callout":"East leads.","mood":"watch"}`, false, "repair changed table, which no problem named"},
		{"drops a field", bad, `{"title":"Sales","table":"Region | Sales\nEast | 12","mood":"watch"}`, false, "repair changed callout, which no problem named"},
		{"adds a field", bad, `{"title":"Sales","table":"Region | Sales\nEast | 12","callout":"East leads.","mood":"watch","keyFigures":["East: 12"]}`, false, "repair changed keyFigures, which no problem named"},
		{"mends JSON with the same texts", `{"title":"Sales","callout":"East leads." "mood":"watch"}`, `{"title":"Sales","callout":"East leads.","mood":"watch"}`, true, ""},
		{"mends JSON with a new text", `{"title":"Sales","callout":"East leads." "mood":"watch"}`, `{"title":"Sales","callout":"West leads.","mood":"watch"}`, false, "repair changed callout, which no problem named"},
		{"fixes the cells of a named row", `{"title":"Sales","table":"Region | Sales\nEast | 12 | x\nWest | 9"}`, `{"title":"Sales","table":"Region | Sales\nEast | 12\nWest | 9"}`, true, ""},
		{"changes a figure in a named row", `{"title":"Sales","table":"Region | Sales\nEast | 12 | x\nWest | 9"}`, `{"title":"Sales","table":"Region | Sales\nEast | 13\nWest | 9"}`, false, "repair changed table, which no problem named"},
		{"changes a figure in a named table", `{"title":"Sales","table":"Region | Sales\nEast | 12 | x\nWest | 9"}`, `{"title":"Sales","table":"Region | Sales\nEast | 12\nWest | 8"}`, false, "repair changed table, which no problem named"},
		{"swaps the figures of two rows", `{"title":"Sales","table":"Region | Sales\nEast | 12 | x\nWest | 9"}`, `{"title":"Sales","table":"Region | Sales\nEast | 9\nWest | 12"}`, false, "repair changed table, which no problem named"},
		{"drops a row", `{"title":"Sales","table":"Region | Sales\nEast | 12 | x\nWest | 9"}`, `{"title":"Sales","table":"Region | Sales\nWest | 9"}`, true, ""},
		{"drops a row that no problem named", `{"title":"Sales","table":"Region | Sales\nEast | 12 | x\nWest | 9"}`, `{"title":"Sales","table":"Region | Sales\nEast | 12"}`, false, "repair changed table, which no problem named"},
		{"writes plain numbers for the chart", `{"title":"Sales","chart":"bar","chartColumn":"Sales","table":"Region | Sales\nEast | $1,200\nWest | 9%"}`, `{"title":"Sales","chart":"bar","chartColumn":"Sales","table":"Region | Sales\nEast | 1200\nWest | 9"}`, true, ""},
		{"drops a row for the chart", `{"title":"Sales","chart":"bar","chartColumn":"Sales","table":"Region | Sales\nEast | $1,200\nWest | 9%\nNorth | n/a"}`, `{"title":"Sales","chart":"bar","chartColumn":"Sales","table":"Region | Sales\nEast | 1200\nWest | 9"}`, false, "repair changed table, which no problem named"},
		{"adds a table to an empty card", `{"title":"Sales"}`, `{"title":"Sales","table":"Region | Sales\nEast | 12\nWest | 9"}`, false, "repair changed table, which no problem named"},
		{"puts a key figure in form", `{"title":"Sales","keyFigures":["Revenue: 12","Orders 40"]}`, `{"title":"Sales","keyFigures":["Revenue: 12","Orders: 40"]}`, true, ""},
		{"changes a key figure", `{"title":"Sales","keyFigures":["Revenue: 12","Orders 40"]}`, `{"title":"Sales","keyFigures":["Revenue: 12","Orders: 41"]}`, false, "repair changed keyFigures, which no problem named"},
		{"changes a key figure that no problem named", `{"title":"Sales","keyFigures":["Revenue: 12","Orders 40"]}`, `{"title":"Sales","keyFigures":["Revenue: 13","Orders: 40"]}`, false, "repair changed keyFigures, which no problem named"},
		{"mends JSON with a cut number", `{"title":"Sales","table":"Region | Sales\nEast | 12699845.92" "mood":"watch"}`, `{"title":"Sales","table":"Region | Sales\nEast | 12699845","mood":"watch"}`, false, "repair changed table, which no problem named"},
		{"mends JSON and keeps the rows", `{"title":"Sales","table":"Region | Sales\nEast | 12699845.92\nWest | 9" "mood":"watch"}`, `{"title":"Sales","table":"Region | Sales\nEast | 12699845.92\nWest | 9","mood":"watch"}`, true, ""},
		{"mends JSON and drops a row", `{"title":"Sales","table":"Region | Sales\nEast | 12699845.92\nWest | 9" "mood":"watch"}`, `{"title":"Sales","table":"Region | Sales\nEast | 12699845.92","mood":"watch"}`, false, "repair changed table, which no problem named"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, messages, issues := extractCardFences(context.Background(), "Up.\n\n```neko_cards\n"+c.original+"\n```", true,
				func(context.Context, string, []string, bool) (string, llm.Usage, error) {
					return c.reply, llm.Usage{}, nil
				})
			if (len(messages) == 1) != c.shown || c.issue != "" && !slices.Contains(issues, c.issue) {
				t.Fatalf("%d messages, issues %q", len(messages), issues)
			}
		})
	}
}
