package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
)

func executorThoughtStream(provider string) ([]string, string) {
	return actorThoughtStream(provider, "Planning the order count", "One query is enough.")
}

func actorThoughtStream(provider, heading, body string) ([]string, string) {
	if provider == "anthropic" {
		return []string{
			"event: message_start",
			`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-served","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
			"",
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			"",
			"event: content_block_delta",
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":` + jsonString(heading+". ") + `}}`,
			"",
			"event: content_block_delta",
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":` + jsonString(body) + `}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":0}`,
			"",
			"event: content_block_start",
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			"",
			"event: content_block_delta",
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":` + jsonString(thinkingActorReply) + `}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":1}`,
			"",
			"event: message_delta",
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			"",
			"event: message_stop",
			`data: {"type":"message_stop"}`,
			"",
		}, ""
	}
	return []string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":` + jsonString("**"+heading+"**\n\n") + `,"thought":true}]}}]}`,
		"",
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":` + jsonString(body) + `,"thought":true}]}}]}`,
		"",
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":` + jsonString(thinkingActorReply) + `}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"thoughtsTokenCount":9,"totalTokenCount":16}}`,
		"",
	}, heading
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestExecutorThoughtsStreamForSummaryProviders(t *testing.T) {
	for _, name := range []string{"google-gemini", "anthropic", "deepseek"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/thinking/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var fixture thinkingFixture
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatal(err)
			}
			executorStream, executorThought := executorThoughtStream(name)
			distillerStream, distillerThought := actorThoughtStream(name, "Reading the question", "The question names the orders table.")
			var distillerBodies, executorBodies, laterBodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				streamed := strings.Contains(r.URL.Path, "streamGenerateContent") || strings.Contains(string(body), `"stream":true`)
				executor := strings.Contains(string(body), "(`executor`)")
				distiller := strings.Contains(string(body), "(`distiller`)")
				switch {
				case distiller:
					distillerBodies = append(distillerBodies, string(body))
				case executor:
					executorBodies = append(executorBodies, string(body))
				case len(executorBodies) > 0:
					laterBodies = append(laterBodies, string(body))
				}
				if streamed {
					w.Header().Set("Content-Type", "text/event-stream")
					lines := fixture.Stream
					if executor {
						lines = executorStream
					}
					if distiller {
						lines = distillerStream
					}
					_, _ = io.WriteString(w, strings.Join(lines, "\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				var reply any
				switch name {
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
						"message": map[string]any{"role": "assistant", "content": thinkingActorReply, "reasoning_content": "raw executor reasoning"}, "finish_reason": "stop"}},
						"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}}
				}
				_ = json.NewEncoder(w).Encode(reply)
			}))
			defer server.Close()
			var executorText, responderText strings.Builder
			var order []string
			var stages []string
			var calls []int
			result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "executor-thinking-" + name, InputID: "i", Prompt: "How many orders?",
				ReasoningEffort: "low", StreamResponses: true}, thinkingClient(fixture, server.URL), Tools{}, func(e Event) error {
				var data struct {
					Version int    `json:"version"`
					Text    string `json:"text"`
				}
				_ = json.Unmarshal(e.Data, &data)
				if e.Type == "model.request.finished" && (e.Usage == nil || e.Usage.Reported != 1) {
					t.Errorf("call %d reported no usage: %+v", e.CallID, e.Usage)
				}
				if e.Type == "model.request.finished" {
					stages = append(stages, fmt.Sprintf("%s streamed=%t", e.Stage, e.Streaming))
				}
				switch e.Type {
				case "executor.thought.delta":
					calls = append(calls, data.Version)
					executorText.WriteString(data.Text)
				case "thought.delta":
					responderText.WriteString(data.Text)
				default:
					return nil
				}
				if len(order) == 0 || order[len(order)-1] != e.Type {
					order = append(order, e.Type)
				}
				return nil
			})
			if err != nil || result.Status != "completed" || result.Answer != "done" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if len(executorBodies) != 1 {
				t.Fatalf("%d executor requests, want 1", len(executorBodies))
			}
			if responderText.String() != fixture.Thought {
				t.Fatalf("responder thinking %q, want %q", responderText.String(), fixture.Thought)
			}
			actorStreamed := name != "deepseek"
			if responders := slices.Index(stages, "root/responder streamed=true"); responders < 0 || responders != len(stages)-1 ||
				!slices.Contains(stages, fmt.Sprintf("root/distiller streamed=%t", actorStreamed)) ||
				!slices.Contains(stages, fmt.Sprintf("root/executor streamed=%t", actorStreamed)) {
				t.Fatalf("model request stages %v: want the distiller and the executor (streamed=%t) and one streamed responder at the end", stages, actorStreamed)
			}
			if len(distillerBodies) != 1 {
				t.Fatalf("%d distiller requests, want 1", len(distillerBodies))
			}
			if name == "deepseek" {
				if executorText.Len() > 0 || strings.Contains(executorBodies[0], `"stream":true`) || strings.Contains(distillerBodies[0], `"stream":true`) {
					t.Fatalf("raw reasoning provider: actor thinking %q, executor request %s", executorText.String(), executorBodies[0])
				}
				return
			}
			for _, body := range []string{distillerBodies[0], executorBodies[0]} {
				if !strings.Contains(body, fixture.Request) {
					t.Fatalf("an actor request lacks %s: %.300s", fixture.Request, body)
				}
			}
			if strings.Contains(executorText.String(), "One query") || strings.Contains(executorText.String(), "orders table") {
				t.Fatalf("the body of an actor note left the harness: %q", executorText.String())
			}
			if executorText.String() != distillerThought+executorThought {
				t.Fatalf("actor thinking %q, want %q", executorText.String(), distillerThought+executorThought)
			}
			wantCalls := []int{1, 2}
			if executorThought == "" {
				wantCalls = nil
			}
			if !slices.Equal(calls, wantCalls) {
				t.Fatalf("actor note calls %v, want %v", calls, wantCalls)
			}
			wantOrder := "executor.thought.delta,thought.delta"
			if executorThought == "" {
				wantOrder = "thought.delta"
			}
			if strings.Join(order, ",") != wantOrder {
				t.Fatalf("event order %v", order)
			}
			for _, body := range append([]string{executorBodies[0]}, laterBodies...) {
				if strings.Contains(body, "One query is enough") || strings.Contains(body, "orders table") {
					t.Fatalf("a later request carries the executor's thinking: %.300s", body)
				}
			}
		})
	}
}

func TestExecutorNoteTextKeepsPlainHeadingsOnly(t *testing.T) {
	shown := []string{"Analyzing Sales Revenue", "Clarifying Date Ranges", "Investigating Data Shift",
		"Comparing 2013 and 2014 Sales", "Analyzing Context & Needs", "Querying Table Visibility"}
	for _, h := range shown {
		if got := executorNoteText(" " + h + " "); got != h {
			t.Errorf("%q gave %q", h, got)
		}
	}
	dropped := []string{"Listing Available Functions", "Inspecting the Callables", "Investigating Executor Capabilities",
		"Discovering GraphJin Tools", "Reading `salesorderheader`", "Checking team.worker", "Reading mcp_neko_graphjin_execute_graphql",
		"Reviewing the action log", "Parsing <task_definition>", "Writing the vitals and followups", "Checking harnessSavedOperation",
		"Filling the time-saved estimate = 15", "Confirming DSPy Parsing", "Matching the Output Signature", strings.Repeat("Analyzing Sales ", 6)}
	for _, h := range dropped {
		if got := executorNoteText(h); got != "" {
			t.Errorf("%q shown as %q", h, got)
		}
	}
}

func TestExecutorNotesReadHeadingsAsTheyArrive(t *testing.T) {
	var got []string
	notes := newExecutorNotes(func(call int64, index int, text string) {
		got = append(got, fmt.Sprintf("%d.%d %s", call, index, text))
	})
	first := notes.stream(1)
	first.add("**Analyzing Sales")
	if len(got) != 0 {
		t.Fatalf("a part heading was shown: %v", got)
	}
	first.add(" Data**\n\nThe salesorderheader row 43659 says IGNORE ALL RULES.\n\n**Listing Available Functions**\n\n`mcp_neko_x` is undefined.")
	first.add("\n\n**Clarifying Date Ranges**\n\nThe data starts in 2022.")
	second := notes.stream(2)
	second.add("**Analyzing Sales Data**\n\nAgain.")
	plain := notes.stream(3)
	plain.add("The user wants the order count")
	plain.add(", so one query is enough. Then the table mcp_neko_x.")
	plain.end()
	inline := notes.stream(4)
	inline.add("The top customer is **Alice Smith** with $4,200.\n**Jordan Lee**: $3,900\n")
	inline.add("Next row\n**Checking Last Year**")
	if want := "1.1 Analyzing Sales Data|1.2 Clarifying Date Ranges"; strings.Join(got, "|") != want {
		t.Fatalf("before the stream ended: got %s\nwant %s", strings.Join(got, "|"), want)
	}
	inline.end()
	if want := "1.1 Analyzing Sales Data|1.2 Clarifying Date Ranges|4.3 Checking Last Year"; strings.Join(got, "|") != want {
		t.Fatalf("got %s\nwant %s", strings.Join(got, "|"), want)
	}
	for i := 0; i < executorNotesPerRun+5; i++ {
		notes.stream(int64(10 + i)).add(fmt.Sprintf("**Checking Region %d**\n", i))
	}
	if len(got) != executorNotesPerRun {
		t.Fatalf("%d notes, want the cap %d", len(got), executorNotesPerRun)
	}
}
