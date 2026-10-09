package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

// anthropicServer answers actor calls with Messages JSON and streams the last
// reply as Messages SSE with a thinking block.
func anthropicServer(t *testing.T, replies []string, bodies *[]string, mu *sync.Mutex) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		index := len(*bodies)
		*bodies = append(*bodies, string(body))
		mu.Unlock()
		if index >= len(replies) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			send := func(event, data string) { _, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data) }
			send("message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-served","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`)
			send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
			send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Checking the data."}}`)
			send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`)
			send("content_block_stop", `{"type":"content_block_stop","index":0}`)
			send("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
			text, _ := json.Marshal(replies[index])
			send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":%s}}`, text))
			send("content_block_stop", `{"type":"content_block_stop","index":1}`)
			send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`)
			send("message_stop", `{"type":"message_stop"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "claude-served",
			"content": []any{map[string]any{"type": "text", "text": replies[index]}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 3, "output_tokens": 2}})
	}))
	t.Cleanup(server.Close)
	return server
}

func anthropicRouted(server *httptest.Server) *RoutedClient {
	client := ax.NewAI("anthropic", ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "claude-x", "retry", ax.Object("max_retries", 0)))
	return &RoutedClient{AIClient: client, ModelNames: map[string]string{"anthropic": "claude-x"}, Providers: map[string]string{"anthropic": "anthropic"}, DefaultProvider: "anthropic"}
}

func TestAnthropicRunThinksCachesAndStreamsThoughts(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	server := anthropicServer(t, []string{`{"javascriptCode":"final('Check the data',{})"}`, `{"javascriptCode":"final('Answer',{})"}`, `Answer: done`}, &bodies, &mu)
	var thoughts, steps []string
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "anthropic", InputID: "i", Prompt: "p", ReasoningEffort: "low", StreamResponses: true},
		anthropicRouted(server), Tools{}, func(e Event) error {
			var data struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(e.Data, &data)
			switch e.Type {
			case "thought.delta":
				thoughts = append(thoughts, data.Text)
			case "actor.step":
				steps = append(steps, data.Text)
			}
			if (e.Type == "thought.delta" || e.Type == "actor.step") && e.Sequence != 0 {
				t.Errorf("live event advanced the sequence: %+v", e)
			}
			return nil
		})
	if err != nil || result.Status != "completed" || result.Answer != "done" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, body := range bodies {
		if !strings.Contains(body, `"thinking":{"type":"enabled"`) || !strings.Contains(body, `"cache_control":{"type":"ephemeral"}`) {
			t.Fatalf("request %d lacks thinking or cache_control: %s", i, body)
		}
	}
	if strings.Join(thoughts, "") != "Checking the data." || len(steps) == 0 || steps[0] != "Check the data" {
		t.Fatalf("thoughts=%q steps=%q", thoughts, steps)
	}
}

func TestSkillsCatalogLoadsAndReportsUse(t *testing.T) {
	model := &scripted{replies: []string{
		`{"javascriptCode":"await discover({skills:['daily-lead']}); console.log('loaded')"}`,
		`{"javascriptCode":"final('Follow the daily lead guide',{})"}`,
		`{"javascriptCode":"await used('daily-lead','followed the guide'); console.log('noted')"}`,
		`{"javascriptCode":"final('Done',{})"}`,
		`Answer: done`,
	}}
	server := model.server(t)
	var used []string
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "skills", InputID: "i", Prompt: "Make the daily lead report"}, openAIClient(server),
		Tools{Skills: []Skill{{Name: "daily-lead", Description: "Build the daily lead report", Content: "Step 1: read the leads table."}}},
		func(e Event) error {
			if e.Type == "skill.used" {
				used = append(used, e.Name)
			}
			return nil
		})
	if err != nil || result.Status != "completed" {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, model.calls())
	}
	if !strings.Contains(model.body(0), "daily-lead") || !strings.Contains(model.body(0), "Build the daily lead report") {
		t.Fatal("the skills catalog did not reach the request")
	}
	if !strings.Contains(model.body(1), "Step 1: read the leads table.") {
		t.Fatal("the loaded skill guide did not reach the next request")
	}
	if len(used) != 1 || used[0] != "daily-lead" {
		t.Fatalf("used=%q", used)
	}
}
