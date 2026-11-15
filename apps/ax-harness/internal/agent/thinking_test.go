package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

var updateThinking = flag.Bool("update-thinking", false, "rewrite the recorded events in testdata/thinking")

// thinkingFixture is one provider's responder stream in its documented wire
// format, the thinking text the harness must report, and the events it
// reported. The host's mapper test replays the recorded events.
type thinkingFixture struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Docs     string            `json:"docs"`
	Stream   []string          `json:"stream"`
	Thought  string            `json:"thought"`
	Request  string            `json:"request,omitempty"`
	Events   []json.RawMessage `json:"events"`
}

const thinkingActorReply = `{"javascriptCode":"final('Check the data',{})"}`

func thinkingServer(t *testing.T, fixture thinkingFixture, streamed *[]string) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "streamGenerateContent") || strings.Contains(string(body), `"stream":true`) {
			*streamed = append(*streamed, string(body))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.Join(fixture.Stream, "\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var reply any
		switch fixture.Provider {
		case "anthropic":
			reply = map[string]any{"id": "m", "type": "message", "role": "assistant", "model": fixture.Model,
				"content": []any{map[string]any{"type": "text", "text": thinkingActorReply}}, "stop_reason": "end_turn",
				"usage": map[string]any{"input_tokens": 3, "output_tokens": 2}}
		case "google-gemini":
			reply = map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model",
				"parts": []any{map[string]any{"text": thinkingActorReply}}}, "finishReason": "STOP"}},
				"usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 2, "totalTokenCount": 5}}
		default:
			reply = map[string]any{"id": "c", "object": "chat.completion", "choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": thinkingActorReply}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}}
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(server.Close)
	return server
}

func thinkingClient(fixture thinkingFixture, url string) ax.AIClient {
	options := ax.Object("base_url", url, "api_key", "synthetic", "model", fixture.Model, "retry", ax.Object("max_retries", 0))
	var client ax.AIClient
	if fixture.Provider == "openai-compatible" {
		client = ax.NewOpenAICompatibleClient(options)
	} else {
		client = ax.NewAI(fixture.Provider, options)
	}
	return &RoutedClient{AIClient: client, ModelNames: map[string]string{fixture.Provider: fixture.Model},
		Providers: map[string]string{fixture.Provider: fixture.Provider}, DefaultProvider: fixture.Provider}
}

func TestThinkingReachesTheEventsForEveryProvider(t *testing.T) {
	paths, err := filepath.Glob("testdata/thinking/*.json")
	if err != nil || len(paths) < 10 {
		t.Fatalf("fixtures %v %v", paths, err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture thinkingFixture
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		t.Run(fixture.Name, func(t *testing.T) {
			var streamed []string
			server := thinkingServer(t, fixture, &streamed)
			var thoughts strings.Builder
			var events []json.RawMessage
			result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "thinking-" + fixture.Name, InputID: "i", Prompt: "How many orders?",
				ReasoningEffort: "low", StreamResponses: true}, thinkingClient(fixture, server.URL), Tools{}, func(e Event) error {
				switch e.Type {
				case "thought.delta", "answer.delta", "actor.step":
				default:
					return nil
				}
				var data struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(e.Data, &data)
				if e.Type == "thought.delta" {
					thoughts.WriteString(data.Text)
				}
				recorded, _ := json.Marshal(struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
				}{e.Type, e.Data})
				events = append(events, recorded)
				return nil
			})
			if err != nil || result.Status != "completed" || result.Answer != "done" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if len(streamed) != 1 || !strings.Contains(streamed[0], fixture.Request) {
				t.Fatalf("the responder request lacks %s: %v", fixture.Request, streamed)
			}
			if thoughts.String() != fixture.Thought {
				t.Fatalf("thinking %q, want %q", thoughts.String(), fixture.Thought)
			}
			if *updateThinking {
				fixture.Events = events
				out, _ := json.MarshalIndent(fixture, "", " ")
				if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			if fmt.Sprint(compactAll(events)) != fmt.Sprint(compactAll(fixture.Events)) {
				t.Fatalf("events differ from the recording; run with -update-thinking after a check:\n got %s\nwant %s", compactAll(events), compactAll(fixture.Events))
			}
		})
	}
}

func compactAll(events []json.RawMessage) []string {
	out := make([]string, len(events))
	for i, e := range events {
		var b bytes.Buffer
		_ = json.Compact(&b, e)
		out[i] = b.String()
	}
	return out
}
