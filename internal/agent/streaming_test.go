package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestResponderDeltaArrivesBeforeProviderFinishes(t *testing.T) {
	var calls atomic.Int32
	firstObserved := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := calls.Add(1)
		if call < 3 {
			w.Header().Set("Content-Type", "application/json")
			answer := `{"javascriptCode":"final('Answer',{})"}`
			_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, answer)
			return
		}
		if call != 3 || !strings.Contains(string(body), `"stream":true`) {
			http.Error(w, "expected one streaming responder", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Answer: Hello \"}}]}\n\n")
		flusher.Flush()
		select {
		case <-firstObserved:
		case <-time.After(5 * time.Second):
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	var deltas []string
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "stream", InputID: "input", Prompt: "Say hello", StreamResponses: true}, client, Tools{}, func(event Event) error {
		if event.Type != "answer.delta" {
			return nil
		}
		if event.Sequence != 0 {
			t.Errorf("provisional event advanced durable sequence: %+v", event)
		}
		var data struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Errorf("invalid delta: %v", err)
		}
		deltas = append(deltas, data.Text)
		once.Do(func() { close(firstObserved) })
		return nil
	})
	if err != nil || result.Status != "completed" || result.Answer != "Hello world" || len(deltas) < 2 || calls.Load() != 3 {
		t.Fatalf("result=%+v err=%v deltas=%q calls=%d", result, err, deltas, calls.Load())
	}
}
