package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	axgoja "github.com/ax-llm/ax/packages/go/runtime/goja"
)

// This pins the Ax behavior a lifecycle state hook would rely on: steering
// queued from a host tool result must reach a subsequent model boundary.
func TestAxRunControlAppliesToolResultSteeringAtModelBoundary(t *testing.T) {
	answers := []string{
		`{"javascriptCode":"final('Read the fixture',{})"}`,
		`{"javascriptCode":"const row=read({}); final('Use the row',{row});"}`,
		`{"answer":"REF-42"}`,
	}
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		index := len(requests)
		requests = append(requests, string(body))
		mu.Unlock()
		if index >= len(answers) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	control := ax.RunControl()
	runtime := axgoja.NewRuntime()
	runtime.RegisterCallable("read", func(ax.Value) (ax.Value, error) {
		if err := control.Steer("Host state updated: REF-42"); err != nil {
			return nil, err
		}
		return ax.Object("reference", "REF-42"), nil
	})
	agent := ax.NewAgent("question:string -> answer:string", ax.Object("runtime", runtime,
		"instruction", "Read the fixture then answer.", "directResponse", "off", "maxSteps", 8, "validationRetries", 0, "infraRetries", 0))
	output, err := agent.ForwardWithHooks(context.Background(), client, ax.Object("question", "Find reference"),
		ax.Object("control", control, "maxSteps", 8, "validationRetries", 0, "infraRetries", 0), ax.AxRuntimeHooks{})
	mu.Lock()
	seen := append([]string(nil), requests...)
	mu.Unlock()
	if err != nil || len(seen) != 3 || !strings.Contains(toJSON(output), "REF-42") {
		t.Fatalf("output=%v err=%v requests=%d", output, err, len(seen))
	}
	if strings.Contains(seen[0], "Host state updated") || strings.Contains(seen[1], "Host state updated") ||
		strings.Count(seen[2], "Host state updated: REF-42") != 1 {
		t.Fatalf("steering did not reach exactly the next boundary: %q", seen)
	}
}
