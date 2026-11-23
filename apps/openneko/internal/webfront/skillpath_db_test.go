package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// TestWorkflowStepSkillPathResolvesFromTheRunFolder runs a workflow whose step
// names a skill by its path from the organisation folder. The model takes the
// path from the prompt and runs the skill's script after a cd to the run
// folder, as agents do. OPENNEKO_TEST_SANDBOX_URL runs it against the image.
func TestWorkflowStepSkillPathResolvesFromTheRunFolder(t *testing.T) {
	box := startRecipeSandbox(t, false)
	a, _, wf := workflowTurnFront(t, `[{"id":"s1","description":"Use skills/stock-check/ for the count"}]`)
	ws, err := turnWorkspace("org", wfTurnThread, wfTurnWorkRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws.SkillsRoot, "stock-check"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws.SkillsRoot, "stock-check", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"SKILL.md": "---\nname: stock-check\ndescription: Checks stock\n---\nGUIDE-BODY-7731\n",
		"scripts/x.py": "print('SCRIPT-RAN-' + str(5520 + 1))\n"} {
		if err := os.WriteFile(filepath.Join(ws.SkillsRoot, "stock-check", name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OPENNEKO_SANDBOX_URL", box.URL)
	t.Setenv("OPENNEKO_SANDBOX_KEY", box.Key)
	t.Setenv(OperatorEgressEnv, "1")

	stepPath := regexp.MustCompile(`1\. Use ([^ \\"]+)/ for`)
	var mu sync.Mutex
	var bodies []string
	reply := func(body string) string {
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, body)
		switch len(bodies) {
		case 1:
			return `{"javascriptCode":"final('Read the guide', {})"}`
		case 2:
			path := "no-step-path"
			if m := stepPath.FindStringSubmatch(bodies[0]); m != nil {
				path = m[1]
			}
			script := `cd \"$RUN_DIR\" && python3 \"` + path + `/scripts/x.py\"`
			guide := `cat \"$SKILLS_DIR/stock-check/SKILL.md\"`
			return `{"javascriptCode":"const ran = await terminal({command:'` + script + `'}); const guide = await terminal({command:'` + guide + `'}); final('Report', {ran, guide});"}`
		}
		return "The guide was read."
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		content := reply(string(raw))
		if strings.Contains(string(raw), `"stream":true`) {
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}}}})
			end, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
			w.Header().Set("content-type", "text/event-stream")
			_, _ = w.Write([]byte("data: " + string(chunk) + "\n\ndata: " + string(end) + "\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(model.Close)
	dir := testsock.Dir(t)
	socket := filepath.Join(dir, "serve.sock")
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	serveDoor(t, socket)
	ctx, stop := context.WithCancel(context.Background())
	srv := (&serve.Server{Getenv: os.Getenv, MaxTurns: 2, Sandbox: box}).RunUnconfinedForTest()
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { stop(); <-done; os.RemoveAll(dir) })
	t.Setenv("OPENNEKO_SERVE_SOCKET", socket)

	log := &eventLog{}
	o := workflowTurnOpts(wf, log)
	o.resolve = fixedBackend(model.URL)
	res, err := a.runWorkflowTurn(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	seen := append([]string{}, bodies...)
	mu.Unlock()
	if len(seen) < 3 {
		t.Fatalf("the model saw %d calls; result %+v\nevents %s", len(seen), res, strings.Join(log.all(), "\n"))
	}
	after := strings.Join(seen[2:], "\n")
	if !strings.Contains(after, "SCRIPT-RAN-5521") || !strings.Contains(after, "GUIDE-BODY-7731") {
		at := max(0, strings.Index(after, `\"ran\"`))
		t.Fatalf("the terminal did not read the guide by the prompt's path: %s", after[at:min(len(after), at+400)])
	}
	if m := stepPath.FindStringSubmatch(seen[0]); m == nil || m[1] != "$SKILLS_DIR/stock-check" {
		t.Fatalf("the step names the skill as %v", m)
	}
	if !regexp.MustCompile(`The skills folder is ` + regexp.QuoteMeta(agentRunsRoot()) + `/[^ ]+/skills\.`).MatchString(seen[0]) {
		t.Fatal("the prompt does not give the stage's skills folder")
	}
	if res.Status != "completed" {
		t.Fatalf("result %+v", res)
	}
}
