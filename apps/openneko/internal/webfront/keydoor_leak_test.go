package webfront

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

// frameTap sits between web and serve on the serve socket and keeps every
// byte that crosses it, both ways.
func frameTap(t *testing.T, serveSocket string) (string, *lockedBuffer) {
	t.Helper()
	path := filepath.Join(filepath.Dir(serveSocket), "tap.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	seen := &lockedBuffer{}
	go func() {
		for {
			web, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer web.Close()
				srv, err := net.Dial("unix", serveSocket)
				if err != nil {
					return
				}
				defer srv.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(io.MultiWriter(srv, seen), web); done <- struct{}{} }()
				go func() { _, _ = io.Copy(io.MultiWriter(web, seen), srv); done <- struct{}{} }()
				<-done
			}()
		}
	}()
	return path, seen
}

func TestAPlantedKeyStaysInTheKeyDoor(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	raw := make([]byte, 12)
	_, _ = rand.Read(raw)
	planted := "PLANTED-" + hex.EncodeToString(raw)

	var mu sync.Mutex
	var auths []string
	failing := false
	replies := []string{
		`{"javascriptCode":"final('Summarise stock', {})"}`,
		`{"javascriptCode":"final('Report', {status:'fine'});"}`,
		"Stock is fine.",
	}
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		fail := failing
		content := replies[min(calls, len(replies)-1)]
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case fail:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided: `+planted+`","type":"invalid_request_error"}}`)
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float64{1, 2, 3}}}})
		case strings.Contains(string(body), `"stream":true`):
			w.Header().Set("Content-Type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}}}})
			_, _ = io.WriteString(w, "data: "+string(chunk)+"\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		}
	}))
	defer model.Close()

	sealed, err := config.EncryptValue(a.SecretsDir, planted)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, db, `delete from llm_provider_config where org_id = 'org'`)
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, model, enabled, config, secrets)
		values ('org', 'primary', 'openai-compatible', 'fixture', true, $1, $2)`,
		`{"url":"`+model.URL+`/v1"}`, `{"apiKey":"`+sealed+`"}`)

	serveSocket := strictServe(t)
	tap, frames := frameTap(t, serveSocket)
	t.Setenv("OPENNEKO_SERVE_SOCKET", tap)
	logs := captureLog(t)
	tracePath := filepath.Join(t.TempDir(), "trace.jsonl")
	if a.EvalTrace, err = OpenEvalTrace(tracePath, nil, nil); err != nil {
		t.Fatal(err)
	}

	events := &eventLog{}
	turn := func(runID string) chatTurnResult {
		t.Helper()
		untrace := a.EvalTrace.register(runID)
		defer untrace()
		store := a.newRunEventStore("org", threadMine, runID)
		emit := func(ctx context.Context, e any) error { _ = events.emit(ctx, e); return store.emit(ctx, e) }
		res, err := a.runChatTurn(ctx, chatTurnOptions{OrgID: "org", ThreadID: threadMine, RunID: runID,
			Message: "Summarise stock", Channel: "web", IncludeUxMetadata: true, NativeDelegationOff: true, Emit: emit,
			Summarize: noSummary(t), keepBot: func(context.Context, botKeepInput) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.finalize(ctx); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := turn(runAdmin); res.Status != "completed" || res.FinalText != "Stock is fine." {
		t.Fatalf("first turn %+v\n%s", res, strings.Join(events.all(), "\n"))
	}

	settings := llm.Settings{Source: "org", Provider: "openai-compatible", Model: "fixture", Enabled: true,
		Config: map[string]any{"url": model.URL + "/v1"}, Secrets: map[string]string{"apiKey": planted}}
	if err := a.hostCalls().TestPrimary(ctx, "org", settings); err != nil {
		t.Fatalf("host call: %v", err)
	}
	t.Setenv("NEKO_EMBEDDING_URL", model.URL+"/v1")
	t.Setenv("NEKO_EMBEDDING_MODEL", "fixture")
	t.Setenv("NEKO_EMBEDDING_API_KEY", planted)
	t.Setenv("NEKO_EMBEDDING_DIMENSIONS", "3")
	if _, err := a.EmbedText(ctx, "a memory"); err != nil {
		t.Fatalf("embedding: %v", err)
	}

	mu.Lock()
	failing = true
	mu.Unlock()
	var failedRun string
	if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, actor_user_id, actor_role) values ('org', $1, 'ax', 'admin', 'admin') returning id::text`,
		threadMine).Scan(&failedRun); err != nil {
		t.Fatal(err)
	}
	failed := turn(failedRun)
	hostErr := a.hostCalls().TestPrimary(ctx, "org", settings)
	_, embedErr := a.EmbedText(ctx, "another memory")
	if failed.Status != "failed" || hostErr == nil || embedErr == nil {
		t.Fatalf("with a refused key: turn %+v, host call %v, embedding %v", failed, hostErr, embedErr)
	}

	mu.Lock()
	for _, auth := range auths {
		if auth != "Bearer "+planted {
			t.Errorf("the provider got %q, want the planted key", auth)
		}
	}
	mu.Unlock()
	trace, _ := os.ReadFile(tracePath)
	var dump strings.Builder
	rows, err := db.Query(ctx, `select table_name from information_schema.tables where table_schema = current_schema() and table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	for _, table := range tables {
		var text *string
		if err := db.QueryRow(ctx, `select string_agg(t::text, E'\n') from "`+table+`" t`).Scan(&text); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		if text != nil && strings.Contains(*text, planted) {
			t.Errorf("table %s holds the planted key", table)
		}
		if text != nil {
			dump.WriteString(*text)
		}
	}
	if !strings.Contains(dump.String(), "Stock is fine.") {
		t.Fatal("the database holds no answer: the database check read nothing")
	}
	var stored int
	if err := db.QueryRow(ctx, `select count(*) from work_run_event where run_id in ($1, $2)`, runAdmin, failedRun).Scan(&stored); err != nil || stored == 0 {
		t.Fatalf("%d stored events: %v", stored, err)
	}
	for name, text := range map[string]string{
		"a frame between web and serve": frames.String(),
		"a log line":                    logs.String(),
		"the trace":                     string(trace),
		"an event":                      strings.Join(events.all(), "\n"),
		"the failed turn's error":       failed.Error,
		"the host call's error":         hostErr.Error(),
		"the embedding call's error":    embedErr.Error(),
	} {
		if strings.Contains(text, planted) {
			t.Errorf("%s holds the planted key", name)
		}
	}
	if !strings.Contains(hostErr.Error(), "Incorrect API key provided: [redacted]") || !strings.Contains(frames.String(), "Incorrect API key provided: [redacted]") {
		t.Fatalf("the provider's error text did not come back through serve: %v", hostErr)
	}
	if !strings.Contains(frames.String(), `"type":"start"`) || !strings.Contains(frames.String(), `"type":"generate"`) || !strings.Contains(frames.String(), `"type":"embed"`) {
		t.Fatal("the tap saw no start, generate or embed frame")
	}
	if len(trace) == 0 {
		t.Fatal("the trace is empty: the trace check read nothing")
	}
}
