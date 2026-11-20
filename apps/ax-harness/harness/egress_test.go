package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func TestTerminalSendsTheOrgsEgressList(t *testing.T) {
	var mu sync.Mutex
	var egress []*sandbox.Egress
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct {
				Egress *sandbox.Egress `json:"egress"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			egress = append(egress, body.Egress)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 0, "output": "ok"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer box.Close()
	responses := []string{`{"javascriptCode":"final('Run it', {})"}`, `{"javascriptCode":"const r = terminal({command:'true'}); final('Report', {code: r.exit_code});"}`, `Answer: done`}
	call := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		n := call
		call++
		mu.Unlock()
		if n >= len(responses) {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": responses[n]}, "finish_reason": "stop"}}})
	}))
	defer model.Close()
	environ := []string{
		"OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=thread-1", "OPENNEKO_HARNESS_RUN_ID=work-run-1",
		"OPENNEKO_HARNESS_SHELL=1", "OPENNEKO_HARNESS_WORKSPACE_DIR=" + t.TempDir(),
		"OPENNEKO_HARNESS_EGRESS_HOSTS=API.example.com, pypi.org",
		"HARNESS_MODEL_URL=" + model.URL, "HARNESS_MODEL=fixture", "HARNESS_MODEL_API_KEY=k",
	}
	var out bytes.Buffer
	code, err := RunTurn(context.Background(), Turn{Environ: environ, Sandbox: sandbox.Config{URL: box.URL},
		In: strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Run it"}`), Out: &out})
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v out=%s", code, err, out.String())
	}
	if len(egress) != 1 || egress[0] == nil {
		t.Fatalf("the terminal sent egress %v; out=%s", egress, out.String())
	}
	if e := egress[0]; e.Org != "org-1" || e.Label != "work-run-1" || !slices.Equal(e.Hosts, []string{"api.example.com", "pypi.org"}) {
		t.Fatalf("egress = %+v", e)
	}
}
