package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestAQuietModelCallShowsAPlainStepLine(t *testing.T) {
	saved := quietStepDelay
	quietStepDelay = time.Second
	t.Cleanup(func() { quietStepDelay = saved })
	replies := []string{
		`{"javascriptCode":"final('Read the data',{})"}`,
		`{"javascriptCode":"const a=alpha({}); console.log(a)"}`,
		`{"javascriptCode":"final('Answer',{})"}`,
	}
	for _, slow := range []bool{true, false} {
		t.Run(fmt.Sprintf("slow=%t", slow), func(t *testing.T) {
			wait := time.Duration(0)
			if slow {
				wait = 1500 * time.Millisecond
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				call := int(calls.Add(1))
				if call > 1 {
					time.Sleep(wait)
				}
				if strings.Contains(string(body), `"stream":true`) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Answer: Done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				reply := replies[min(call, len(replies))-1]
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, reply)
			}))
			defer server.Close()
			client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
			tools := Tools{Capabilities: []Capability{{Name: "alpha", Version: "1", Origin: "mcp", Effect: "read", Description: "Read alpha.",
				InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{"ok":true}`), nil
				}}}}
			var mu sync.Mutex
			var order, lines []string
			result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "quiet", InputID: "i", Prompt: "Read alpha", StreamResponses: true}, client, tools, func(e Event) error {
				mu.Lock()
				defer mu.Unlock()
				switch e.Type {
				case "step.quiet":
					var data struct {
						Text string `json:"text"`
					}
					_ = json.Unmarshal(e.Data, &data)
					if e.Sequence != 0 {
						t.Errorf("step.quiet advanced the durable sequence: %+v", e)
					}
					lines = append(lines, data.Text)
					order = append(order, "line:"+data.Text)
				case "tool.finished", "answer.delta", "run.finished":
					order = append(order, e.Type)
				}
				return nil
			})
			if err != nil || result.Status != "completed" || result.Answer != "Done" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			time.Sleep(2 * quietStepDelay)
			mu.Lock()
			defer mu.Unlock()
			want := []string{"line:Working out the first step", "tool.finished", "line:Putting the findings together", "line:Writing the answer", "answer.delta", "run.finished"}
			if !slow {
				want = []string{"tool.finished", "answer.delta", "run.finished"}
			}
			order = slices.Compact(order)
			if !slices.Equal(order, want) {
				t.Fatalf("events %v, want %v (lines %v)", order, want, lines)
			}
		})
	}
}

func TestANoteOrTheRunEndStopsTheStepLine(t *testing.T) {
	saved := quietStepDelay
	quietStepDelay = 50 * time.Millisecond
	t.Cleanup(func() { quietStepDelay = saved })
	for _, shown := range []string{"executor.thought.delta", "thought.delta", "answer.delta", "actor.step", "tool.started", "run.finished"} {
		var mu sync.Mutex
		var got []string
		r := &recorder{spec: Spec{RunID: "r", StreamResponses: true}, cancel: func() {}, emit: func(e Event) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, e.Type)
			return nil
		}}
		r.mu.Lock()
		r.watchQuiet(1, "root/executor")
		r.mu.Unlock()
		if shown == "run.finished" {
			r.send(Event{Type: shown})
		} else {
			r.progress(Event{Type: shown})
		}
		time.Sleep(3 * quietStepDelay)
		mu.Lock()
		if slices.Contains(got, "step.quiet") {
			t.Errorf("a step line came after %s: %v", shown, got)
		}
		mu.Unlock()
	}
}
