package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// The responder is the turn's one streamed model call. Its provider usage
// arrives in the last stream chunk and must reach the org spend meter.
func TestTurnSpendIncludesTheStreamedResponder(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	ctx := t.Context()
	responses := []string{
		`{"javascriptCode":"final('Find reference', {})"}`,
		`{"javascriptCode":"final('Report', {reference:'REF-42'});"}`,
		"Answer: REF-42",
	}
	var mu sync.Mutex
	var buffered, streamed int
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		content := responses[min(buffered+streamed, len(responses)-1)]
		stream := strings.Contains(string(body), `"stream":true`)
		if stream {
			streamed++
		} else {
			buffered++
		}
		mu.Unlock()
		if stream {
			w.Header().Set("content-type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0,
				"delta": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk,
				`{"choices":[],"model":"fixture","usage":{"prompt_tokens":40,"completion_tokens":60,"total_tokens":100}}`)
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
	}))
	defer model.Close()

	dir := testsock.Dir(t)
	socket := filepath.Join(dir, "serve.sock")
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, stop := context.WithCancel(context.Background())
	serveDoor(t, socket)
	srv := (&serve.Server{Getenv: os.Getenv, MaxTurns: 2}).RunUnconfinedForTest()
	done := make(chan struct{})
	go func() { _ = srv.Serve(serveCtx, ln); close(done) }()
	defer func() { stop(); <-done }()

	ws, err := turnWorkspace("org", threadMine, runAdmin)
	if err != nil {
		t.Skipf("workspace: %v", err)
	}
	for _, d := range []string{ws.SkillsRoot, ws.RunRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	seed(t, db, `insert into spend_reservation (org_id, work_run_id, source, reserved_micros) values ('org', $1, 'chat', 3000000)`, runAdmin)
	store := f.API.newRunEventStore("org", threadMine, runAdmin)
	route := llm.AxModelRoute{Provider: "openai-compatible", Model: "fixture", URL: model.URL, KeyEnv: "OPENAI_API_KEY",
		Price: &llm.Price{InputUSDPer1M: 1, OutputUSDPer1M: 1}}
	res, err := f.API.runAgentCore(ctx, agentCoreInput{
		Backend: &llm.AxBackendConfig{Route: route},
		Prompt:  "Find reference",
		OrgID:   "org", ThreadID: threadMine, RunID: runAdmin,
		Workspace: ws, Keys: map[string]string{"OPENAI_API_KEY": "k"}, Socket: socket,
		NativeDelegationOff: true,
		FilesEnabled:        func() bool { return false },
		toolBackend:         func(turnBinding) toolBackend { return &fakeTools{reply: []any{}} },
		withoutScriptData:   true,
		Emit:                store.emit,
	})
	if err != nil || res.Status != "completed" {
		t.Fatalf("result %+v err %v", res, err)
	}
	if err := store.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	want := int64(5*buffered + 100*streamed)
	calls := streamed
	mu.Unlock()
	if calls != 1 {
		t.Fatalf("%d streamed model calls", calls)
	}
	var tokens, micros int64
	var priced string
	if err := db.QueryRow(ctx, `select tokens, cost_micros, priced from spend_ledger where work_run_id = $1`, runAdmin).Scan(&tokens, &micros, &priced); err != nil {
		t.Fatal(err)
	}
	if tokens != want || micros != want || priced != "estimated" {
		t.Fatalf("spend row: %d tokens, %d micros, %s; want %d tokens and micros", tokens, micros, priced, want)
	}
	var coverage string
	if err := db.QueryRow(ctx, `select payload->'usage'->>'coverage' from work_run_event where run_id = $1 and kind = 'usage'`, runAdmin).Scan(&coverage); err != nil || coverage != "complete" {
		t.Fatalf("usage coverage %q %v", coverage, err)
	}
}
