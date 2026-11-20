package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func TestABudgetSummaryTurnEndsItsStreamWithDone(t *testing.T) {
	clearAxEnv(t, map[string]string{"OPENNEKO_AGENT_MAX_TURNS": "2"})
	f, db := signedInChatFront(t)
	a := f.API
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	oldPoll, oldIdle := runStreamPoll, runEventFlushAfter
	t.Cleanup(func() { runStreamPoll, runEventFlushAfter = oldPoll, oldIdle })
	runStreamPoll, runEventFlushAfter = time.Hour, time.Millisecond

	var mu sync.Mutex
	calls, summaries := 0, 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		content := `{"javascriptCode":"console.log('still working')"}`
		switch {
		case strings.Contains(string(body), "budget before it finished"):
			summaries++
			content = "Answer: Partial answer from the budget summary."
		case calls == 1:
			content = `{"javascriptCode":"final('Look', {})"}`
		}
		mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("content-type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}}}})
			end, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
			_, _ = w.Write([]byte("data: " + string(chunk) + "\n\ndata: " + string(end) + "\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(model.Close)
	socket := filepath.Join(testsock.Dir(t), "serve.sock")
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan struct{})
	serveDoor(t, socket)
	server := &serve.Server{Getenv: os.Getenv, MaxTurns: 2}
	go func() { _ = server.Serve(ctx, ln); close(served) }()
	defer func() { stop(); <-served }()

	a.turnResolve = fixedBackend(model.URL)
	var result axRunResult
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		ws, err := turnWorkspace(in.OrgID, in.ThreadID, in.RunID)
		if err != nil {
			t.Skipf("workspace: %v", err)
		}
		for _, d := range []string{ws.SkillsRoot, ws.RunRoot} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return axRunResult{}, err
			}
		}
		in.Workspace, in.Socket, in.NativeDelegationOff, in.withoutScriptData = ws, socket, true, true
		in.FilesEnabled = func() bool { return false }
		in.toolBackend = func(turnBinding) toolBackend { return &fakeTools{reply: []any{}} }
		res, err := a.runAgentCore(ctx, in)
		result = res
		return res, err
	}

	w := call(f, "POST", "/api/v1/work/threads/"+threadMine+"/runs", `{"message":"Compare sales by territory"}`, sessionCookie("member"))
	runID, _ := decode(t, w)["runId"].(string)
	if runID == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	s := openStream(t, srv, "/api/v1/work/threads/"+threadMine+"/runs/"+runID+"/events", "member")
	var frames []string
	for {
		frame := s.next(60 * time.Second)
		if frame == "" {
			break
		}
		frames = append(frames, frame)
	}
	mu.Lock()
	n, summed := calls, summaries
	mu.Unlock()
	if !result.Degraded || !strings.Contains(result.FinalText, "Partial answer") || summed != 1 {
		t.Fatalf("the turn did not take the budget summary path: %+v, %d model calls, %d summaries", result, n, summed)
	}
	done := -1
	for i, frame := range frames {
		if strings.Contains(frame, `"type":"done"`) {
			done = i
		}
	}
	if done < 0 {
		t.Fatalf("the live stream has no done event; last frames: %q", frames[max(0, len(frames)-4):])
	}
	for _, frame := range frames[done+1:] {
		if !strings.Contains(frame, `"type":"telemetry"`) {
			t.Fatalf("an event after done: %q", frame)
		}
	}
	last := rowText(t, db, `select payload->>'type' from work_run_event where run_id = $1 and payload->>'type' <> 'telemetry' order by id desc limit 1`, runID)
	if last != "done" {
		t.Fatalf("the last stored event is %q", last)
	}
}
