package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/command"
	"github.com/open-neko/harness/internal/session"
)

func TestInspectorDerivesPinnedRoutingWithoutCredentials(t *testing.T) {
	manifest := `{"context":"cheap","executor":"cheap","responder":"cheap","routes":[{"key":"cheap","model":"fixture","url":"https://example.invalid/v1","api_key_env":"HARNESS_CHEAP_KEY"}]}`
	digest, err := command.RoutingDigest(manifest)
	if err != nil || digest == "" {
		t.Fatal(err)
	}
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "routed-inspection", InputID: "input", Prompt: "Answer", HostRoutingDigest: digest}
	answers := []string{`{"javascriptCode":"final('Answer',{})"}`, `{"javascriptCode":"final('Answer',{})"}`, `{"answer":"Done"}`}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(answers) == 0 {
			http.Error(w, "too many calls", 400)
			return
		}
		response := answers[0]
		answers = answers[1:]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", response), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	result, err := session.RunWithTools(context.Background(), root, spec, client, agent.Tools{}, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" {
		t.Fatalf("run=%+v err=%v", result, err)
	}
	t.Setenv("HARNESS_STATE_DIR", root)
	t.Setenv("HARNESS_MODEL_ROUTES", manifest)
	oldStdin, oldStdout, oldArgs := os.Stdin, os.Stdout, os.Args
	defer func() { os.Stdin, os.Stdout, os.Args = oldStdin, oldStdout, oldArgs }()
	input := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(input, []byte(`{"version":1,"run_id":"routed-inspection","input_id":"input","prompt":"Answer"}`), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	output, err := os.Create(filepath.Join(t.TempDir(), "output.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	os.Stdin, os.Stdout, os.Args = read, output, []string{"harness-inspect"}
	if err := inspect(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"outcome":"terminal"`) {
		t.Fatalf("inspection=%s", data)
	}
	changed := strings.Replace(manifest, "fixture", "other-model", 1)
	t.Setenv("HARNESS_MODEL_ROUTES", changed)
	if _, err := read.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := inspect(); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("changed route accepted: %v", err)
	}
}
