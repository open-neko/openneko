package harness

import (
	"bytes"
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
)

func TestFileWriteExpandsTheRunFolderOfTheTurn(t *testing.T) {
	workspace := t.TempDir()
	runDir := filepath.Join(workspace, "runs", "r1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	responses := []string{`{"javascriptCode":"final('Write it', {})"}`,
		`{"javascriptCode":"const r = file_write({path:'$RUN_DIR/a.py', content:'print(1)'}); final('Report', {path: r.path});"}`, `Answer: done`}
	var mu sync.Mutex
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
		"OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=thread-1", "OPENNEKO_HARNESS_WORKSPACE_DIR=" + workspace, "RUN_DIR=" + runDir,
		"HARNESS_MODEL_URL=" + model.URL, "HARNESS_MODEL=fixture", "HARNESS_MODEL_API_KEY=k",
	}
	var out bytes.Buffer
	code, err := RunTurn(context.Background(), Turn{Environ: environ,
		In: strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Write it"}`), Out: &out})
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v out=%s", code, err, out.String())
	}
	if data, err := os.ReadFile(filepath.Join(runDir, "a.py")); err != nil || string(data) != "print(1)" {
		t.Fatalf("file_write did not put $RUN_DIR/a.py in the run folder: %v; out=%s", err, out.String())
	}
}
