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

type streamFormat struct {
	name     string
	provider string
	events   []string
	usage    ModelUsage
}

var openAIStream = []string{
	`data: {"id":"c","model":"m-served","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
	`data: {"id":"c","model":"m-served","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
	`data: {"id":"c","model":"m-served","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
	`data: [DONE]`,
}

var streamFormats = []streamFormat{
	{"openai-compatible", "openai-compatible", openAIStream, ModelUsage{Reported: 1, InputTokens: 11, OutputTokens: 7, TotalTokens: 18}},
	{"openai profile", "groq", openAIStream, ModelUsage{Reported: 1, InputTokens: 11, OutputTokens: 7, TotalTokens: 18}},
	{"gemini", "google-gemini", []string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":1,"totalTokenCount":12},"modelVersion":"g-served"}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":20,"thoughtsTokenCount":2},"modelVersion":"g-served"}`,
	}, ModelUsage{Reported: 1, InputTokens: 11, OutputTokens: 7, TotalTokens: 20, ReasoningTokens: 2}},
	{"anthropic", "anthropic", []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-served\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"cache_read_input_tokens\":5}}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
	}, ModelUsage{Reported: 1, InputTokens: 3, OutputTokens: 4, TotalTokens: 12, CacheReadTokens: 5}},
}

// sseServer sends the events in order. With abortAfter > 0 it drops the
// connection after that many events, as a provider that fails mid-stream.
func sseServer(t *testing.T, events []string, abortAfter int, bodies *[]string) *httptest.Server {
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for i, event := range events {
			if abortAfter > 0 && i == abortAfter {
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}
			_, _ = fmt.Fprintf(w, "%s\n\n", event)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func streamClient(provider, url string) ax.RuntimeHookAIClient {
	options := ax.Object("base_url", url, "api_key", "synthetic", "model", "m", "retry", ax.Object("max_retries", 0))
	if provider == "openai-compatible" {
		return ax.NewOpenAICompatibleClient(options)
	}
	return ax.NewAI(provider, options).(ax.RuntimeHookAIClient)
}

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) emit(e Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
	return nil
}

func (l *eventLog) finished() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Event
	for _, e := range l.events {
		if e.Type == "model.request.finished" {
			out = append(out, e)
		}
	}
	return out
}

func openMeteredStream(t *testing.T, provider, url string) (*recorder, *eventLog, ax.AxChatStream) {
	t.Helper()
	log := &eventLog{}
	rec := &recorder{spec: Spec{Version: 1, RunID: "r", InputID: "i"}, emit: log.emit, cancel: func() {}, openStreams: map[*meteredStream]struct{}{}}
	hooks := ax.AxRuntimeHooks{RateLimiter: ax.AxRateLimiterFunc(rec.admitModel)}
	request := ax.Object("chat_prompt", ax.Array(ax.Object("role", "user", "content", "hi")), "model_config", ax.Object("stream", true))
	stream, err := streamClient(provider, url).StreamEventsWithHooks(context.Background(), request, ax.Object(), hooks)
	if err != nil {
		t.Fatal(err)
	}
	return rec, log, stream
}

func TestStreamedRequestReportsTheUsageOfItsLastChunk(t *testing.T) {
	for _, format := range streamFormats {
		t.Run(format.name, func(t *testing.T) {
			var bodies []string
			server := sseServer(t, format.events, 0, &bodies)
			rec, log, stream := openMeteredStream(t, format.provider, server.URL)
			chunks := 0
			for stream.Next() {
				chunks++
				if len(log.finished()) != 0 {
					t.Fatalf("the request finished at chunk %d, before the stream ended", chunks)
				}
			}
			_ = stream.Close()
			finished := log.finished()
			if stream.Err() != nil || len(finished) != 1 {
				t.Fatalf("err %v, %d finished events", stream.Err(), len(finished))
			}
			got := finished[0]
			if got.Error != "" || !got.Streaming || got.Usage == nil {
				t.Fatalf("finished event %+v", got)
			}
			want := format.usage
			want.PricedBy = unpriced
			if *got.Usage != want {
				t.Fatalf("usage %+v, want %+v", *got.Usage, want)
			}
			if usage := rec.usageSnapshot(); usage.Coverage != "complete" || usage.Requests != 1 || usage.TotalTokens != want.TotalTokens {
				t.Fatalf("run usage %+v", usage)
			}
			if format.events[0] == openAIStream[0] && !strings.Contains(bodies[0], `"stream_options":{"include_usage":true}`) {
				t.Fatalf("the request does not ask for usage in the stream: %s", bodies[0])
			}
		})
	}
}

func TestStreamThatEndsEarlyKeepsTheReportedUsageAsPartial(t *testing.T) {
	gemini := streamFormats[2]
	expectPartial := func(t *testing.T, rec *recorder, log *eventLog, failed bool, tokens int64) {
		t.Helper()
		finished := log.finished()
		if len(finished) != 1 || (finished[0].Error != "") != failed {
			t.Fatalf("finished events %+v", finished)
		}
		usage := rec.usageSnapshot()
		if tokens == 0 {
			if finished[0].Usage != nil || usage.Coverage != "unavailable" {
				t.Fatalf("event usage %+v, run usage %+v", finished[0].Usage, usage)
			}
			return
		}
		if u := finished[0].Usage; u == nil || u.Coverage != "partial" || u.Reported != 0 || u.TotalTokens != tokens {
			t.Fatalf("event usage %+v", finished[0].Usage)
		}
		if usage.Coverage != "partial" || usage.Reported != 0 || usage.TotalTokens != tokens || usage.PricedBy != unpriced {
			t.Fatalf("run usage %+v", usage)
		}
		if usage.Early == nil || usage.Early.Requests != 1 || usage.Early.TotalTokens != tokens {
			t.Fatalf("early usage %+v", usage.Early)
		}
		rec.mu.Lock()
		charged := rec.chargedTokens()
		rec.mu.Unlock()
		if charged != tokens {
			t.Fatalf("charged %d tokens for a stream that reported %d", charged, tokens)
		}
	}
	t.Run("cancel", func(t *testing.T) {
		var bodies []string
		rec, log, stream := openMeteredStream(t, gemini.provider, sseServer(t, gemini.events, 0, &bodies).URL)
		if !stream.Next() {
			t.Fatal(stream.Err())
		}
		_ = stream.Close()
		_ = stream.Close()
		expectPartial(t, rec, log, false, 12)
	})
	t.Run("cancel before any usage", func(t *testing.T) {
		var bodies []string
		rec, log, stream := openMeteredStream(t, "openai-compatible", sseServer(t, openAIStream, 0, &bodies).URL)
		if !stream.Next() {
			t.Fatal(stream.Err())
		}
		_ = stream.Close()
		expectPartial(t, rec, log, false, 0)
	})
	t.Run("provider error", func(t *testing.T) {
		var bodies []string
		rec, log, stream := openMeteredStream(t, gemini.provider, sseServer(t, gemini.events, 1, &bodies).URL)
		for stream.Next() {
		}
		if stream.Err() == nil {
			t.Fatal("the dropped connection gave no error")
		}
		expectPartial(t, rec, log, true, 12)
	})
	t.Run("left open", func(t *testing.T) {
		var bodies []string
		rec, log, stream := openMeteredStream(t, gemini.provider, sseServer(t, gemini.events, 0, &bodies).URL)
		if !stream.Next() {
			t.Fatal(stream.Err())
		}
		rec.closeStreams()
		expectPartial(t, rec, log, false, 12)
		if len(rec.openStreams) != 0 {
			t.Fatal("the closed stream is still listed")
		}
	})
}

// The responder is the one streamed call of a turn; its tokens belong in
// the run usage that the org spend meter reads.
func TestRunUsageCountsTheStreamedResponder(t *testing.T) {
	replies := []string{`{"javascriptCode":"final('Report', {})"}`, `{"javascriptCode":"final('Done', {})"}`, "Answer: done"}
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reply := replies[min(calls, len(replies)-1)]
		calls++
		mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk,
				`{"choices":[],"model":"served","usage":{"prompt_tokens":40,"completion_tokens":60,"total_tokens":100}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "served", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
	}))
	t.Cleanup(server.Close)
	log := &eventLog{}
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "u", InputID: "i", Prompt: "p", StreamResponses: true}, openAIClient(server), Tools{}, log.emit)
	if err != nil || result.Status != "completed" || result.Usage == nil {
		t.Fatalf("result %+v err %v", result, err)
	}
	streamed := 0
	for _, e := range log.finished() {
		if e.Streaming {
			streamed++
			if e.Usage == nil || e.Usage.TotalTokens != 100 {
				t.Fatalf("streamed request usage %+v", e.Usage)
			}
		}
	}
	if u := *result.Usage; streamed != 1 || u.Coverage != "complete" || u.Requests != 3 || u.Reported != 3 || u.TotalTokens != 110 {
		t.Fatalf("%d streamed requests, run usage %+v", streamed, u)
	}
}

func TestRunSendsReplyWrittenWhenTheWrittenReplyIsTheAnswer(t *testing.T) {
	write := `{"javascriptCode":"replyAsWritten({text: 'Exactly this.'}); final('Report', {})"}`
	replies := []string{write, write, "Answer: model text"}
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reply := replies[min(calls, len(replies)-1)]
		calls++
		mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(server.Close)
	log := &eventLog{}
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "w", InputID: "i", Prompt: "p", StreamResponses: true}, openAIClient(server), Tools{}, log.emit)
	if err != nil || result.Status != "completed" || result.Answer != "Exactly this." {
		t.Fatalf("result %+v err %v", result, err)
	}
	var written []string
	for _, e := range log.events {
		if e.Type == "reply.written" {
			written = append(written, string(e.Data))
		}
	}
	if len(written) != 1 || written[0] != `{"explicit":true,"responder_text":false}` {
		t.Fatalf("reply.written events %v", written)
	}

	plain := &eventLog{}
	plainCode := `{"javascriptCode":"final('Report', {})"}`
	mu.Lock()
	replies = []string{plainCode, plainCode, "Answer: model text"}
	calls = 0
	mu.Unlock()
	if _, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "p", InputID: "i", Prompt: "p", StreamResponses: true}, openAIClient(server), Tools{}, plain.emit); err != nil {
		t.Fatal(err)
	}
	for _, e := range plain.events {
		if e.Type == "reply.written" {
			t.Fatalf("a run with no written reply sent %s", e.Data)
		}
	}
}

func TestClosingStreamsWhileAReaderReadsHasNoRace(t *testing.T) {
	var events []string
	for i := 1; i <= 400; i++ {
		events = append(events, fmt.Sprintf(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"x"}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":%d,"totalTokenCount":%d},"modelVersion":"g"}`, i, 11+i))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			if _, err := fmt.Fprintf(w, "%s\n\n", event); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(server.Close)
	rec, log, stream := openMeteredStream(t, "google-gemini", server.URL)
	read := make(chan int)
	go func() {
		n := 0
		for stream.Next() {
			n++
			if n == 5 {
				read <- n
			}
		}
		close(read)
	}()
	<-read
	rec.closeStreams()
	for range read {
	}
	if finished := log.finished(); len(finished) != 1 || finished[0].Usage == nil || finished[0].Usage.Coverage != "partial" {
		t.Fatalf("finished events %+v", finished)
	}
}
