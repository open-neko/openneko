package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
)

type providerCall struct {
	host, path, query, key string
	stream                 bool
	headers                http.Header
}

// fakeProviders answers in the wire format of each provider: OpenAI chat
// completions, Anthropic messages and Gemini generateContent.
type fakeProviders struct {
	mu      sync.Mutex
	calls   []providerCall
	replies []string
	served  int
	fail    map[string]int
}

func (f *fakeProviders) record(c providerCall) (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if status := f.fail[c.key]; status != 0 {
		return "", status
	}
	reply := f.replies[min(f.served, len(f.replies)-1)]
	f.served++
	return reply, 200
}

func (f *fakeProviders) all() []providerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]providerCall{}, f.calls...)
}

func (f *fakeProviders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	stream := strings.Contains(string(body), `"stream":true`) || strings.Contains(r.URL.Path, ":streamGenerateContent")
	key := r.Header.Get("Authorization")
	for _, h := range []string{"X-Api-Key", "X-Goog-Api-Key", "Api-Key"} {
		if v := r.Header.Get(h); v != "" {
			key = h + " " + v
		}
	}
	text, status := f.record(providerCall{host: r.Header.Get("X-Test-Upstream-Host"), path: r.URL.Path, query: r.URL.RawQuery,
		key: key, stream: stream, headers: r.Header.Clone()})
	if status != 200 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"overloaded","type":"overloaded_error"}}`)
		return
	}
	var events []any
	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		if !stream {
			fakeJSON(w, map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
				"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn",
				"usage": map[string]any{"input_tokens": 3, "output_tokens": 2}})
			return
		}
		events = []any{
			map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
				"content": []any{}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 0}}},
			map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
			map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}},
			map[string]any{"type": "content_block_stop", "index": 0},
			map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}},
			map[string]any{"type": "message_stop"},
		}
	case strings.Contains(r.URL.Path, ":generateContent"), strings.Contains(r.URL.Path, ":streamGenerateContent"):
		answer := map[string]any{"candidates": []any{map[string]any{"index": 0, "finishReason": "STOP",
			"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}}}},
			"usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 2, "totalTokenCount": 5}}
		if !stream {
			fakeJSON(w, answer)
			return
		}
		events = []any{answer}
	default:
		if !stream {
			fakeJSON(w, map[string]any{"model": "m", "choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
			return
		}
		events = []any{
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}}}},
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}},
			"[DONE]",
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		line := "[DONE]"
		if m, ok := e.(map[string]any); ok {
			raw, _ := json.Marshal(m)
			line = string(raw)
			if kind, ok := m["type"].(string); ok {
				_, _ = fmt.Fprintf(w, "event: %s\n", kind)
			}
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
		w.(http.Flusher).Flush()
	}
}

func fakeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// toFake sends each upstream call of the door to the fake provider and
// names the host the door chose in a header.
type toFake struct{ fake *url.URL }

func (t toFake) RoundTrip(r *http.Request) (*http.Response, error) {
	out := r.Clone(r.Context())
	out.Header.Set("X-Test-Upstream-Host", r.URL.Host)
	out.URL.Scheme, out.URL.Host, out.Host = t.fake.Scheme, t.fake.Host, t.fake.Host
	return http.DefaultTransport.RoundTrip(out)
}

type doorProvider struct {
	name, host, wantPath, header string
	route                        func(model string) llm.AxModelRoute
}

var doorProviders = []doorProvider{
	{"openai", "api.openai.com", "/v1/chat/completions", "Authorization",
		func(m string) llm.AxModelRoute { return llm.AxModelRoute{Provider: "openai", Model: m} }},
	{"anthropic", "api.anthropic.com", "/v1/messages", "X-Api-Key",
		func(m string) llm.AxModelRoute { return llm.AxModelRoute{Provider: "anthropic", Model: m} }},
	{"google-gemini", "generativelanguage.googleapis.com", "/v1beta/models/MODEL:", "X-Goog-Api-Key",
		func(m string) llm.AxModelRoute { return llm.AxModelRoute{Provider: "google-gemini", Model: m} }},
	{"azure-openai", "res1.openai.azure.com", "/openai/deployments/MODEL/chat/completions", "Api-Key",
		func(m string) llm.AxModelRoute {
			return llm.AxModelRoute{Provider: "azure-openai", Model: m,
				Options: map[string]string{"resource_name": "res1", "deployment_name": m, "api_version": "2024-10-21"}}
		}},
	{"openai-compatible", "compat.example", "/v1/chat/completions", "Authorization",
		func(m string) llm.AxModelRoute {
			return llm.AxModelRoute{Provider: "openai-compatible", Model: m, URL: "https://compat.example/v1"}
		}},
}

func (p doorProvider) keyHeader(key string) string {
	if p.header == "Authorization" {
		return "Bearer " + key
	}
	return p.header + " " + key
}

// doorTurn runs one turn through a strict serve and a key door whose
// upstream calls reach fake.
func doorTurn(t *testing.T, fake *fakeProviders, cfg *llm.AxBackendConfig, keys map[string]string, tools *fakeTools) axRunResult {
	t.Helper()
	clearAxEnv(t, nil)
	model := httptest.NewServer(fake)
	t.Cleanup(model.Close)
	target, _ := url.Parse(model.URL)
	socket := strictServe(t)
	door := &llm.KeyDoor{Transport: toFake{target}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	path := keydoor.SocketPath(os.Getenv, socket)
	if _, err := llm.ListenDoor(ctx, path, door); err != nil {
		t.Fatal(err)
	}
	a := &API{Door: door, doorPath: path}
	ws, err := turnWorkspace("org-door", "thread-door", "run-door")
	if err != nil {
		t.Skipf("workspace: %v", err)
	}
	for _, d := range []string{ws.SkillsRoot, ws.RunRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	res, err := a.runAgentCore(context.Background(), agentCoreInput{
		Backend: cfg, Prompt: "Find reference",
		OrgID: "org-door", ThreadID: "thread-door", RunID: "run-door",
		Workspace: ws, Keys: keys, Socket: socket,
		NativeDelegationOff: true,
		FilesEnabled:        func() bool { return false },
		toolBackend:         func(turnBinding) toolBackend { return tools },
		withoutScriptData:   true,
		Emit:                func(context.Context, any) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func checkDoorCalls(t *testing.T, p doorProvider, model string, calls []providerCall, wantKey string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("the provider got no call")
	}
	for _, c := range calls {
		if c.host != p.host {
			t.Errorf("the door called host %q, want %q", c.host, p.host)
		}
		want := strings.ReplaceAll(p.wantPath, "MODEL", model)
		if !strings.HasPrefix(c.path, want) {
			t.Errorf("the door called path %q, want %q", c.path, want)
		}
		if p.name == "azure-openai" && c.query != "api-version=2024-10-21" {
			t.Errorf("azure query %q", c.query)
		}
		if c.key != p.keyHeader(wantKey) {
			t.Errorf("the provider got key %q, want %q", c.key, p.keyHeader(wantKey))
		}
		for name, values := range c.headers {
			if strings.Contains(strings.Join(values, " "), keydoor.TokenPrefix) {
				t.Errorf("the door token reached the provider in %s", name)
			}
		}
	}
}

var doorActorReplies = []string{
	`{"javascriptCode":"final('Find reference', {})"}`,
	`{"javascriptCode":"final('Report', {reference:'REF-42'});"}`,
	"Answer: REF-42",
}

func TestKeyDoorCarriesAStreamedAnswerForEachProvider(t *testing.T) {
	for _, p := range doorProviders {
		t.Run(p.name, func(t *testing.T) {
			fake := &fakeProviders{replies: doorActorReplies}
			cfg := &llm.AxBackendConfig{Route: p.route("model-a")}
			res := doorTurn(t, fake, cfg, map[string]string{"MODEL_API_KEY": "planted-" + p.name}, &fakeTools{reply: []any{}})
			if res.Status != "completed" || !strings.Contains(res.FinalText, "REF-42") {
				t.Fatalf("result %+v", res)
			}
			calls := fake.all()
			checkDoorCalls(t, p, "model-a", calls, "planted-"+p.name)
			if last := calls[len(calls)-1]; !last.stream {
				t.Errorf("the answer was not streamed: %s", last.path)
			}
		})
	}
}

func TestKeyDoorCarriesAToolCallTurnForEachProvider(t *testing.T) {
	for _, p := range doorProviders {
		t.Run(p.name, func(t *testing.T) {
			fake := &fakeProviders{replies: []string{
				`{"javascriptCode":"final('Find reference', {})"}`,
				`{"javascriptCode":"const found = await mcp_neko_memory_search({query:'reference'}); final('Report', {reference:'REF-42', found});"}`,
				"Answer: REF-42",
			}}
			tools := &fakeTools{reply: []any{}}
			res := doorTurn(t, fake, &llm.AxBackendConfig{Route: p.route("model-a")}, map[string]string{"MODEL_API_KEY": "planted-" + p.name}, tools)
			if res.Status != "completed" || !strings.Contains(res.FinalText, "REF-42") {
				t.Fatalf("result %+v", res)
			}
			checkDoorCalls(t, p, "model-a", fake.all(), "planted-"+p.name)
			tools.mu.Lock()
			defer tools.mu.Unlock()
			if len(tools.calls) == 0 {
				t.Fatal("the turn called no tool")
			}
		})
	}
}

func TestKeyDoorCarriesAFallbackAfterA503ForEachProvider(t *testing.T) {
	for _, p := range doorProviders {
		t.Run(p.name, func(t *testing.T) {
			fake := &fakeProviders{replies: doorActorReplies, fail: map[string]int{p.keyHeader("primary-" + p.name): 503}}
			fallback := p.route("model-b")
			cfg := &llm.AxBackendConfig{Route: p.route("model-a"), Fallback: &fallback}
			res := doorTurn(t, fake, cfg, map[string]string{"MODEL_API_KEY": "primary-" + p.name, llm.FallbackKeyEnv: "fallback-" + p.name}, &fakeTools{reply: []any{}})
			if res.Status != "completed" || !strings.Contains(res.FinalText, "REF-42") {
				t.Fatalf("result %+v", res)
			}
			var primary, second []providerCall
			for _, c := range fake.all() {
				if c.key == p.keyHeader("primary-"+p.name) {
					primary = append(primary, c)
				} else {
					second = append(second, c)
				}
			}
			if len(primary) == 0 {
				t.Fatal("the primary route got no call")
			}
			checkDoorCalls(t, p, "model-a", primary, "primary-"+p.name)
			checkDoorCalls(t, p, "model-b", second, "fallback-"+p.name)
		})
	}
}

// Embedding speaks one wire format, OpenAI's /embeddings with a bearer key,
// at whatever URL hosts it. Anthropic and Azure have no such endpoint here.
func TestKeyDoorCarriesAnEmbeddingCallForEachEmbeddingURL(t *testing.T) {
	for _, c := range []struct{ name, url, host, path string }{
		{"openai", "https://api.openai.com/v1", "api.openai.com", "/v1/embeddings"},
		{"google-gemini", "https://generativelanguage.googleapis.com/v1beta/openai", "generativelanguage.googleapis.com", "/v1beta/openai/embeddings"},
		{"openai-compatible", "http://embedder.internal:8080/v1", "embedder.internal:8080", "/v1/embeddings"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var got []providerCall
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				got = append(got, providerCall{host: r.Header.Get("X-Test-Upstream-Host"), path: r.URL.Path, key: r.Header.Get("Authorization"), headers: r.Header.Clone()})
				mu.Unlock()
				fakeJSON(w, map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float64{1, 2, 3}}}})
			}))
			defer model.Close()
			target, _ := url.Parse(model.URL)
			socket := strictServe(t)
			door := &llm.KeyDoor{Transport: toFake{target}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := keydoor.SocketPath(os.Getenv, socket)
			if _, err := llm.ListenDoor(ctx, path, door); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NEKO_EMBEDDING_URL", c.url)
			t.Setenv("NEKO_EMBEDDING_MODEL", "fixture")
			t.Setenv("NEKO_EMBEDDING_API_KEY", "planted-embed-"+c.name)
			t.Setenv("NEKO_EMBEDDING_DIMENSIONS", "3")
			if v, err := (&API{Door: door, doorPath: path}).EmbedText(ctx, "a memory"); err != nil || len(v) != 3 {
				t.Fatalf("vector %v, %v", v, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) != 1 || got[0].host != c.host || got[0].path != c.path || got[0].key != "Bearer planted-embed-"+c.name {
				t.Fatalf("the provider got %+v", got)
			}
		})
	}
}
