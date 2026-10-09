package session

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
)

func TestStreamDeltasAreLiveOnlyOnTerminalReplay(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := calls.Add(1)
		if n < 3 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, `{"javascriptCode":"final('Answer',{})"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Answer: Hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	spec := agent.Spec{Version: 1, RunID: "stream-replay", InputID: "input", Prompt: "Say hello", StreamResponses: true}
	root := t.TempDir()
	var live []agent.Event
	result, err := RunWithTools(context.Background(), root, spec, client, agent.Tools{}, func(e agent.Event) error {
		live = append(live, e)
		return nil
	})
	if err != nil || result.Status != "completed" || result.Answer != "Hello" {
		t.Fatalf("live result=%+v err=%v", result, err)
	}
	deltas := 0
	sequence := uint64(0)
	for _, e := range live {
		if e.Type == "answer.delta" {
			deltas++
			if e.Sequence != 0 {
				t.Fatalf("provisional event has durable sequence: %+v", e)
			}
			continue
		}
		sequence++
		if e.Sequence != sequence {
			t.Fatalf("broken durable sequence: %+v", e)
		}
	}
	if deltas == 0 {
		t.Fatal("missing live responder delta")
	}
	var replay []agent.Event
	repeated, err := RunWithTools(context.Background(), root, spec, nil, agent.Tools{}, func(e agent.Event) error {
		replay = append(replay, e)
		return nil
	})
	if err != nil || repeated.Answer != result.Answer || calls.Load() != 3 || len(replay) != int(sequence) {
		t.Fatalf("replay=%+v err=%v calls=%d events=%d", repeated, err, calls.Load(), len(replay))
	}
	for _, e := range replay {
		if e.Type == "answer.delta" {
			t.Fatal("provisional delta persisted into replay")
		}
	}
	// The persisted checkpoint remains a valid bounded JSON record.
	if report, err := Inspect(root, spec); err != nil || report.Outcome != "terminal" {
		t.Fatalf("checkpoint report=%+v err=%v", report, err)
	}
}
